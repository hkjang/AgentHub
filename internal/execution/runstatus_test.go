package execution

import (
	"context"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A handoff is the task queue's "런타임 인계" — a wait, the guide says, not a
// failure. The run that carried the work to that point used to be filed as
// failed with no reason, so the history disagreed with the queue about the same
// attempt. Photographing the run history for the guide is what surfaced it.
func TestAHandedOffRunIsCompletedWorkNotAFailure(t *testing.T) {
	cases := []struct {
		name    string
		outcome Outcome
		ctxErr  error
		want    string
	}{
		{"handoff", Outcome{Status: store.TaskHandoff, parked: ErrHandedOff}, nil, "completed"},
		{"approval", Outcome{Status: "waiting_approval", parked: ErrAwaitingApproval}, nil, "completed"},
		{"completed", Outcome{Status: store.TaskCompleted}, nil, "completed"},
		{"quota wait", Outcome{parked: ErrRuntimeQuota}, nil, "cancelled"},
		{"failed", Outcome{Status: store.TaskFailed, Failure: "모델이 답을 돌려주지 않았습니다."}, nil, "failed"},
		{"cancelled mid-run", Outcome{Status: store.TaskFailed}, context.Canceled, "cancelled"},
		{"deadline is a failure", Outcome{Status: store.TaskFailed}, context.DeadlineExceeded, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runStatus(tc.outcome, tc.ctxErr); got != tc.want {
				t.Fatalf("runStatus(%+v, %v) = %q, want %q", tc.outcome, tc.ctxErr, got, tc.want)
			}
		})
	}
}
