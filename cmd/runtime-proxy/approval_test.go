package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// controlPlaneStub answers the two routes the gate calls: create an approval, then
// report its status. decision is what the second call reports.
type controlPlaneStub struct {
	server   *httptest.Server
	requests int32
	polls    int32
	decision atomic.Value // string
	// seen records what the reviewer would have been shown.
	seenTool      atomic.Value // string
	seenArguments atomic.Value // string
}

func newControlPlaneStub(initial string) *controlPlaneStub {
	stub := &controlPlaneStub{}
	stub.decision.Store(initial)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runtime-gateway/tool-approvals", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.requests, 1)
		if r.Header.Get("Authorization") != "Bearer runtime-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			RuntimeID string `json:"runtimeId"`
			Server    string `json:"server"`
			Tool      string `json:"tool"`
			Arguments string `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.seenTool.Store(body.Server + "/" + body.Tool)
		stub.seenArguments.Store(body.Arguments)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"approval-1","status":"pending"}`))
	})
	mux.HandleFunc("/api/v1/runtime-gateway/tool-approvals/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.polls, 1)
		decision, _ := stub.decision.Load().(string)
		_, _ = fmt.Fprintf(w, `{"id":"approval-1","status":%q}`, decision)
	})
	stub.server = httptest.NewServer(mux)
	return stub
}

func (s *controlPlaneStub) close() { s.server.Close() }

func (s *controlPlaneStub) approver() *approver {
	return &approver{
		baseURL: s.server.URL, runtimeID: "runtime-1", token: "runtime-token",
		client: s.server.Client(), wait: 3 * time.Second, poll: 20 * time.Millisecond,
	}
}

// upstreamStub is the MCP server behind the gateway. It records whether it was
// reached at all, which is the question every one of these tests asks.
func upstreamStub(calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
}

func gatedUpstream(url string) mcpUpstream {
	return mcpUpstream{Name: "git", Upstream: url, Mode: "deny", Tools: []string{"force_push"}, ApprovalTools: []string{"delete_branch"}}
}

func callTool(handler http.Handler, tool string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"branch":"main"}}}`, tool)
	request := httptest.NewRequest(http.MethodPost, "/mcp/git", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func rpcErrorMessage(t *testing.T, body string) string {
	t.Helper()
	var decoded struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("response is not JSON-RPC: %s", body)
	}
	return decoded.Error.Message
}

func TestGatedToolWaitsForApprovalThenRuns(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, stub.approver())
	// The decision arrives while the call is being held.
	go func() {
		time.Sleep(60 * time.Millisecond)
		stub.decision.Store("approved")
	}()
	response := callTool(handler, "delete_branch")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "result") {
		t.Fatalf("an approved call did not run: %d %s", response.Code, response.Body.String())
	}
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Fatalf("upstream was called %d times", upstreamCalls)
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatalf("the gate asked %d times", stub.requests)
	}
	// The reviewer has to see what the call would do, not just its name.
	if seen, _ := stub.seenTool.Load().(string); seen != "git/delete_branch" {
		t.Fatalf("the approval named %q", seen)
	}
	if arguments, _ := stub.seenArguments.Load().(string); !strings.Contains(arguments, "main") {
		t.Fatalf("the approval carried no arguments: %q", arguments)
	}
}

// A platform rule that named no tool gates every tool on the server, including
// the ones nobody declared — the same reading as the server-wide denial beside
// it, and the reason it is a flag rather than a list of names.
func TestServerWideGateHoldsAnUndeclaredTool(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("rejected")
	defer stub.close()

	server := mcpUpstream{Name: "git", Upstream: upstream.URL, PolicyGateAll: true}
	handler := mcpGatewayWithApprover([]mcpUpstream{server}, func(map[string]any) {}, stub.approver())
	response := callTool(handler, "a_tool_nobody_declared")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "거절") {
		t.Fatalf("an undeclared tool was not gated: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("a call nobody approved reached the MCP server")
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatalf("the gate asked %d times", stub.requests)
	}
}

func TestRejectedToolNeverReachesTheServer(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("rejected")
	defer stub.close()

	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, stub.approver())
	response := callTool(handler, "delete_branch")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "거절") {
		t.Fatalf("unexpected refusal: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("a rejected call reached the MCP server")
	}
}

func TestApprovalTimeoutBlocksTheCall(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	gate := stub.approver()
	gate.wait = 80 * time.Millisecond
	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, gate)
	response := callTool(handler, "delete_branch")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "대기 시간") {
		t.Fatalf("unexpected refusal: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("a call nobody decided on ran anyway")
	}
}

func TestGateFailsClosedWhenTheControlPlaneIsUnreachable(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("approved")
	gate := stub.approver()
	stub.close() // the control plane is gone before the call is made

	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, gate)
	response := callTool(handler, "delete_branch")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "승인을 요청할 수 없어") {
		t.Fatalf("unexpected refusal: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("the call ran while approval could not be asked for")
	}
}

func TestGatedToolWithNoApproverConfiguredIsRefused(t *testing.T) {
	// A runtime configured to need approval with no way to ask for one must not
	// quietly fall back to running the tool.
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()

	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, nil)
	response := callTool(handler, "delete_branch")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "승인 요청 경로") {
		t.Fatalf("unexpected refusal: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("a gated call ran without a gate")
	}
}

func TestUngatedToolRunsWithoutAsking(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	handler := mcpGatewayWithApprover([]mcpUpstream{gatedUpstream(upstream.URL)}, func(map[string]any) {}, stub.approver())
	response := callTool(handler, "list_branches")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "result") {
		t.Fatalf("an ungated call was blocked: %s", response.Body.String())
	}
	if atomic.LoadInt32(&stub.requests) != 0 {
		t.Fatal("an ungated call asked for approval")
	}
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Fatalf("upstream was called %d times", upstreamCalls)
	}
}

func TestBlockedToolIsRefusedBeforeAnyApproval(t *testing.T) {
	// The allow/deny list decides first: a blocked tool is not worth a person's
	// time, and asking would imply it could be approved.
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("approved")
	defer stub.close()

	gated := gatedUpstream(upstream.URL)
	gated.ApprovalTools = append(gated.ApprovalTools, "force_push")
	handler := mcpGatewayWithApprover([]mcpUpstream{gated}, func(map[string]any) {}, stub.approver())
	response := callTool(handler, "force_push")

	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "차단") {
		t.Fatalf("unexpected refusal: %q", message)
	}
	if atomic.LoadInt32(&stub.requests) != 0 {
		t.Fatal("a blocked tool was sent for approval")
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatal("a blocked tool reached the MCP server")
	}
}

func TestEveryToolIsGatedWhenTheServerRequiresApproval(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("approved")
	defer stub.close()

	server := mcpUpstream{Name: "git", Upstream: upstream.URL, ApprovalRequired: true}
	handler := mcpGatewayWithApprover([]mcpUpstream{server}, func(map[string]any) {}, stub.approver())
	if response := callTool(handler, "anything_at_all"); response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatal("an approval-required server let a call through without asking")
	}
}

func TestTrimArgumentsIsBoundedAndReadable(t *testing.T) {
	rendered := trimArguments([]byte(`{"branch":"main","force":true}`))
	if !strings.Contains(rendered, "\"branch\"") || !strings.Contains(rendered, "\n") {
		t.Fatalf("arguments are not readable: %q", rendered)
	}
	long := trimArguments([]byte(`{"file":"` + strings.Repeat("x", maxArgumentChars*2) + `"}`))
	if len(long) > maxArgumentChars+40 {
		t.Fatalf("arguments are unbounded: %d chars", len(long))
	}
	if !strings.Contains(long, "생략") {
		t.Fatal("a trimmed argument list does not say it was trimmed")
	}
	if trimArguments(nil) != "" {
		t.Fatal("a call with no arguments should render as nothing")
	}
}
