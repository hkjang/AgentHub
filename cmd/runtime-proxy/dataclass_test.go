package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hkjang/AgentHub/internal/policy"
)

// A rule about a data class is the one kind of rule only this process can answer.
// A tool call never passes through the control plane, so what the call carries is
// known here — after the scan — and nowhere else.
//
// It used to be resolved away when the runtime was provisioned, against a request
// that had been scanned by nobody, which no such rule matches. So the rule reached
// no Pod, the class's own global action decided instead, and a deployment that had
// written "no 주민등록번호 through this server" while leaving the class on 기록만
// sent them and kept a finding.
func dataClassUpstream(url, effect string, classes ...string) mcpUpstream {
	return mcpUpstream{Name: "jira", Upstream: url,
		PolicyRules: []policy.CompiledRule{{Effect: effect, DataClasses: classes}}}
}

func callJira(handler http.Handler, arguments string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":%s}}`, arguments)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp/jira", strings.NewReader(body)))
	return recorder
}

func TestAPolicyRuleAboutADataClassDecidesInThePod(t *testing.T) {
	var calls int32
	origin := upstreamStub(&calls)
	defer origin.Close()

	var audited []map[string]any
	// 기록만 for the class itself: the scanner would have let this through
	// redacted, and the rule is what refuses it.
	handler := mcpGatewayWith([]mcpUpstream{dataClassUpstream(origin.URL, policy.Deny, "rrn")},
		func(entry map[string]any) { audited = append(audited, entry) }, nil, scannerFor("rrn", "redact"))

	response := callJira(handler, `{"summary":"고객 900101-1234568 문의"}`)
	if calls != 0 {
		t.Fatal("a call the policy refuses must not reach the MCP server")
	}
	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "주민등록번호") {
		t.Fatalf("the refusal must name the class: %q", message)
	}
	if strings.Contains(response.Body.String(), "1234568") {
		t.Fatalf("the refusal discloses the value: %s", response.Body.String())
	}
	if len(audited) == 0 || audited[0]["decision"] != "denied" || audited[0]["policyEffect"] != policy.Deny {
		t.Fatalf("the refusal is not in the audit trail as the policy's: %#v", audited)
	}

	// The rule is about what the call carries, not about the tool. The same tool
	// with ordinary arguments is not what the operator wrote it about.
	if response := callJira(handler, `{"summary":"프린터가 고장났습니다"}`); calls != 1 {
		t.Fatalf("an ordinary call was refused: %s", response.Body.String())
	}
}

// And the same rule asking for a person instead. The gateway is the one place a
// call can be held open while somebody decides, so a gate is a gate here too.
func TestADataClassGateWaitsForAPerson(t *testing.T) {
	var calls int32
	origin := upstreamStub(&calls)
	defer origin.Close()
	stub := newControlPlaneStub("approved")
	defer stub.close()

	handler := mcpGatewayWith([]mcpUpstream{dataClassUpstream(origin.URL, policy.RequireApproval, "rrn")},
		func(map[string]any) {}, stub.approver(), scannerFor("rrn", "redact"))

	if response := callJira(handler, `{"summary":"고객 900101-1234568 문의"}`); calls != 1 {
		t.Fatalf("an approved call did not run: %s", response.Body.String())
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatalf("the reviewer was asked %d times, want once", stub.requests)
	}
	// The reviewer sees the call, and never the value in it.
	if seen, _ := stub.seenArguments.Load().(string); strings.Contains(seen, "900101-1234568") {
		t.Fatalf("the approval request discloses the value: %s", seen)
	}
	if response := callJira(handler, `{"summary":"프린터가 고장났습니다"}`); calls != 2 {
		t.Fatalf("an ordinary call was held for a reviewer: %s", response.Body.String())
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatalf("a call carrying nothing was sent to a reviewer: %d requests", stub.requests)
	}
}

// A tool the policy already gates by name is not sent to the same person twice
// because the scan found something. The scan is a new fact about the call, not a
// new decision about the tool.
func TestAGatedToolIsNotSentToTheReviewerTwice(t *testing.T) {
	var calls int32
	origin := upstreamStub(&calls)
	defer origin.Close()
	stub := newControlPlaneStub("approved")
	defer stub.close()

	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL,
		PolicyRules: []policy.CompiledRule{{Effect: policy.RequireApproval, Tools: []string{"create_issue"}}}}},
		func(map[string]any) {}, stub.approver(), scannerFor("rrn", "redact"))

	if response := callJira(handler, `{"summary":"고객 900101-1234568 문의"}`); calls != 1 {
		t.Fatalf("the approved call did not run: %s", response.Body.String())
	}
	if got := atomic.LoadInt32(&stub.requests); got != 1 {
		t.Fatalf("the reviewer was asked %d times for one call", got)
	}
}

// An answer cannot be sent to a reviewer — it has already been read — so a rule
// that would have asked for one refuses it, which is what the model boundary does
// with the same rule on a completion.
func TestADataClassRuleDecidesTheAnswerToo(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"홍길동 900101-1234568"}]}}`))
	}))
	defer origin.Close()

	for _, effect := range []string{policy.Deny, policy.RequireApproval} {
		inspect := scannerFor("rrn", "redact")
		inspect.settings.ScanResponses = true
		handler := mcpGatewayWith([]mcpUpstream{dataClassUpstream(origin.URL, effect, "rrn")},
			func(map[string]any) {}, nil, inspect)

		response := callJira(handler, `{"summary":"조회"}`)
		if strings.Contains(response.Body.String(), "홍길동") {
			t.Fatalf("%s: the answer reached the agent: %s", effect, response.Body.String())
		}
		if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "주민등록번호") {
			t.Fatalf("%s: the refusal must name the class: %q", effect, message)
		}
	}
}

// The tool list is not filtered on account of such a rule. The tool stays callable
// with anything else, and hiding it would tell the model something the policy does
// not say.
func TestADataClassRuleDoesNotHideTheTool(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"create_issue"},{"name":"read_issue"}]}}`))
	}))
	defer origin.Close()

	upstream := dataClassUpstream(origin.URL, policy.Deny, "rrn")
	if upstream.restricts() {
		t.Fatal("a rule about what a call carries does not restrict which tools exist")
	}
	handler := mcpGatewayWith([]mcpUpstream{upstream}, func(map[string]any) {}, nil, scannerFor("rrn", "redact"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp/jira", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	for _, tool := range []string{"create_issue", "read_issue"} {
		if !strings.Contains(recorder.Body.String(), tool) {
			t.Fatalf("%s disappeared from the tool list: %s", tool, recorder.Body.String())
		}
	}
}

// A gateway with no scanner configured has nothing to answer such a rule with, and
// says so by leaving the call alone rather than by refusing everything.
func TestWithoutAScannerADataClassRuleDecidesNothing(t *testing.T) {
	var calls int32
	origin := upstreamStub(&calls)
	defer origin.Close()
	handler := mcpGatewayWith([]mcpUpstream{dataClassUpstream(origin.URL, policy.Deny, "rrn")},
		func(map[string]any) {}, nil, nil)
	if response := callJira(handler, `{"summary":"고객 900101-1234568 문의"}`); calls != 1 {
		t.Fatalf("an unscanned call was refused by a rule nothing could answer: %s", response.Body.String())
	}
}

// The refusal a reviewer never got to make still reaches the agent as a refusal
// rather than as a call that quietly went out.
func TestADataClassGateWithNowhereToAskRefuses(t *testing.T) {
	var calls int32
	origin := upstreamStub(&calls)
	defer origin.Close()
	handler := mcpGatewayWith([]mcpUpstream{dataClassUpstream(origin.URL, policy.RequireApproval, "rrn")},
		func(map[string]any) {}, nil, scannerFor("rrn", "redact"))

	response := callJira(handler, `{"summary":"고객 900101-1234568 문의"}`)
	if calls != 0 {
		t.Fatal("a call that needs a decision nobody can make must not go out")
	}
	if message := rpcErrorMessage(t, response.Body.String()); !strings.Contains(message, "승인") {
		t.Fatalf("the refusal must say a decision was needed: %q", message)
	}
}
