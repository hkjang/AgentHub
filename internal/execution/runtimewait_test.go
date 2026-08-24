package execution

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A wrong image tag is one of the commonest ways a deployment breaks, and the
// cluster says so plainly: "런타임 이미지를 가져오지 못했습니다 … ErrImagePull:
// pull access denied". The runtime record carried that sentence while the task
// reported only "Runtime이 준비되기를 기다리다 시간이 초과되었습니다" — so
// somebody reading the task had to go and find the runtime to learn that a tag
// does not exist.
//
// Observed on a real cluster by approving an image that was never loaded.
func TestAWaitThatTimedOutSaysWhatTheClusterSaid(t *testing.T) {
	reason := `런타임 이미지를 가져오지 못했습니다 (qwencode-config-init): Back-off pulling image "agenthub-qwencode:v9.9.9-missing": ErrImagePull`
	message := runtimeWaitTimeout(reason)
	if !strings.Contains(message, "ErrImagePull") || !strings.Contains(message, "v9.9.9-missing") {
		t.Errorf("the cluster's reason was dropped: %q", message)
	}
	if !strings.Contains(message, "시간이 초과") {
		t.Errorf("the message no longer says the wait ended: %q", message)
	}
}

// A cluster that said nothing is not a cluster to invent words for.
func TestAWaitWithNoReasonSaysOnlyThat(t *testing.T) {
	message := runtimeWaitTimeout("   ")
	if !strings.HasSuffix(message, "시간이 초과되었습니다") {
		t.Fatalf("got %q", message)
	}
}

// A Pod's events can run long; the run's line has to stay readable.
func TestALongReasonIsCut(t *testing.T) {
	message := runtimeWaitTimeout(strings.Repeat("길다 ", 200))
	if len(message) > 420 {
		t.Fatalf("the failure line is %d characters", len(message))
	}
	if !strings.Contains(message, "…") {
		t.Error("a cut reason does not say it was cut")
	}
}

// And the wait has to keep the reason it saw. Reading it every tick and
// discarding it is what produced the message this test exists to prevent.
func TestTheWaitKeepsWhatItWasTold(t *testing.T) {
	body, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) waitForRuntime(")
	if at < 0 {
		t.Fatal("the wait is gone; this guard is reading nothing")
	}
	wait := source[at:]
	if end := strings.Index(wait, "\n// releaseRuntime"); end >= 0 {
		wait = wait[:end]
	}
	if !strings.Contains(wait, "reason = trimmed") {
		t.Error("the wait does not keep the reason the cluster gave it")
	}
	if !strings.Contains(wait, "runtimeWaitTimeout(reason)") {
		t.Error("the timeout does not carry the reason")
	}
}

// A wait ends on whichever clock runs out first, and for a long time only one
// of them answered. When the task's own limit was shorter than the three
// minutes this wait allows, the branch below returned Go's own ctx.Err() and
// the reason collected three seconds earlier went nowhere.
//
// Measured on a cluster: an orca runtime whose sidecar tag did not exist sat in
// ImagePullBackOff, the cluster said "pull access denied … repository does not
// exist", and the task reported "Runtime을 확보하지 못했습니다: context deadline
// exceeded".
func TestBothClocksSayWhatTheClusterSaw(t *testing.T) {
	message := runtimeWaitEnded(context.DeadlineExceeded, "런타임 이미지를 가져오지 못했습니다 (ErrImagePull)")
	if !strings.Contains(message, "ErrImagePull") {
		t.Error("a wait the task's clock ended does not carry what the cluster said")
	}
	if strings.Contains(message, "context deadline exceeded") {
		t.Error("the person is shown Go's own words for the clock that ran out")
	}
	cancelled := runtimeWaitEnded(context.Canceled, "런타임 이미지를 가져오지 못했습니다")
	if !strings.Contains(cancelled, "취소") {
		t.Error("a cancelled wait is reported as a timeout")
	}
	if strings.Contains(cancelled, "context canceled") {
		t.Error("the person is shown Go's own words for a cancellation")
	}
	body, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) waitForRuntime(")
	if at < 0 {
		t.Fatal("the wait is gone; this guard is reading nothing")
	}
	wait := source[at:]
	if end := strings.Index(wait, "\n// releaseRuntime"); end >= 0 {
		wait = wait[:end]
	}
	if strings.Contains(wait, "return ctx.Err()") {
		t.Error("the wait still hands back Go's context error, so the cluster's reason is lost")
	}
	if !strings.Contains(wait, "runtimeWaitEnded(ctx.Err(), reason)") {
		t.Error("the task's own clock does not report the reason the cluster gave")
	}
}
