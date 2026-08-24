package execution

import (
	"strings"
	"testing"
)

// A container that was killed reports 128 plus the signal, and both endings
// arrived as "에이전트 출력을 읽지 못했습니다(종료 코드 137)" — a sentence that
// sends somebody to look at the agent's output format when the truth is the Pod
// ran out of memory or was deleted underneath it.
//
// Observed by deleting a runtime's Pod while its task was running.
func TestAKilledContainerSaysItWasKilled(t *testing.T) {
	// 137 literally: the number a killed container reports, not our name for it.
	err := cliFailure(137, nil, "", nil)
	if err == nil {
		t.Fatal("a killed agent produced no error")
	}
	if strings.Contains(err.Error(), "출력을 읽지 못했습니다") {
		t.Errorf("a killed container reads as unparseable output: %v", err)
	}
	for _, want := range []string{"137", "OOMKilled", "Memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

// SIGTERM is a different repair from a memory limit: somebody or something asked
// the Pod to go.
func TestATerminatedContainerSaysSo(t *testing.T) {
	err := cliFailure(143, nil, "", nil)
	if err == nil {
		t.Fatal("a terminated agent produced no error")
	}
	// Not the generic "실행이 실패했습니다(종료 코드 143)", which says nothing
	// about what to look at.
	if !strings.Contains(err.Error(), "종료 요청") || !strings.Contains(err.Error(), "Pod") {
		t.Errorf("a termination is not explained: %v", err)
	}
	if strings.Contains(err.Error(), "OOMKilled") {
		t.Errorf("a termination was blamed on memory: %v", err)
	}
}

// The endings that were already explained keep their words.
func TestTheKnownGuardrailsAreUnchanged(t *testing.T) {
	if err := cliFailure(cliExitTurnLimit, nil, "", nil); err == nil || !strings.Contains(err.Error(), "최대 단계 수") {
		t.Errorf("turn limit: %v", err)
	}
	if err := cliFailure(cliExitBudget, nil, "", nil); err == nil || !strings.Contains(err.Error(), "실행 예산") {
		t.Errorf("budget: %v", err)
	}
}

// Every backend that runs a command in a Pod meets the same two endings, and one
// of three explaining them is the shape this platform keeps removing.
func TestEveryPodBackendExplainsAKilledContainer(t *testing.T) {
	// The review engine printing nothing because it was stopped is not the same
	// as printing something unreadable.
	_, err := parseReview("", "", 137)
	if err == nil || !strings.Contains(err.Error(), "137") || !strings.Contains(err.Error(), "OOMKilled") {
		t.Errorf("review: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "읽지 못했습니다") {
		t.Errorf("review reads a killed container as unreadable output: %v", err)
	}

	investigateErr := investigationFailure(143, "")
	if investigateErr == nil || !strings.Contains(investigateErr.Error(), "종료 요청") {
		t.Errorf("investigate: %v", investigateErr)
	}

	// And an ordinary non-zero exit keeps the words it had.
	_, ordinary := parseReview("", "boom", 1)
	if ordinary == nil || !strings.Contains(ordinary.Error(), "읽지 못했습니다") {
		t.Errorf("an ordinary failure changed meaning: %v", ordinary)
	}
}
