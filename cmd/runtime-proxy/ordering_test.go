package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/policy"
)

// The order the rules were written in is the policy, and it now travels with
// them. A gate written above a server-wide deny — "a delete needs a person, and
// nothing else on this server is allowed at all" — has to wait for a reviewer
// here, because here is the only place a tool call is actually refused.
//
// Given the restrictions without their order, the gateway read its own: denial
// first. The call was refused outright while the console, the simulator and the
// audit trail all said it was waiting for someone.
func TestAGateWrittenAboveADenyWaitsInsteadOfBeingRefused(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()
	stub := newControlPlaneStub("pending")
	defer stub.close()

	binding := mcpUpstream{Name: "git", Upstream: upstream.URL, PolicyRules: []policy.CompiledRule{
		{Effect: policy.RequireApproval, Tools: []string{"delete_*"}},
		{Effect: policy.Deny},
	}}
	handler := mcpGatewayWithApprover([]mcpUpstream{binding}, func(map[string]any) {}, stub.approver())
	go func() {
		time.Sleep(60 * time.Millisecond)
		stub.decision.Store("approved")
	}()
	response := callTool(handler, "delete_branch")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "result") {
		t.Fatalf("the approved call did not run: %d %s", response.Code, response.Body.String())
	}
	if atomic.LoadInt32(&stub.requests) != 1 {
		t.Fatalf("nobody was asked: the call was refused by the rule written below the gate")
	}
	// And the deny below it still decides everything the gate did not name.
	refused := callTool(handler, "read_file")
	if message := rpcErrorMessage(t, refused.Body.String()); !strings.Contains(message, "플랫폼 정책") {
		t.Fatalf("the deny below the gate stopped applying: %q", message)
	}
}

// The document's default decides every tool no rule named, and a closed platform
// is written by setting it once. It never reached the Pod at all: task.create
// and runtime.start honoured it because the API evaluates the document, and
// every tool call inside the Pod went through.
func TestTheDocumentDefaultDecidesAToolNoRuleNamed(t *testing.T) {
	var upstreamCalls int32
	upstream := upstreamStub(&upstreamCalls)
	defer upstream.Close()

	binding := mcpUpstream{Name: "git", Upstream: upstream.URL, PolicyDefault: policy.Deny,
		PolicyRules: []policy.CompiledRule{{Effect: policy.Allow, Tools: []string{"read_*"}}}}
	handler := mcpGateway([]mcpUpstream{binding}, func(map[string]any) {})

	refused := callTool(handler, "delete_branch")
	if message := rpcErrorMessage(t, refused.Body.String()); !strings.Contains(message, "플랫폼 정책") {
		t.Fatalf("a tool no rule named was allowed under a default of deny: %q", message)
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatalf("the call reached the server %d times", upstreamCalls)
	}
	if response := callTool(handler, "read_file"); response.Code != http.StatusOK {
		t.Fatalf("the rule above the default stopped deciding: %d %s", response.Code, response.Body.String())
	}
}

// A Pod whose control plane predates the ordered rules receives the summary and
// nothing else, and it has to keep enforcing exactly what it always did.
func TestTheSummaryAloneStillDecides(t *testing.T) {
	upstream := mcpUpstream{Name: "github", PolicyDenyAll: true, PolicyAllowed: []string{"read_file"}}
	if !upstream.permits("read_file") || upstream.permits("delete_repo") {
		t.Fatal("the summary alone no longer decides")
	}
	if !upstream.restricts() {
		t.Fatal("a server-wide deny must still filter the advertised tool list")
	}
	// And a deny that only exists in the ordered rules filters that list too.
	ordered := mcpUpstream{Name: "github", PolicyRules: []policy.CompiledRule{{Effect: policy.Deny, Tools: []string{"delete_*"}}}}
	if !ordered.restricts() {
		t.Fatal("a tool the agent may not call would still be advertised to it")
	}
}
