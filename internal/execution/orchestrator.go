// Package execution runs Agent Tasks autonomously.
//
// The control plane hands work to this package as a queued AgentTask; the
// orchestrator turns one task attempt into an AgentRun, acquires a Runtime if
// the agent's policy asks for one, drives the agent toward its Goal, decides
// whether the Goal was met, records the timeline and artifacts, and releases the
// Runtime again.
//
// Reasoning is executed against the agent's bound model endpoint. Tool use
// inside a step belongs to the runtime adapters and is reached through their own
// sessions; what this package owns is the task lifecycle, the guardrails and the
// evidence trail.
package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimespec"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Completion is the reasoning capability one step needs. It matches the workflow
// engine's seam so both planes share one model client.
type Completion interface {
	Complete(ctx context.Context, step workflow.Step, prompt string) (string, error)
}

// Orchestrator executes one task at a time. A worker runs several of these
// concurrently; the type holds no per-task state.
type Orchestrator struct {
	store      *store.Store
	spawner    appRuntime.Spawner
	specs      *runtimespec.Builder
	completion Completion
	logger     *slog.Logger
	workerID   string
}

func New(db *store.Store, spawner appRuntime.Spawner, completion Completion, logger *slog.Logger, workerID string) *Orchestrator {
	return &Orchestrator{store: db, spawner: spawner, specs: runtimespec.New(db, logger), completion: completion, logger: logger, workerID: workerID}
}

// Outcome tells the worker what to do with the task afterwards.
type Outcome struct {
	Status  string
	Result  string
	Failure string
	// parked is set when the task stopped for a reason that is neither success
	// nor failure — today only a pending approval.
	parked error
	// Retryable distinguishes an infrastructure hiccup, which is worth another
	// attempt, from the agent genuinely failing to meet its goal.
	Retryable bool
}

// Execute runs one attempt. It returns an Outcome rather than an error for
// anything the agent itself caused, so the worker can apply the retry policy
// without unpacking error strings.
func (o *Orchestrator) Execute(ctx context.Context, task store.AgentTask, traceID string) (Outcome, error) {
	agent, err := o.store.AgentByID(ctx, task.AgentID, task.OwnerID, true)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: "Agent 정의를 찾을 수 없습니다."}, err
	}
	goal, err := o.store.AgentGoalByID(ctx, agent.ID)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: err.Error()}, err
	}

	run := store.AgentRun{
		TaskID: task.ID, AgentID: agent.ID, OwnerID: task.OwnerID,
		Attempt: task.Attempts, AgentVersion: agent.Version, TraceID: traceID, WorkerID: o.workerID,
		ModelEndpointID: agent.ModelEndpointID,
	}
	run, err = o.store.CreateAgentRun(ctx, run)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: err.Error()}, err
	}
	started := time.Now()
	o.event(ctx, run, "task.started", "Task 실행을 시작했습니다.", map[string]any{"attempt": task.Attempts, "priority": task.Priority})

	// The whole attempt is bounded by the goal's duration limit.
	if goal.MaxDurationSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(goal.MaxDurationSeconds)*time.Second)
		defer cancel()
	}

	outcome := o.run(ctx, &run, task, agent, goal)
	run.DurationMs = time.Since(started).Milliseconds()
	switch {
	case errors.Is(outcome.parked, ErrAwaitingApproval):
		// The run ends here but the task has not: a new run is created when the
		// reviewer decides, so this one is recorded as completed work rather than
		// a failure.
		run.Status = "completed"
	case outcome.Status == store.TaskCompleted:
		run.Status = "completed"
	case errors.Is(ctx.Err(), context.Canceled):
		run.Status = "cancelled"
	default:
		run.Status = "failed"
	}
	run.Result, run.FailureReason = outcome.Result, outcome.Failure
	if err := o.store.FinishAgentRun(ctx, run); err != nil {
		o.logger.Error("run could not be recorded", "run", run.ID, "error", err)
	}
	o.event(ctx, run, "task."+outcome.Status, outcome.Failure, map[string]any{
		"durationMs": run.DurationMs, "steps": run.StepCount, "totalTokens": run.TotalTokens,
	})
	return outcome, nil
}

// run is the body of one attempt, separated so Execute always finalises the run.
func (o *Orchestrator) run(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal) Outcome {
	// Concurrency policy is enforced here rather than at enqueue time, because a
	// task can sit in the queue long enough for the situation to change.
	if goal.ConcurrencyPolicy == "reject" {
		if active, err := o.store.RunningRunsForAgent(ctx, agent.ID, run.ID); err == nil && active >= goal.MaxConcurrentRuns {
			return Outcome{Status: store.TaskFailed, Failure: "이 Agent는 이미 실행 중이며 중복 실행이 거부되도록 설정되어 있습니다."}
		}
	}

	model, err := o.resolveModel(ctx, agent)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: err.Error(), Retryable: false}
	}
	run.ModelName = model.ModelName

	// Acquire a Runtime when the agent's policy asks for one. This is what lets a
	// user open the live Runtime mid-task, and what the workspace and MCP tools
	// live in.
	var acquired *acquiredRuntime
	if goal.StartOnDemand {
		acquired, err = o.acquireRuntime(ctx, *run, agent)
		if err != nil {
			// Runtime trouble is infrastructure, not the agent failing its goal.
			return Outcome{Status: store.TaskFailed, Failure: "Runtime을 확보하지 못했습니다: " + err.Error(), Retryable: true}
		}
		if acquired != nil {
			run.RuntimeID = &acquired.runtimeID
			defer o.releaseRuntime(context.WithoutCancel(ctx), *run, agent, goal, acquired)
		}
	}

	plan := o.plan(ctx, run, task, goal, model)

	transcript, outcome := o.think(ctx, run, task, agent, goal, model, plan)
	if errors.Is(outcome.parked, ErrAwaitingApproval) {
		return outcome
	}
	if outcome.Status != "" {
		return outcome
	}
	return o.evaluate(ctx, run, task, agent, goal, model, transcript)
}

type resolvedModel struct {
	BaseURL   string
	ModelName string
	APIKey    string
}

func (o *Orchestrator) resolveModel(ctx context.Context, agent store.Agent) (resolvedModel, error) {
	if agent.ModelEndpointID == nil || *agent.ModelEndpointID == "" {
		return resolvedModel{}, errors.New("이 Agent에는 Model Endpoint가 연결되어 있지 않아 자동 실행할 수 없습니다.")
	}
	endpoint, key, err := o.store.ModelEndpointByID(ctx, *agent.ModelEndpointID)
	if err != nil {
		return resolvedModel{}, err
	}
	if endpoint.BaseURL == "" || endpoint.DefaultModel == "" {
		return resolvedModel{}, errors.New("연결된 Model Endpoint에 Base URL 또는 Model 이름이 없습니다.")
	}
	return resolvedModel{BaseURL: endpoint.BaseURL, ModelName: endpoint.DefaultModel, APIKey: key}, nil
}

// think drives the agent toward its goal, one reasoning step at a time, until it
// reports completion or a guardrail stops it.
func (o *Orchestrator) think(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, plan string) ([]string, Outcome) {
	// Everything the agent knows before it starts: its goal, what it remembers
	// from previous runs, the plan it just made, and any approval decision that
	// resumed this task.
	prelude := systemPrompt(agent, goal) + o.loadMemory(ctx, agent.ID)
	step := workflow.Step{
		ID: "task", AgentID: agent.ID, AgentName: agent.Name,
		SystemPrompt: prelude,
		ModelBaseURL: model.BaseURL, ModelName: model.ModelName, ModelAPIKey: model.APIKey,
	}
	preamble := ""
	if strings.TrimSpace(plan) != "" {
		preamble += "\n# 실행 계획\n" + plan + "\n"
	}
	preamble += o.approvalContext(ctx, task)
	transcript := []string{}
	for sequence := 1; sequence <= goal.MaxSteps; sequence++ {
		if err := ctx.Err(); err != nil {
			return transcript, Outcome{Status: store.TaskFailed, Failure: "최대 실행 시간을 초과했습니다.", Retryable: true}
		}
		prompt := stepPrompt(task, goal, transcript) + preamble
		startedAt := time.Now()
		output, usage, err := o.complete(ctx, step, prompt)
		elapsed := time.Since(startedAt).Milliseconds()

		record := store.AgentRunStep{
			RunID: run.ID, Sequence: sequence, Type: "reasoning",
			Title: fmt.Sprintf("추론 %d", sequence), Input: prompt, Output: output,
			Status: "succeeded", PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, DurationMs: elapsed,
		}
		if err != nil {
			record.Status, record.Error = "failed", err.Error()
		}
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("run step could not be recorded", "run", run.ID, "error", storeErr)
		}
		run.StepCount = sequence
		run.TotalTokens += usage.TotalTokens

		if err != nil {
			o.event(ctx, *run, "step.failed", err.Error(), map[string]any{"sequence": sequence})
			// A model error is transient far more often than not.
			return transcript, Outcome{Status: store.TaskFailed, Failure: err.Error(), Retryable: true}
		}
		o.event(ctx, *run, "step.completed", record.Title, map[string]any{"sequence": sequence, "durationMs": elapsed, "tokens": usage.TotalTokens})
		transcript = append(transcript, output)
		o.saveMemory(ctx, *run, task, output)

		// A request for approval stops the run here. The task is parked rather
		// than failed, so it resumes on the reviewer's decision with its
		// transcript intact.
		if approvals := directivesOfKind(output, directiveApproval); goal.ApprovalRequired && len(approvals) > 0 {
			if err := o.requestApproval(ctx, *run, task, approvals[0]); err != nil {
				return transcript, Outcome{Status: store.TaskFailed, Failure: "승인 요청을 생성하지 못했습니다: " + err.Error(), Retryable: true}
			}
			return transcript, Outcome{Status: "waiting_approval", parked: ErrAwaitingApproval, Result: strings.Join(transcript, "\n\n")}
		}

		// Delegation results are fed back so the agent can carry on knowing what
		// was handed off and what was refused.
		if delegations := directivesOfKind(output, directiveDelegate); len(delegations) > 0 {
			notes := make([]string, 0, len(delegations))
			for _, directive := range delegations {
				notes = append(notes, o.delegate(ctx, *run, task, goal, directive))
			}
			transcript = append(transcript, "# 위임 결과\n"+strings.Join(notes, "\n"))
			continue
		}

		if declaresCompletion(output) {
			return transcript, Outcome{}
		}
	}
	// Running out of steps is the agent failing to converge, not infrastructure.
	return transcript, Outcome{Status: store.TaskFailed, Failure: fmt.Sprintf("최대 단계 수(%d)에 도달했지만 목표를 완료하지 못했습니다.", goal.MaxSteps)}
}

func (o *Orchestrator) complete(ctx context.Context, step workflow.Step, prompt string) (string, workflow.Usage, error) {
	if reporter, ok := o.completion.(workflow.UsageReporter); ok {
		return reporter.CompleteWithUsage(ctx, step, prompt)
	}
	output, err := o.completion.Complete(ctx, step, prompt)
	return output, workflow.Usage{}, err
}

func (o *Orchestrator) event(ctx context.Context, run store.AgentRun, eventType, message string, details any) {
	if err := o.store.AppendRunEvent(ctx, run.ID, run.TaskID, eventType, message, details); err != nil {
		o.logger.Warn("run event could not be recorded", "run", run.ID, "type", eventType, "error", err)
	}
}
