package execution

import (
	"context"
	"fmt"

	"github.com/hkjang/AgentHub/internal/store"
)

// A retry used to be a restart. The task went back to step one with an empty
// transcript, so every reasoning step was paid for a second time and any step
// that had already changed something outside the platform — a deployment, a
// delegated task, a file written in the workspace — happened again. The same held
// for a task that resumed after an approval: the reasoning that led it to ask for
// approval was thrown away before the decision was applied.
//
// An attempt now starts from what earlier attempts finished.
type checkpoint struct {
	// transcript is what the model is shown as already done, oldest first. It can
	// be shorter than steps when older entries had to be left out to keep the
	// prompt within budget.
	transcript []string
	// steps is how many completed steps the task has. Step numbering continues
	// from here, so a run record reads as one piece of work.
	steps int
	// lastRunID is the run that produced the most recent completed step.
	lastRunID string
	// dropped is how many of the oldest entries did not fit.
	dropped int
}

// maxResumeEntries and maxResumeChars bound what one prompt carries. A task that
// ran for many steps would otherwise resume into a prompt that costs more than
// redoing the work — which is the opposite of the point.
const (
	maxResumeEntries = 40
	maxResumeChars   = 40000
)

// checkpoint reads the completed work this attempt should continue from. A
// failure to read it is not a reason to fail the task: the attempt starts over,
// which is what it did before this existed.
func (o *Orchestrator) checkpoint(ctx context.Context, task store.AgentTask, agent store.Agent, goal store.AgentGoal) checkpoint {
	if !goal.ResumeFromCheckpoint {
		return checkpoint{}
	}
	saved, err := o.store.TaskCheckpoint(ctx, task.ID, agent.Version)
	if err != nil {
		o.logger.Warn("checkpoint could not be read; the attempt starts from the beginning",
			"task", task.ID, "error", err)
		return checkpoint{}
	}
	return trimCheckpoint(saved)
}

// trimCheckpoint keeps the most recent work when the record is longer than one
// prompt can carry, and tells the model what was left out rather than presenting
// a silently truncated history as the whole of it.
func trimCheckpoint(saved store.Checkpoint) checkpoint {
	result := checkpoint{steps: saved.Steps, lastRunID: saved.LastRunID}
	if len(saved.Outputs) == 0 {
		return result
	}
	kept := make([]string, 0, len(saved.Outputs))
	budget := maxResumeChars
	for index := len(saved.Outputs) - 1; index >= 0; index-- {
		entry := saved.Outputs[index]
		// The newest entry is always carried, however long it is: an attempt that
		// resumed with nothing at all would repeat the work instead.
		if len(kept) > 0 && (len(kept) >= maxResumeEntries || len(entry) > budget) {
			result.dropped = index + 1
			break
		}
		budget -= len(entry)
		kept = append(kept, entry)
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	if result.dropped > 0 {
		notice := fmt.Sprintf("(이전 시도의 앞선 %d단계는 길이 제한으로 생략되었습니다. 아래 내용부터 이어서 진행하세요.)", result.dropped)
		kept = append([]string{notice}, kept...)
	}
	result.transcript = kept
	return result
}
