package execution

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// Reaching the time limit and failing to start are different repairs, and every
// backend that runs a command in a Pod reported them with the same sentence:
// "Runtime에서 …을 실행하지 못했습니다: context deadline exceeded". That sends
// somebody to look at Kubernetes when what they need is a bigger number in their
// own Goal.
//
// Observed on a real cluster: a review against a gateway that never answered
// ended exactly that way, at 58 seconds of a 60-second limit.
func TestATimeLimitIsReportedAsATimeLimit(t *testing.T) {
	message := runtimeExecFailure("리뷰", context.DeadlineExceeded, store.AgentGoal{MaxDurationSeconds: 60})
	if strings.Contains(message, "실행하지 못했습니다") {
		t.Errorf("a run that hit its limit reads as a runtime that would not start: %q", message)
	}
	if !strings.Contains(message, "60") {
		t.Errorf("the limit that stopped it is not named: %q", message)
	}
	if strings.Contains(message, "context deadline exceeded") {
		t.Errorf("the Go error is shown to a person: %q", message)
	}
}

// A Goal with no limit still says what happened, without inventing a number.
func TestALimitFromElsewhereIsNotAttributedToTheGoal(t *testing.T) {
	message := runtimeExecFailure("리뷰", context.DeadlineExceeded, store.AgentGoal{})
	if strings.Contains(message, "0초") {
		t.Errorf("a limit nobody set was named: %q", message)
	}
	if !strings.Contains(message, "제한 시간") {
		t.Errorf("the message does not say a limit stopped it: %q", message)
	}
}

// Everything else keeps the words it had: a runtime that could not run the
// command is exactly what it says.
func TestAnOrdinaryFailureIsUnchanged(t *testing.T) {
	message := runtimeExecFailure("에이전트", errors.New("pods \"x\" not found"), store.AgentGoal{MaxDurationSeconds: 60})
	if !strings.Contains(message, "실행하지 못했습니다") || !strings.Contains(message, "not found") {
		t.Fatalf("got %q", message)
	}
}

func TestACancelledRunSaysSo(t *testing.T) {
	if message := runtimeExecFailure("리뷰", context.Canceled, store.AgentGoal{}); !strings.Contains(message, "취소") {
		t.Fatalf("got %q", message)
	}
}

// And every backend that runs a command in a Pod has to use it. One of three
// saying it plainly is the shape this platform keeps removing.
func TestEveryPodBackendExplainsItTheSameWay(t *testing.T) {
	for _, file := range []string{"cli.go", "investigate.go", "review.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if strings.Contains(source, "실행하지 못했습니다: \" + execErr.Error()") {
			t.Errorf("%s still reports every ending as a runtime that would not start", file)
		}
		if !strings.Contains(source, "runtimeExecFailure(") {
			t.Errorf("%s does not explain an exec failure the way the others do", file)
		}
	}
}

// Korean picks 이/가 and 을/를 by whether the word ends in a consonant. The first
// version of this message said "리뷰이 최대 실행 시간에 도달해" — a sentence no
// Korean speaker would write, in the place somebody reads when something has
// gone wrong.
// The particle itself is checked where the rule lives — internal/korean. What
// belongs here is that this message asks for one rather than writing it in.
func TestTheSentenceIsKorean(t *testing.T) {
	message := runtimeExecFailure("리뷰", context.DeadlineExceeded, store.AgentGoal{MaxDurationSeconds: 60})
	if strings.HasPrefix(message, "리뷰이") {
		t.Errorf("the message reads as broken Korean: %q", message)
	}
	if !strings.HasPrefix(message, "리뷰가") {
		t.Errorf("got %q", message)
	}
}
