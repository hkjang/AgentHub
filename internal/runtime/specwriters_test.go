package runtime

import (
	"os"
	"strings"
	"testing"
)

// Every write to a runtime's spec has to be able to lose the race once.
//
// The operator writes status to the same object whenever a Pod changes phase,
// and Kubernetes refuses a spec write that carries a version read before that.
// updateRuntimeObject takes the read again and reapplies the change; a write
// that goes straight to Update does not, and hands the conflict to whoever
// pressed the button. Every path that writes a spec must go through the helper —
// this is the guard that says so, because the paths keep multiplying: start,
// stop, restart, and the environment push are four of them already.
func TestEverySpecWriteGoesThroughTheRetry(t *testing.T) {
	body, err := os.ReadFile("kubernetes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	const helper = "func updateRuntimeObject("
	at := strings.Index(source, helper)
	if at < 0 {
		t.Fatal("updateRuntimeObject is gone; this guard is reading nothing")
	}
	end := strings.Index(source[at:], "\n}\n")
	if end < 0 {
		t.Fatal("updateRuntimeObject has no end; this guard is reading nothing")
	}
	inside := source[at : at+end]
	if !strings.Contains(inside, "retry.RetryOnConflict(") {
		t.Error("updateRuntimeObject no longer retries on conflict, so no spec write does")
	}

	found := 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "Resource(runtimeGVR)") || !strings.Contains(trimmed, ".Update(ctx") {
			continue
		}
		found++
		if !strings.Contains(inside, trimmed) {
			t.Errorf("a spec write outside updateRuntimeObject: %s\nit fails outright when the operator writes status first", trimmed)
		}
	}
	if found == 0 {
		t.Fatal("no spec write was found at all; this guard is reading nothing")
	}
}
