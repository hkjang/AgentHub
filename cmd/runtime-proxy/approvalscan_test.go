package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gatedScanTarget is the MCP server behind a tool that needs a person's decision,
// recording the body it was handed so a test can ask what actually left the Pod.
func gatedScanTarget(received *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
}

// callGatedTool calls the approval-gated tool with arguments of the caller's
// choosing, which is what separates these tests from the ones that only ask
// whether the gate fired.
func callGatedTool(handler http.Handler, arguments string) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_branch","arguments":` + arguments + `}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp/git", strings.NewReader(body)))
	return response
}

// Asking a person is itself a way out of the Pod.
//
// The gate POSTs the call's arguments to the control plane, which stores them on
// the approval row and shows them to whoever decides. The scanner ran after it,
// so a tool that was both gated by name and carrying a resident registration
// number had that number copied into the control plane's database — permanently,
// and in front of every reviewer — before the scanner ever looked at it. The
// value then never reached the MCP server, and the trail recorded a redaction:
// the one boundary the setting exists to hold was held, and the value had
// already left by the door beside it.
func TestAGatedCallIsScannedBeforeAReviewerIsAsked(t *testing.T) {
	var received atomic.Value
	origin := gatedScanTarget(&received)
	defer origin.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	handler := mcpGatewayWith([]mcpUpstream{gatedUpstream(origin.URL)},
		func(map[string]any) {}, stub.approver(), scannerFor("rrn", "redact"))
	go func() {
		time.Sleep(40 * time.Millisecond)
		stub.decision.Store("approved")
	}()
	response := callGatedTool(handler, `{"reason":"고객 900101-1234568 요청","branch":"main"}`)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "result") {
		t.Fatalf("the approved call did not run: %d %s", response.Code, response.Body.String())
	}
	shown, _ := stub.seenArguments.Load().(string)
	if strings.Contains(shown, "900101-1234568") {
		t.Fatalf("the approval request carried the value to the control plane: %s", shown)
	}
	if !strings.Contains(shown, "주민등록번호 삭제됨") {
		t.Fatalf("the reviewer was shown arguments the scanner never touched: %s", shown)
	}
	// The reviewer still has to be able to decide, so everything else survives.
	if !strings.Contains(shown, "main") {
		t.Fatalf("the rewrite left the reviewer nothing to decide on: %s", shown)
	}
	sent, _ := received.Load().(string)
	if strings.Contains(sent, "900101-1234568") || !strings.Contains(sent, "주민등록번호 삭제됨") {
		t.Fatalf("the MCP server was handed the wrong body: %s", sent)
	}
}

// The old order had one thing going for it: a call the scanner refuses never
// became an approval request, so nobody was asked to decide about a call that
// could not happen either way. Scanning first has to keep that.
func TestACallTheScannerRefusesNeverBecomesAnApproval(t *testing.T) {
	var received atomic.Value
	origin := gatedScanTarget(&received)
	defer origin.Close()
	stub := newControlPlaneStub("approved")
	defer stub.close()

	handler := mcpGatewayWith([]mcpUpstream{gatedUpstream(origin.URL)},
		func(map[string]any) {}, stub.approver(), scannerFor("rrn", "block"))
	response := callGatedTool(handler, `{"reason":"고객 900101-1234568 요청","branch":"main"}`)

	if atomic.LoadInt32(&stub.requests) != 0 {
		t.Fatalf("a refused call created %d approval requests", stub.requests)
	}
	if _, asked := stub.seenArguments.Load().(string); asked {
		t.Fatal("a refused call still handed its arguments to the control plane")
	}
	if sent, reached := received.Load().(string); reached {
		t.Fatalf("a refused call reached the MCP server: %s", sent)
	}
	message := rpcErrorMessage(t, response.Body.String())
	if !strings.Contains(message, "주민등록번호") {
		t.Fatalf("the refusal must name the class: %q", message)
	}
	if strings.Contains(message, "1234568") {
		t.Fatalf("the refusal discloses the value: %q", message)
	}
}

// A gated call the scanner found nothing in is the ordinary case, and the
// reviewer must still see exactly what the agent wrote. Redaction that fires on
// clean text would be the same failure from the other side: the person deciding
// would be looking at something the agent never asked for.
func TestACleanGatedCallReachesTheReviewerUnchanged(t *testing.T) {
	var received atomic.Value
	origin := gatedScanTarget(&received)
	defer origin.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	handler := mcpGatewayWith([]mcpUpstream{gatedUpstream(origin.URL)},
		func(map[string]any) {}, stub.approver(), scannerFor("rrn", "block"))
	go func() {
		time.Sleep(40 * time.Millisecond)
		stub.decision.Store("approved")
	}()
	response := callGatedTool(handler, `{"reason":"릴리즈 후 정리","branch":"main"}`)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "result") {
		t.Fatalf("a clean gated call did not run: %d %s", response.Code, response.Body.String())
	}
	shown, _ := stub.seenArguments.Load().(string)
	if !strings.Contains(shown, "릴리즈 후 정리") || !strings.Contains(shown, "main") {
		t.Fatalf("the reviewer was shown something the agent did not write: %s", shown)
	}
	if strings.Contains(shown, "삭제됨") {
		t.Fatalf("clean arguments were redacted on their way to the reviewer: %s", shown)
	}
}
