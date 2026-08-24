package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/hkjang/AgentHub/internal/store"
)

// Why the thing in the Pod stopped, in words that name the right repair.
//
// Every backend that runs a command in a runtime reported the same sentence for
// every ending: "Runtime에서 …을 실행하지 못했습니다: context deadline exceeded".
// That reads as a runtime which would not start, and sends somebody to look at
// Kubernetes — when what happened is that the run reached the time limit their
// own Goal sets, with the engine still working.
//
// Observed on a real cluster: a review against a gateway that never answered
// ended exactly that way, at 58 seconds of a 60-second limit.

// subjectParticle picks 이 or 가 for a word, and objectParticle 을 or 를.
//
// Korean chooses these by whether the last syllable ends in a consonant, so a
// message that hardcodes one is wrong for half the words it is given: the first
// version of this said "리뷰이 최대 실행 시간에 도달해" — a sentence no Korean
// speaker would write, in the place somebody reads when something has gone
// wrong.
func hasFinalConsonant(word string) bool {
	runes := []rune(word)
	if len(runes) == 0 {
		return false
	}
	last := runes[len(runes)-1]
	if last < 0xAC00 || last > 0xD7A3 {
		// Not a Hangul syllable — a number or a Latin word. Neither form is
		// right for every case, and this is the commoner one.
		return false
	}
	return (last-0xAC00)%28 != 0
}

func subjectParticle(word string) string {
	if hasFinalConsonant(word) {
		return "이"
	}
	return "가"
}

func objectParticle(word string) string {
	if hasFinalConsonant(word) {
		return "을"
	}
	return "를"
}

// runtimeExecFailure explains an Exec that ended without a result.
func runtimeExecFailure(what string, err error, goal store.AgentGoal) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		if goal.MaxDurationSeconds > 0 {
			return fmt.Sprintf("%s%s 최대 실행 시간(%d초)에 도달해 중단됐습니다. Goal의 최대 실행 시간을 늘리거나, 대상 범위를 줄여 주세요.",
				what, subjectParticle(what), goal.MaxDurationSeconds)
		}
		// No limit on the Goal means the deadline came from somewhere else, and
		// guessing which would be worse than saying it plainly.
		return what + subjectParticle(what) + " 제한 시간에 도달해 중단됐습니다."
	case errors.Is(err, context.Canceled):
		return what + subjectParticle(what) + " 취소됐습니다."
	}
	return fmt.Sprintf("Runtime에서 %s%s 실행하지 못했습니다: %s", what, objectParticle(what), err.Error())
}
