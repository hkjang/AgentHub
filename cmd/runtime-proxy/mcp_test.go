package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
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

// The platform's policy and the agent's own list are different statements. The
// gateway is where both are enforced, and the one an agent's owner controls must
// not be able to widen the one they do not.
func TestPlatformPolicyOverridesTheAgentsOwnList(t *testing.T) {
	var audited []map[string]any
	upstream := mcpUpstream{
		Name: "github", Upstream: "http://127.0.0.1:1/mcp",
		// The owner allowed the tool explicitly...
		Mode: "allow", Tools: []string{"delete_branch", "read_file"},
		// ...and the platform forbids it.
		PolicyDenied: []string{"github/delete_*"},
	}
	handler := mcpGatewayWithApprover([]mcpUpstream{upstream}, func(entry map[string]any) { audited = append(audited, entry) }, nil)

	recorder := httptest.NewRecorder()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_branch"}}`
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/github", strings.NewReader(body)))
	if !strings.Contains(recorder.Body.String(), "플랫폼 정책") {
		t.Fatalf("the refusal must name the platform: %s", recorder.Body.String())
	}
	if len(audited) != 1 || audited[0]["policy"] != true {
		t.Fatalf("the audit entry must distinguish a platform denial: %#v", audited)
	}

	// What the owner allowed and the platform did not forbid still works.
	audited = nil
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/github",
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file"}}`))) //nolint:bodyclose
	if strings.Contains(recorder.Body.String(), "차단") {
		t.Fatalf("an unrestricted tool must not be refused: %s", recorder.Body.String())
	}
}

// A rule that named no tool covers the tools nobody has seen yet, which is the
// whole reason it is not compiled into a list of names.
func TestPlatformPolicyCanCoverAnEntireServer(t *testing.T) {
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: "http://127.0.0.1:1/mcp", PolicyDenyAll: true,
	}}, func(map[string]any) {}, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/github",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a_tool_nobody_declared"}}`)))
	if !strings.Contains(recorder.Body.String(), "플랫폼 정책") {
		t.Fatalf("a server-wide denial must cover an unknown tool: %s", recorder.Body.String())
	}
}

// A tool the agent may not call must not be advertised to it, or the model keeps
// planning around something that always fails.
func TestPlatformDeniedToolsAreNotAdvertised(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"},{"name":"delete_branch"}]}}`))
	}))
	defer server.Close()
	// No per-agent policy at all: the platform's is the only restriction, and it
	// still has to filter the list.
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: server.URL, PolicyDenied: []string{"delete_*"},
	}}, func(map[string]any) {}, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/github",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	body := recorder.Body.String()
	if strings.Contains(body, "delete_branch") {
		t.Fatalf("a forbidden tool was advertised: %s", body)
	}
	if !strings.Contains(body, "read_file") {
		t.Fatalf("the permitted tool disappeared: %s", body)
	}
}

// scannerFor builds a gateway whose content scanner is configured for one class.
func scannerFor(class, action string) *scanner {
	return &scanner{settings: dlp.Settings{Enabled: true, Classes: map[string]string{class: action}}}
}

// A tool call never passes through the control plane, so this gateway is the only
// place a customer record on its way into a ticket can be caught.
func TestToolArgumentsAreScannedBeforeTheyLeaveThePod(t *testing.T) {
	var received string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
	defer origin.Close()

	var audited []map[string]any
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}},
		func(entry map[string]any) { audited = append(audited, entry) }, nil, scannerFor("rrn", "redact"))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"summary":"고객 900101-1234568 문의","extra":"keep me"}}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(body)))

	if strings.Contains(received, "900101-1234568") {
		t.Fatalf("the value reached the MCP server: %s", received)
	}
	if !strings.Contains(received, "주민등록번호 삭제됨") {
		t.Fatalf("the redaction marker is missing: %s", received)
	}
	// Everything the gateway does not understand has to survive the rewrite.
	if !strings.Contains(received, "keep me") || !strings.Contains(received, `"id":1`) {
		t.Fatalf("the rewrite lost part of the request: %s", received)
	}
	if len(audited) == 0 || audited[0]["dlp"] == nil {
		t.Fatalf("the finding was not audited: %#v", audited)
	}
	if strings.Contains(fmt.Sprint(audited), "1234568") {
		t.Fatalf("the audit entry discloses the value: %#v", audited)
	}
}

func TestBlockingClassRefusesTheToolCall(t *testing.T) {
	reached := false
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer origin.Close()
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}},
		func(map[string]any) {}, nil, scannerFor("rrn", "block"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"summary":"900101-1234568"}}}`)))
	if reached {
		t.Fatal("a blocked call must not reach the MCP server")
	}
	if !strings.Contains(recorder.Body.String(), "주민등록번호") {
		t.Fatalf("the refusal must name the class: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "1234568") {
		t.Fatalf("the refusal discloses the value: %s", recorder.Body.String())
	}
}

// A tool that returns a customer record hands it straight to the model, and from
// there into a transcript far more people can read.
func TestToolResponsesAreScannedWhenConfigured(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"홍길동 900101-1234568"}]}}`))
	}))
	defer origin.Close()
	inspect := scannerFor("rrn", "redact")
	inspect.settings.ScanResponses = true
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}}, func(map[string]any) {}, nil, inspect)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_issue","arguments":{}}}`)))
	answer := recorder.Body.String()
	if strings.Contains(answer, "900101-1234568") {
		t.Fatalf("the value reached the agent: %s", answer)
	}
	if !strings.Contains(answer, "홍길동") {
		t.Fatalf("the rest of the answer was lost: %s", answer)
	}

	// Responses are not scanned unless asked for: it is the expensive half.
	quiet := scannerFor("rrn", "redact")
	handler = mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}}, func(map[string]any) {}, nil, quiet)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_issue","arguments":{}}}`)))
	if !strings.Contains(recorder.Body.String(), "900101-1234568") {
		t.Fatal("response scanning must be opt-in")
	}
}

// A deployment that has not configured scanning must behave exactly as before.
func TestNoScannerMeansNoChange(t *testing.T) {
	var received string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer origin.Close()
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}}, func(map[string]any) {}, nil, nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"summary":"900101-1234568"}}}`
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(body)))
	if received != body {
		t.Fatalf("the request was altered without a scanner:\n got %s\nwant %s", received, body)
	}
}

func TestLoadScanner(t *testing.T) {
	if got, err := loadScanner(""); got != nil || err != nil {
		t.Fatalf("no configuration means no scanner: %#v %v", got, err)
	}
	if got, err := loadScanner(`{"enabled":false,"classes":{"rrn":"block"}}`); got != nil || err != nil {
		t.Fatalf("a disabled scanner is no scanner: %#v %v", got, err)
	}
	// A configuration the gateway cannot use must fail loudly at startup rather
	// than leave an operator believing traffic is being scanned.
	if _, err := loadScanner(`{"enabled":true,"classes":{"rrn":"quarantine"}}`); err == nil {
		t.Fatal("an invalid action must be refused")
	}
	if _, err := loadScanner(`{`); err == nil {
		t.Fatal("malformed configuration must be refused")
	}
	got, err := loadScanner(`{"enabled":true,"classes":{"rrn":"redact"},"scanResponses":true}`)
	if err != nil || got == nil || !got.settings.ScanResponses {
		t.Fatalf("a valid configuration must load: %#v %v", got, err)
	}
}

// A class set to 기록만 records the finding and changes nothing.
//
// The gateway logged every finding it did not refuse as "redacted", so a site in
// the learning phase — which is what 기록만 is for — read a Pod log claiming its
// tool calls had been rewritten on the way out. They had not: the arguments went
// to the MCP server exactly as the agent wrote them, and the one place that says
// otherwise is the record somebody uses to decide whether to start redacting.
func TestAnAuditOnlyFindingIsNotLoggedAsARedaction(t *testing.T) {
	var received string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
	defer origin.Close()

	var audited []map[string]any
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}},
		func(entry map[string]any) { audited = append(audited, entry) }, nil, scannerFor("rrn", "audit"))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"summary":"고객 900101-1234568 문의"}}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/jira", strings.NewReader(body)))

	// 기록만 means the call goes out untouched, which is the fact the log has to
	// agree with.
	if !strings.Contains(received, "900101-1234568") {
		t.Fatalf("an audited call was rewritten on its way out: %s", received)
	}
	var decision string
	for _, entry := range audited {
		if entry["dlp"] != nil {
			decision = fmt.Sprint(entry["decision"])
		}
	}
	if decision == "" {
		t.Fatalf("the finding was not audited at all: %#v", audited)
	}
	if decision != dlp.OutcomeAudited {
		t.Fatalf("a call nothing was removed from was logged as %q: %#v", decision, audited)
	}
}
