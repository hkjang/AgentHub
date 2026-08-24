package execution

import (
	"context"

	"github.com/hkjang/AgentHub/internal/workflow"
)

// Scanning an answer before anything keeps it.
//
// Content inspection blocked what an agent said from being used, and every
// backend had already written the raw text into the run's step: the card number
// the scanner refused to pass on was in this platform's own database, on the
// run's timeline, for anybody who could open it. Blocking the use of data that
// has already been stored is not the feature people configured.
//
// Measured on a cluster: a model answering with a card number produced a failed
// task and a step whose output was the card number.
//
// So the scan happens where the answer is produced, before it is recorded, and
// what gets recorded is what the scanner returned — redacted where that is the
// action, and nothing at all where it is blocked.
func (o *Orchestrator) inspectAnswer(ctx context.Context, step workflow.Step, answer string) (string, error) {
	if o.flowInspector == nil || answer == "" {
		return answer, nil
	}
	return o.flowInspector.Inbound(ctx, step, answer)
}

// failureWith says both things that went wrong.
//
// A run can fail for its own reason and have its text refused by the scanner in
// the same moment. The step recorded whichever was assigned last, and the answer
// was gone either way — so an empty output explained by a timeout read as a run
// that produced nothing, when what happened is that the platform would not keep
// what it produced.
//
// Measured live on the fabric backend: a run cancelled while a card number sat
// in its workers' words stored an empty step whose only explanation was
// "워커 실행이 취소됐습니다".
func failureWith(primary, refusal error) string {
	switch {
	case primary == nil && refusal == nil:
		return ""
	case primary == nil:
		return refusal.Error()
	case refusal == nil:
		return primary.Error()
	}
	return primary.Error() + " — " + refusal.Error()
}
