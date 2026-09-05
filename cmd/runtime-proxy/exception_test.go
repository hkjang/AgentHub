package main

import "testing"

// The gateway is where a tool call is actually refused, so it is where the
// exception has to be honoured. "Nobody may use this server, except read_file"
// is one deny and one allow; a gateway that only understands the deny enforces
// half of a rule and refuses a call the policy permits.
func TestTheGatewayHonoursAPolicyException(t *testing.T) {
	upstream := mcpUpstream{Name: "github", PolicyDenyAll: true, PolicyAllowed: []string{"read_file", "list_*"}}
	for tool, want := range map[string]bool{"read_file": true, "list_issues": true, "delete_repo": false, "a_new_tool": false} {
		if got := upstream.permits(tool); got != want {
			t.Errorf("permits(%s) = %v, want %v", tool, got, want)
		}
		// And the two refusals stay distinguishable: what the agent is told, and
		// what the audit trail records, is "the platform forbids this" only for
		// the tools it actually forbids.
		if got := upstream.deniedByPlatform(tool); got != !want {
			t.Errorf("deniedByPlatform(%s) = %v, want %v", tool, got, !want)
		}
	}

	// The same for a gate: an exception above it does not wait for a person.
	gated := mcpUpstream{Name: "github", PolicyGateAll: true, PolicyAllowed: []string{"read_file"}}
	if gated.needsApproval("read_file") {
		t.Error("the exception still waits for approval")
	}
	if !gated.needsApproval("delete_repo") {
		t.Error("the gate stopped applying to everything else")
	}
}

// The exception is the platform's statement about the platform's own rules. The
// agent's list and the catalogue entry belong to other people, and an exception
// written in the central policy does not widen either of them — otherwise a
// platform rule would hand an agent a tool its owner never gave it.
func TestAPolicyExceptionDoesNotWidenTheOtherControls(t *testing.T) {
	owned := mcpUpstream{Name: "github", Mode: "allow", Tools: []string{"list_issues"}, PolicyDenyAll: true, PolicyAllowed: []string{"read_file"}}
	if owned.permits("read_file") {
		t.Error("a platform exception granted a tool the agent's own allow list does not name")
	}
	both := mcpUpstream{Name: "github", Mode: "allow", Tools: []string{"read_file", "list_issues"}, PolicyDenyAll: true, PolicyAllowed: []string{"read_file"}}
	if !both.permits("read_file") {
		t.Error("both ends named the tool and it is still refused")
	}
	if both.permits("list_issues") {
		t.Error("the platform's deny stopped applying to what its exception did not name")
	}

	catalogued := mcpUpstream{Name: "github", ApprovalRequired: true, PolicyGateAll: true, PolicyAllowed: []string{"read_file"}}
	if !catalogued.needsApproval("read_file") {
		t.Error("a platform exception cancelled the catalogue's own approval requirement")
	}
	listed := mcpUpstream{Name: "github", ApprovalTools: []string{"read_file"}, PolicyGateAll: true, PolicyAllowed: []string{"read_file"}}
	if !listed.needsApproval("read_file") {
		t.Error("a platform exception cancelled the approval the agent's owner asked for")
	}
}
