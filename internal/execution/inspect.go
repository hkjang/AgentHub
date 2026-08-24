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
