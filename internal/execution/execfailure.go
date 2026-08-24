package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/hkjang/AgentHub/internal/korean"
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

// runtimeExecFailure explains an Exec that ended without a result.
func runtimeExecFailure(what string, err error, goal store.AgentGoal) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		if goal.MaxDurationSeconds > 0 {
			return fmt.Sprintf("%s%s 최대 실행 시간(%d초)에 도달해 중단됐습니다. Goal의 최대 실행 시간을 늘리거나, 대상 범위를 줄여 주세요.",
				what, korean.Subject(what), goal.MaxDurationSeconds)
		}
		// No limit on the Goal means the deadline came from somewhere else, and
		// guessing which would be worse than saying it plainly.
		return what + korean.Subject(what) + " 제한 시간에 도달해 중단됐습니다."
	case errors.Is(err, context.Canceled):
		return what + korean.Subject(what) + " 취소됐습니다."
	}
	return fmt.Sprintf("Runtime에서 %s%s 실행하지 못했습니다: %s", what, korean.Object(what), err.Error())
}
