package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// ErrAwaitingApproval parks a task while a human decides. It is not a failure:
// the worker must not retry it, and the attempt must not be charged.
var ErrAwaitingApproval = errors.New("task is waiting for approval")

// plan asks the model for a plan before the agent starts working.
//
// Whether this happens is the agent's own configuration. OpenCode and Hermes run
// their own agent loops, so 'native' leaves planning to them; imposing a platform
// plan there would fight the adapter rather than help it.
func (o *Orchestrator) plan(ctx context.Context, run *store.AgentRun, task store.AgentTask, goal store.AgentGoal, model resolvedModel) string {
	if goal.PlannerMode != "platform" && goal.PlannerMode != "hybrid" {
		return ""
	}
	step := workflow.Step{
		ID: "plan", AgentName: "Planner",
		SystemPrompt: "당신은 실행 계획을 세우는 플래너입니다. 주어진 목표와 완료 조건을 달성할 단계를 설계하고, " +
			`반드시 {"steps":[{"id":"01","type":"tool|reasoning|artifact","action":"..."}]} 형식의 JSON만 출력하세요. ` +
			"설명이나 코드 펜스를 붙이지 마세요.",
		ModelBaseURL: model.BaseURL, ModelName: model.ModelName, ModelAPIKey: model.APIKey,
	}
	var b strings.Builder
	b.WriteString("# 목표\n")
	b.WriteString(goal.Description)
	b.WriteString("\n\n# Task\n")
	b.WriteString(task.Title)
	if strings.TrimSpace(task.Input) != "" {
		b.WriteString("\n")
		b.WriteString(task.Input)
	}
	if len(goal.SuccessCriteria) > 0 {
		b.WriteString("\n\n# 완료 조건\n")
		for _, criterion := range goal.SuccessCriteria {
			b.WriteString("- ")
			b.WriteString(criterion)
			b.WriteString("\n")
		}
	}

	startedAt := time.Now()
	output, usage, err := o.complete(ctx, step, b.String())
	run.TotalTokens += usage.TotalTokens
	run.StepCount++
	if _, storeErr := o.store.AppendRunStep(ctx, store.AgentRunStep{
		RunID: run.ID, Sequence: run.StepCount, Type: "plan", Title: "실행 계획",
		Input: b.String(), Output: output, Status: stepStatus(err), Error: errorText(err),
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		DurationMs: time.Since(startedAt).Milliseconds(),
	}); storeErr != nil {
		o.logger.Error("plan step could not be recorded", "run", run.ID, "error", storeErr)
	}
	if err != nil {
		// Planning is an aid, not a gate: a planner that cannot be reached must
		// not stop the agent from attempting the work.
		o.event(ctx, *run, "plan.failed", err.Error(), nil)
		return ""
	}

	steps := json.RawMessage(`[]`)
	var decoded struct {
		Steps json.RawMessage `json:"steps"`
	}
	if json.Unmarshal([]byte(extractJSON(output)), &decoded) == nil && len(decoded.Steps) > 0 {
		steps = decoded.Steps
	}
	if _, err := o.store.CreatePlan(ctx, store.AgentPlan{RunID: run.ID, TaskID: task.ID, Mode: goal.PlannerMode, Goal: goal.Description, Steps: steps}); err != nil {
		o.logger.Warn("plan could not be stored", "run", run.ID, "error", err)
	}
	o.event(ctx, *run, "plan.created", "실행 계획을 수립했습니다.", map[string]any{"mode": goal.PlannerMode})
	return output
}

// loadMemory renders what the agent already knows into a prompt section.
func (o *Orchestrator) loadMemory(ctx context.Context, agentID string) string {
	memories, err := o.store.Memories(ctx, agentID)
	if err != nil {
		o.logger.Warn("memory could not be read", "agent", agentID, "error", err)
		return ""
	}
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n# 기억하고 있는 사실\n")
	for _, memory := range memories {
		b.WriteString("- ")
		b.WriteString(memory.Key)
		b.WriteString(": ")
		b.WriteString(memory.Value)
		b.WriteString("\n")
	}
	return b.String()
}

// saveMemory persists MEMORY directives. Entries are scoped to the agent so they
// outlive both the run and the Runtime Pod.
func (o *Orchestrator) saveMemory(ctx context.Context, run store.AgentRun, task store.AgentTask, output string) {
	for _, directive := range directivesOfKind(output, directiveMemory) {
		if directive.Arg == "" {
			continue
		}
		agentID := run.AgentID
		entry := store.AgentMemory{
			OwnerID: task.OwnerID, Scope: "agent", AgentID: &agentID,
			Key: directive.Arg, Value: directive.Body,
		}
		if err := o.store.PutMemory(ctx, entry, run.ID); err != nil {
			o.logger.Warn("memory could not be stored", "run", run.ID, "key", directive.Arg, "error", err)
			continue
		}
		o.event(ctx, run, "memory.written", directive.Arg, map[string]any{"scope": "agent"})
	}
}

// requestApproval raises an approval and parks the task.
//
// This is the gate from requirement 28: an autonomous agent must not perform a
// state-changing action on its own. The agent declares the intent, a person
// decides, and the task resumes with that decision in its transcript.
func (o *Orchestrator) requestApproval(ctx context.Context, run store.AgentRun, task store.AgentTask, directive Directive) error {
	summary := directive.Arg
	if summary == "" {
		summary = firstLine(directive.Body)
	}
	approval, err := o.store.CreateApproval(ctx, task.OwnerID, "task", task.ID, "agent.action", summary, map[string]any{
		"taskId": task.ID, "runId": run.ID, "agentId": run.AgentID, "detail": directive.Body,
	})
	if err != nil {
		return err
	}
	if err := o.store.ParkTaskForApproval(ctx, task.ID, approval.ID); err != nil {
		return err
	}
	o.event(ctx, run, "approval.requested", summary, map[string]any{"approvalId": approval.ID})
	told, notifyErr := o.store.NotifyApprovers(ctx, approval, "에이전트 작업 승인 요청",
		task.Title+" 작업이 상태 변경 작업 승인을 기다리고 있습니다: "+summary)
	if notifyErr != nil {
		o.logger.Warn("approval reviewers could not be notified", "approval", approval.ID, "error", notifyErr)
	} else if told == 0 {
		// Nobody can answer this. Said out loud, because the task is now waiting on
		// a person who does not exist.
		o.logger.Error("nobody was told about an approval request", "approval", approval.ID, "task", task.ID)
	}
	// The owner is told too: their task has stopped and they should know why.
	_ = o.store.CreateNotification(ctx, task.OwnerID, "approval", "작업이 승인을 기다립니다",
		task.Title+" — "+summary, "/tasks")
	return nil
}

// delegate hands a sub-task to another agent.
//
// It always goes through the task queue rather than calling the other agent's
// Runtime, which is what keeps permissions, quota, depth and the audit trail
// intact. A cycle or an exceeded depth is refused and reported back to the
// delegating agent rather than silently dropped.
func (o *Orchestrator) delegate(ctx context.Context, run store.AgentRun, task store.AgentTask, goal store.AgentGoal, directive Directive) string {
	if goal.MaxDelegationDepth <= 0 {
		return fmt.Sprintf("위임이 허용되지 않은 Agent입니다: %s", directive.Arg)
	}
	if task.Delegation >= goal.MaxDelegationDepth {
		return fmt.Sprintf("위임 깊이 상한(%d)에 도달해 %s 에게 위임하지 못했습니다.", goal.MaxDelegationDepth, directive.Arg)
	}
	target, err := o.store.AgentByName(ctx, task.OwnerID, directive.Arg)
	if err != nil {
		return fmt.Sprintf("위임 대상 Agent를 찾지 못했습니다: %s", directive.Arg)
	}
	if target.ID == run.AgentID {
		return "자기 자신에게는 위임할 수 없습니다."
	}
	// A → B → C → A would otherwise run forever.
	chain, err := o.store.DelegationChain(ctx, task.ID)
	if err == nil {
		for _, ancestor := range chain {
			if ancestor == target.ID {
				return fmt.Sprintf("%s 는 이미 이 작업 계보에 포함되어 있어 순환 위임이 됩니다.", target.Name)
			}
		}
	}
	if target.ModelEndpointID == nil || *target.ModelEndpointID == "" {
		return fmt.Sprintf("%s 에는 Model Endpoint가 연결되어 있지 않아 위임할 수 없습니다.", target.Name)
	}

	title := firstLine(directive.Body)
	if title == "" {
		title = task.Title + " (위임)"
	}
	parent := task.ID
	child, err := o.store.CreateAgentTask(ctx, store.CreateTaskInput{
		AgentID: target.ID, OwnerID: task.OwnerID, Title: title, Input: directive.Body,
		Priority: task.Priority, Source: "agent", CreatedBy: task.OwnerID,
		ParentTaskID: &parent, Delegation: task.Delegation + 1,
	})
	if err != nil {
		o.logger.Error("delegated task could not be created", "run", run.ID, "target", target.ID, "error", err)
		return "위임 작업을 생성하지 못했습니다."
	}
	o.event(ctx, run, "task.delegated", target.Name, map[string]any{"taskId": child.ID, "agentId": target.ID, "depth": child.Delegation})
	return fmt.Sprintf("%s 에게 작업을 위임했습니다 (Task %s). 결과는 별도 작업으로 추적됩니다.", target.Name, child.ID[:8])
}

// approvalContext renders a decided approval so the resumed run can see it.
func (o *Orchestrator) approvalContext(ctx context.Context, task store.AgentTask) string {
	if task.ApprovalID == nil || *task.ApprovalID == "" {
		return ""
	}
	status, reason, err := o.store.ApprovalDecisionForTask(ctx, task.ID)
	if err != nil {
		return ""
	}
	switch status {
	case "approved":
		return "\n# 승인 결과\n요청한 작업이 승인되었습니다: " + reason + "\n이제 해당 작업을 수행하고 마무리하세요.\n"
	case "rejected":
		return "\n# 승인 결과\n요청한 작업이 거절되었습니다: " + reason + "\n해당 작업을 수행하지 말고, 승인 없이 가능한 범위로 마무리하세요.\n"
	}
	return ""
}
