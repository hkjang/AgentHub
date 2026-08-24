package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A Pod that cannot pull its image retries for ever, which is right — a registry
// comes back. But the runtime then sits half-started with the reason on its own
// row and nobody looking at that row: one was left in ImagePullBackOff for
// sixty-five minutes during this work and noticed only by hand, on a screen
// whose whole job is to answer "what is broken now".
func TestReadinessAsksAboutRuntimesThatNeverArrived(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "RuntimesStuckStarting(") {
		t.Fatal("readiness never asks whether a runtime is still trying to start")
	}
	at := strings.Index(source, "RuntimesStuckStarting(")
	section := source[at:]
	if end := strings.Index(section, "\n\t// Single sign-on"); end >= 0 {
		section = section[:end]
	}
	// The reason the cluster gave is the whole value of asking.
	if !strings.Contains(section, "runtime.FailureReason") {
		t.Error("a stuck runtime is reported without what the cluster said about it")
	}
	// And it names where to go, like every other row here.
	if !strings.Contains(section, `Fix: "/agents/"`) {
		t.Error("a stuck runtime does not say where it is looked at")
	}
	// Long text from a Pod's events must not swamp the row.
	if !strings.Contains(section, "reason[:200]") {
		t.Error("the reason is carried without a bound")
	}
}

// A runtime starting normally is not trouble, and a screen that says so about
// every cold start is a screen people stop reading.
func TestTheWindowLeavesRoomForAnOrdinaryStart(t *testing.T) {
	if runtimeStuckAfter < 5*time.Minute {
		t.Errorf("a runtime is called stuck after %v, which an image pull can take", runtimeStuckAfter)
	}
	if runtimeStuckAfter > 30*time.Minute {
		t.Errorf("a runtime is only called stuck after %v, by which time somebody has already asked", runtimeStuckAfter)
	}
}

func TestAgeIsSaidInTheWordsSomebodyWouldUse(t *testing.T) {
	for elapsed, want := range map[time.Duration]string{
		12 * time.Minute:   "12분",
		90 * time.Minute:   "1시간",
		30 * time.Hour:     "1일",
		3 * 24 * time.Hour: "3일",
	} {
		if got := humanSince(time.Now().Add(-elapsed)); got != want {
			t.Errorf("%v reads as %q, want %q", elapsed, got, want)
		}
	}
}

// A crash loop wears a healthy word. The runtime's status says running, because
// Kubernetes keeps starting it again, and the only sign is a count on the
// agent's page that nobody is asked to judge: forty restarts and one restart
// read the same there.
func TestReadinessAsksAboutRuntimesThatKeepDying(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "RuntimesRestarting(") {
		t.Fatal("readiness never asks whether a runtime keeps restarting")
	}
	at := strings.Index(source, "RuntimesRestarting(")
	section := source[at:]
	if end := strings.Index(section, "\n\t// Single sign-on"); end >= 0 {
		section = section[:end]
	}
	// The count is the finding: "keeps restarting" without it is a feeling.
	if !strings.Contains(section, "runtime.Restarts") {
		t.Error("the report does not say how many times")
	}
	if !strings.Contains(section, "runtime.FailureReason") {
		t.Error("a crash loop is reported without what the cluster said about it")
	}
	if !strings.Contains(section, `Fix: "/agents/"`) {
		t.Error("a crash-looping runtime does not say where it is looked at")
	}
}

// One restart is a node being drained, not news; a threshold high enough to
// mean nothing is not worth having either.
func TestTheRestartThresholdMeansSomething(t *testing.T) {
	if runtimeRestartAlarm < 3 {
		t.Errorf("a runtime is called a crash loop after %d restarts, which happens on a drained node", runtimeRestartAlarm)
	}
	if runtimeRestartAlarm > 20 {
		t.Errorf("a crash loop is only reported after %d restarts, by which time it has been failing for an hour", runtimeRestartAlarm)
	}
}
