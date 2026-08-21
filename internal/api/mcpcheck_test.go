package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A server registered and never asked is a tool call that fails mid-task. Each
// answer here is named for what to fix, and one of them is not a fault at all.
func TestAnMCPCheckNamesWhatItFound(t *testing.T) {
	handshake := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2026-07-28","serverInfo":{"name":"probe"}}}`
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		verdict string
		says    string
		tools   int
	}{
		{"answers with tools", mcpServerStub(handshake, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search"},{"name":"fetch"}]}}`), "ok", "2개", 2},
		{"answers over SSE", mcpServerStub("event: message\ndata: "+handshake+"\n\n", "event: message\ndata: "+`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"only"}]}}`+"\n\n"), "ok", "1개", 1},
		{"no tools", mcpServerStub(handshake, `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`), "no_tools", "하나도", 0},
		{"not an MCP server", mcpServerStub(`<html>hello</html>`, ""), "not_mcp", "MCP", 0},
		{"key refused", mcpStatus(http.StatusUnauthorized), "unauthorised", "인증", 0},
		{"wrong path", mcpStatus(http.StatusNotFound), "wrong_path", "/mcp", 0},
	} {
		server := httptest.NewServer(tc.handler)
		verdict, detail, tools := askMCPServer(context.Background(), "shared", server.URL)
		server.Close()
		if verdict != tc.verdict {
			t.Errorf("%s → %q, want %q (%s)", tc.name, verdict, tc.verdict, detail)
		}
		if !strings.Contains(detail, tc.says) {
			t.Errorf("%s does not mention %q: %q", tc.name, tc.says, detail)
		}
		if len(tools) != tc.tools {
			t.Errorf("%s returned %d tools, want %d", tc.name, len(tools), tc.tools)
		}
	}
}

// A server that runs inside the runtime's Pod has no address to call from here.
// That is not a broken server, and saying "연결 실패" about it would send somebody
// to fix an endpoint that was never meant to be reachable.
func TestAPodLocalServerIsNotReportedAsBroken(t *testing.T) {
	for _, mode := range []string{"sidecar", "dedicated"} {
		verdict, detail, _ := askMCPServer(context.Background(), mode, "http://127.0.0.1:1/mcp")
		if verdict != "not_checkable" {
			t.Errorf("%s → %q (%s)", mode, verdict, detail)
		}
		if !strings.Contains(detail, "런타임") {
			t.Errorf("%s does not explain why: %q", mode, detail)
		}
	}
	if verdict, _, _ := askMCPServer(context.Background(), "shared", "  "); verdict != "unconfigured" {
		t.Errorf("an empty address → %q", verdict)
	}
}

// Half of these servers answer in server-sent events. Reading only the JSON
// shape would report a working server as one that does not speak the protocol.
func TestAnSSEFrameIsUnwrapped(t *testing.T) {
	raw := []byte("event: message\ndata: {\"result\":{\"tools\":[{\"name\":\"x\"}]}}\n\n")
	var payload struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if err := json.Unmarshal(mcpPayload(raw), &payload); err != nil {
		t.Fatalf("unwrapped frame does not parse: %v", err)
	}
	if len(payload.Result.Tools) != 1 || payload.Result.Tools[0].Name != "x" {
		t.Errorf("payload = %#v", payload)
	}
	plain := []byte(`{"result":{"tools":[]}}`)
	if string(mcpPayload(plain)) != string(plain) {
		t.Error("a plain JSON body should pass through untouched")
	}
}

// mcpServerStub answers the handshake with one body and everything after it with
// another, which is the order a check asks in.
func mcpServerStub(handshake, tools string) http.HandlerFunc {
	seen := false
	return func(w http.ResponseWriter, _ *http.Request) {
		if !seen {
			seen = true
			_, _ = w.Write([]byte(handshake))
			return
		}
		_, _ = w.Write([]byte(tools))
	}
}

func mcpStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}
