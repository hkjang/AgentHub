package api

import "testing"

// A dependency that could not be checked is not a dependency that works. The
// only two words that mean "nothing to do here" are the two that were actually
// confirmed; everything else — unreachable, unauthorised, not_checkable, unknown
// — belongs on the list somebody reads.
func TestOnlyAConfirmedAnswerCountsAsReady(t *testing.T) {
	for _, verdict := range []string{"ok", "enforced"} {
		if !readinessOK[verdict] {
			t.Errorf("%q should count as ready", verdict)
		}
	}
	for _, verdict := range []string{
		"unreachable", "unauthorised", "wrong_path", "not_mcp", "no_tools", "model_missing",
		"unconfigured", "incomplete", "issuer_mismatch", "client_rejected", "not_checkable", "unknown", "",
	} {
		if readinessOK[verdict] {
			t.Errorf("%q must not count as ready", verdict)
		}
	}
}

// Nothing runs without the cluster and nobody logs in without the provider, so
// the list is ordered the way somebody would fix it rather than alphabetically.
func TestReadinessIsOrderedByWhatToFixFirst(t *testing.T) {
	if readinessRank("Kubernetes") >= readinessRank("인증") {
		t.Error("the cluster has to come before the identity provider")
	}
	if readinessRank("인증") >= readinessRank("모델") {
		t.Error("logging in has to come before what people log in to use")
	}
	if readinessRank("MCP") <= readinessRank("모델") {
		t.Error("tools come after the models that call them")
	}
}
