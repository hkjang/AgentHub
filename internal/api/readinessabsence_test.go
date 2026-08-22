package api

import (
	"os"
	"strings"
	"testing"
)

// A list of dependencies cannot report the one that is not there.
//
// The readiness screen asked each configured model endpoint and each configured
// MCP server how it was, which answers everything except "there aren't any" — and
// that is the state a new deployment is in. With no model endpoint every prose,
// flow and investigation agent fails the moment it calls a model, and this screen
// reported no problems.
//
// The worker is the same shape and worse. A control plane with no worker looks
// healthy from every angle: the console answers, agents save, tasks queue, and
// nothing ever claims one. It is the most common way a first deployment stalls.
func TestReadinessReportsWhatIsMissingAndNotOnlyWhatIsBroken(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) readiness(")
	if at < 0 {
		t.Fatal("the readiness handler is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\nfunc "); end >= 0 {
		handler = handler[:end]
	}
	// The row and its verdict together. Checking for the verdict alone passed on
	// the cluster's own "unconfigured" row, which is a guard that cannot fail —
	// exactly what it is here to catch in the code it guards.
	for _, absence := range []struct{ what, evidence string }{
		{"no model endpoint is configured", `Area: "모델", Name: "모델 엔드포인트", Verdict: "unconfigured"`},
		{"no worker is running", `Area: "실행", Name: "워커", Verdict: "none"`},
		{"execution is paused", `Area: "실행", Name: "워커", Verdict: "paused"`},
		{"the cluster is not configured at all", `Area: "Kubernetes", Name: "클러스터", Verdict: "unconfigured"`},
	} {
		if !strings.Contains(handler, absence.evidence) {
			t.Errorf("readiness does not report that %s; the screen says nothing is wrong while nothing can run", absence.what)
		}
	}
	if !strings.Contains(handler, "LiveWorkers(") {
		t.Error("readiness never asks whether a worker is alive")
	}
	// The verdicts it produces have to be ones the summary counts as problems,
	// otherwise the row appears and the count beside it still reads zero.
	for _, verdict := range []string{"unconfigured", "none", "unknown"} {
		if readinessOK[verdict] {
			t.Errorf("%q is treated as a passing verdict; a deployment that cannot run anything would report no problems", verdict)
		}
	}
	// Paused is a decision rather than a fault, but it still has to be visible: it
	// is the answer to "why is nothing running".
	if readinessOK["paused"] {
		t.Error("a paused execution plane is counted as fine; it is the answer to why nothing is running")
	}
}
