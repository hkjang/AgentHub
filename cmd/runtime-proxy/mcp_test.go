package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gatewayFor(t *testing.T, upstreamHandler http.HandlerFunc, policy mcpUpstream) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	origin := httptest.NewServer(upstreamHandler)
	t.Cleanup(origin.Close)
	policy.Upstream = origin.URL
	if policy.Name == "" {
		policy.Name = "context7"
	}
	audit := &[]map[string]any{}
	gateway := httptest.NewServer(mcpGateway([]mcpUpstream{policy}, func(entry map[string]any) { *audit = append(*audit, entry) }))
	t.Cleanup(gateway.Close)
	return gateway, audit
}

func call(t *testing.T, gateway *httptest.Server, server, body string) (*http.Response, string) {
	t.Helper()
	response, err := http.Post(gateway.URL+"/mcp/"+server, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("gateway call failed: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	raw, _ := io.ReadAll(response.Body)
	return response, string(raw)
}

func TestDeniedToolNeverReachesTheServer(t *testing.T) {
	reached := false
	gateway, audit := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}, mcpUpstream{Mode: "deny", Tools: []string{"delete-library"}})

	response, body := call(t, gateway, "context7", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete-library"}}`)
	if reached {
		t.Fatal("a denied tool call must not be forwarded upstream")
	}
	// A policy decision is a JSON-RPC error, not a transport failure.
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(body, "차단") {
		t.Fatalf("the caller should be told why: %s", body)
	}
	if len(*audit) != 1 || (*audit)[0]["decision"] != "denied" {
		t.Fatalf("the decision must be audited: %#v", *audit)
	}
}

func TestAllowedToolIsForwarded(t *testing.T) {
	gateway, audit := gatewayFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("credential not attached: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"content":[]}}`))
	}, mcpUpstream{Mode: "allow", Tools: []string{"get-library-docs"}, AuthHeader: "Authorization", AuthTemplate: "Bearer %s", CredentialEnv: "TEST_MCP_CREDENTIAL"})
	t.Setenv("TEST_MCP_CREDENTIAL", "secret-token")

	_, body := call(t, gateway, "context7", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get-library-docs"}}`)
	if !strings.Contains(body, `"result"`) {
		t.Fatalf("an allowed call should reach the server: %s", body)
	}
	if len(*audit) != 1 || (*audit)[0]["decision"] != "allowed" {
		t.Fatalf("an allowed call is audited too: %#v", *audit)
	}
}

// An allow policy that lists nothing permits nothing. The alternative reading —
// an empty list meaning "everything" — turns a misconfiguration into open access.
func TestEmptyAllowListPermitsNothing(t *testing.T) {
	policy := mcpUpstream{Mode: "allow"}
	if policy.permits("anything") {
		t.Fatal("an empty allow list must not permit a tool")
	}
	if !(mcpUpstream{}).permits("anything") {
		t.Fatal("no policy at all must leave the binding unrestricted")
	}
}

func TestToolListIsFilteredToWhatMayBeCalled(t *testing.T) {
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"resolve-library-id"},{"name":"get-library-docs"},{"name":"delete-library"}]}}`))
	}, mcpUpstream{Mode: "deny", Tools: []string{"delete-library"}})

	_, body := call(t, gateway, "context7", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if strings.Contains(body, "delete-library") {
		t.Fatalf("a tool that cannot be called must not be advertised: %s", body)
	}
	for _, want := range []string{"resolve-library-id", "get-library-docs"} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s should still be listed: %s", want, body)
		}
	}
}

// Streamable HTTP answers with SSE, so the filter has to survive the framing.
func TestToolListIsFilteredInsideAnEventStream(t *testing.T) {
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"tools\":[{\"name\":\"keep\"},{\"name\":\"drop\"}]}}\n\n"))
	}, mcpUpstream{Mode: "allow", Tools: []string{"keep"}})

	response, body := call(t, gateway, "context7", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("framing must be preserved, got %q", got)
	}
	if strings.Contains(body, `"drop"`) || !strings.Contains(body, `"keep"`) {
		t.Fatalf("event stream filtering is wrong: %q", body)
	}
	if !strings.Contains(body, "event: message") {
		t.Fatalf("non-data lines must survive: %q", body)
	}
}

// Anything that is not a tools/list result passes through untouched, so an error
// or an unfamiliar shape is never mangled by the rewriter.
func TestUnexpectedShapesPassThrough(t *testing.T) {
	policy := mcpUpstream{Mode: "allow", Tools: []string{"keep"}}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text"}]}}`,
		`not json at all`,
	} {
		if got := string(rewriteToolsPayload([]byte(raw), policy)); got != raw {
			t.Errorf("payload %q was rewritten to %q", raw, got)
		}
	}
}

func TestUnknownServerIsRejected(t *testing.T) {
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {}, mcpUpstream{Name: "context7"})
	response, _ := call(t, gateway, "other", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

// The denial has to carry the caller's own request id, or the client cannot
// match the error to the call it made.
func TestDenialEchoesTheRequestID(t *testing.T) {
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {}, mcpUpstream{Mode: "allow"})
	_, body := call(t, gateway, "context7", `{"jsonrpc":"2.0","id":"abc-1","method":"tools/call","params":{"name":"x"}}`)
	var decoded struct {
		ID    string `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("denial is not valid JSON-RPC: %v (%s)", err, body)
	}
	if decoded.ID != "abc-1" || decoded.Error.Code == 0 {
		t.Fatalf("denial = %+v, want the request id and an error code", decoded)
	}
}

func TestLoadUpstreamsRejectsIncompleteEntries(t *testing.T) {
	if _, err := loadUpstreams(`[{"name":"a"}]`); err == nil {
		t.Fatal("an entry without an upstream must be rejected")
	}
	if _, err := loadUpstreams(`not json`); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
	got, err := loadUpstreams(``)
	if err != nil || got != nil {
		t.Fatalf("an empty config is not an error: %v %v", got, err)
	}
}
