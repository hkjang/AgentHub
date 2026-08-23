package store

import "testing"

// A gated call has to name what is being decided about.
//
// Approval was built for the egress gateway inside a Pod, so the runtime was
// always there to name. Work handed to a registered agent server has no Pod
// here, and the first version left the column empty — the run failed on a
// not-null violation instead of asking anybody, which is the shape of bug this
// backend keeps producing: a field that was never empty before.
func TestAGatedCallAlwaysNamesWhatIsBeingDecided(t *testing.T) {
	fromPod := ToolApproval{RuntimeID: "runtime-1", AgentID: "agent-1"}
	if got := approvalResource(fromPod); got != "runtime-1" {
		t.Errorf("a call from a Pod named %q instead of its runtime", got)
	}
	fromServer := ToolApproval{AgentID: "agent-1"}
	if got := approvalResource(fromServer); got != "agent-1" {
		t.Errorf("a call from an agent server named %q; an approval attached to nothing cannot be reviewed", got)
	}
}
