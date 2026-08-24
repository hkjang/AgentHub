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
