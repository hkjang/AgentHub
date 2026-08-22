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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimespec"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
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
	// flowInspector scans text entering and leaving a runtime's own flow engine.
	flowInspector FlowInspector
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
	// nor failure: a pending approval, or a handover to a person in the runtime.
	parked error
	// Note carries what a parked task is waiting for, so the worker can tell the
	// owner without re-reading the row it was just written to.
	Note string
	// Retryable distinguishes an infrastructure hiccup, which is worth another
	// attempt, from the agent genuinely failing to meet its goal.
	Retryable bool
}

// Execute runs one attempt. It returns an Outcome rather than an error for
// anything the agent itself caused, so the worker can apply the retry policy
// without unpacking error strings.
func (o *Orchestrator) Execute(ctx context.Context, task store.AgentTask, traceID string) (Outcome, error) {
	// One span per attempt, with everything the run does hanging off it. The
	// platform's own trace id becomes the span's when tracing is on, so the id in
	// the console, in the log line and in the collector are the same string.
	ctx, span := telemetry.Start(ctx, "task.execute",
		attribute.String("agenthub.task.id", task.ID),
		attribute.String("agenthub.agent.id", task.AgentID),
		attribute.Int("agenthub.task.attempt", task.Attempts),
		attribute.String("agenthub.task.priority", task.Priority),
		attribute.String("agenthub.task.source", task.Source),
	)
	defer span.End()
	if id := telemetry.TraceID(ctx); id != "" {
		traceID = id
	}

	agent, err := o.store.AgentByID(ctx, task.AgentID, task.OwnerID, true)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: "Agent 정의를 찾을 수 없습니다."}, err
	}
	goal, err := o.store.AgentGoalByID(ctx, agent.ID)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: err.Error()}, err
	}

	// What earlier attempts already finished. A retry — automatic, manual, or the
	// resumption after an approval — continues from here instead of paying for the
	// same reasoning again and repeating whatever those steps already did.
	resume := o.checkpoint(ctx, task, agent, goal)

	run := store.AgentRun{
		TaskID: task.ID, AgentID: agent.ID, OwnerID: task.OwnerID,
		Attempt: task.Attempts, AgentVersion: agent.Version, TraceID: traceID, WorkerID: o.workerID,
		ModelEndpointID: agent.ModelEndpointID, ResumedSteps: resume.steps,
	}
	run, err = o.store.CreateAgentRun(ctx, run)
	if err != nil {
		return Outcome{Status: store.TaskFailed, Failure: err.Error()}, err
	}
	started := time.Now()
	o.event(ctx, run, "task.started", "Task 실행을 시작했습니다.", map[string]any{"attempt": task.Attempts, "priority": task.Priority})
	if len(resume.transcript) > 0 {
		o.event(ctx, run, "task.resumed", fmt.Sprintf("이전 시도에서 완료한 %d단계를 이어받아 계속합니다.", resume.steps),
			map[string]any{"resumedSteps": resume.steps, "carriedSteps": len(resume.transcript), "fromRun": resume.lastRunID})
	}

	// The whole attempt is bounded by the goal's duration limit.
	if goal.MaxDurationSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(goal.MaxDurationSeconds)*time.Second)
		defer cancel()
	}

	outcome := o.run(ctx, &run, task, agent, goal, resume)
	run.DurationMs = time.Since(started).Milliseconds()
	switch {
	case errors.Is(outcome.parked, ErrRuntimeQuota):
		// The attempt did nothing and will be made again, so recording it as
		// completed work would put a run in the history that never ran.
		run.Status = "cancelled"
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
	if run.Metering == "" {
		// Nothing claimed the accounting. On the platform's own reasoning loop that
		// means every model call went through the gateway and was counted there. On
		// any other backend it means the run ended before the agent reported
		// anything — which is the conservative label, not the flattering one.
		run.Metering = store.MeteringUnmetered
		if goal.Runner == store.RunnerProse || goal.Runner == "" {
			run.Metering = store.MeteringGateway
		}
	}
	// The run is over, and one of the ways it ends is by running out of the time
	// the goal gave it — which cancels the very context the ending would be
	// written with. Recorded on the run's own context, a task that hit its
	// duration limit failed to write its outcome and stayed "running" in every
	// list forever, which is the one state it certainly was not in.
	finish, endFinish := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer endFinish()
	if err := o.store.FinishAgentRun(finish, run); err != nil {
		o.logger.Error("run could not be recorded", "run", run.ID, "error", err)
	}
	// The attributes an operator sorts by when a nightly agent got slow or
	// expensive: how long, how many steps, how many tokens, and how it ended.
	span.SetAttributes(
		attribute.String("agenthub.run.id", run.ID),
		attribute.String("agenthub.run.status", run.Status),
		attribute.Int("agenthub.run.steps", run.StepCount),
		attribute.Int("agenthub.run.resumed_steps", run.ResumedSteps),
		attribute.Int("agenthub.run.tokens", run.TotalTokens),
		attribute.String("agenthub.model.name", run.ModelName),
	)
	if run.Status == "failed" {
		span.SetStatus(codes.Error, outcome.Failure)
	}
	o.event(finish, run, "task."+outcome.Status, outcome.Failure, map[string]any{
		"durationMs": run.DurationMs, "steps": run.StepCount, "totalTokens": run.TotalTokens,
		"metering": run.Metering,
	})
	return outcome, nil
}

// run is the body of one attempt, separated so Execute always finalises the run.
func (o *Orchestrator) run(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, resume checkpoint) Outcome {
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
		acquireCtx, acquireSpan := telemetry.Start(ctx, "runtime.acquire", attribute.String("agenthub.agent.id", agent.ID))
		acquired, err = o.acquireRuntime(acquireCtx, *run, agent)
		telemetry.Fail(acquireSpan, err)
		if acquired != nil {
			acquireSpan.SetAttributes(attribute.String("agenthub.runtime.id", acquired.runtimeID))
		}
		acquireSpan.End()
		if errors.Is(err, ErrRuntimeQuota) {
			// Not a failure and not this task's fault: a limit is full and clears
			// when a runtime somewhere stops. The worker puts it back on the queue
			// without spending an attempt.
			return Outcome{parked: ErrRuntimeQuota, Note: strings.TrimPrefix(err.Error(), ErrRuntimeQuota.Error()+": ")}
		}
		if err != nil {
			// Runtime trouble is infrastructure, not the agent failing its goal.
			return Outcome{Status: store.TaskFailed, Failure: "Runtime을 확보하지 못했습니다: " + err.Error(), Retryable: true}
		}
		if acquired != nil {
			run.RuntimeID = &acquired.runtimeID
			defer o.releaseRuntime(context.WithoutCancel(ctx), *run, agent, goal, acquired)
		}
	}

	// Some Goals do not reason step by step, because the work is a program that
	// already exists: a flow somebody drew, the runtime's own agent, or an
	// application the site already runs. All of them still go through the same
	// evaluator afterwards, so completion is judged by the same criteria as any
	// other agent's.
	switch goal.Runner {
	case store.RunnerFlow:
		transcript, outcome := o.runFlow(ctx, run, task, agent, goal, acquired)
		if outcome.Status != "" {
			return outcome
		}
		return o.evaluate(ctx, run, task, agent, goal, model, transcript)
	case store.RunnerCLI:
		transcript, outcome := o.runCLI(ctx, run, task, agent, goal, model, acquired)
		if outcome.Status != "" {
			return outcome
		}
		return o.evaluate(ctx, run, task, agent, goal, model, transcript)
	case store.RunnerACP:
		transcript, outcome := o.runACP(ctx, run, task, agent, goal, acquired)
		if outcome.Status != "" {
			return outcome
		}
		return o.evaluate(ctx, run, task, agent, goal, model, transcript)
	case store.RunnerInvestigate:
		transcript, outcome := o.runInvestigate(ctx, run, task, agent, goal, model, acquired)
		if outcome.Status != "" {
			return outcome
		}
		return o.evaluate(ctx, run, task, agent, goal, model, transcript)
	case store.RunnerDify:
		transcript, outcome := o.runExternalApp(ctx, run, task, agent, goal)
		if outcome.Status != "" {
			return outcome
		}
		return o.evaluate(ctx, run, task, agent, goal, model, transcript)
	}

	plan := ""
	if len(resume.transcript) == 0 {
		plan = o.plan(ctx, run, task, goal, model)
	}

	transcript, outcome := o.think(ctx, run, task, agent, goal, model, plan, resume, o.environment(ctx, agent, goal, acquired != nil))
	if errors.Is(outcome.parked, ErrAwaitingApproval) || errors.Is(outcome.parked, ErrHandedOff) {
		return outcome
	}
	if outcome.Status != "" {
		return outcome
	}
	return o.evaluate(ctx, run, task, agent, goal, model, transcript)
}

// ErrHandedOff parks a task that needs a person in the runtime. It is a sentinel
// for the same reason ErrAwaitingApproval is: the worker must not treat a parked
// task as a failure and must not touch its status on the way out.
var ErrHandedOff = errors.New("런타임 인계를 기다립니다")

// environment resolves what this run can actually reach, for the prompt.
//
// Everything here is read from what the platform already knows, and a lookup that
// fails degrades to "less is claimed" rather than to a failed task: an agent told
// nothing about its tools is worse off than one told nothing at all, but a task
// that dies because a workspace name could not be read is worse than both.
func (o *Orchestrator) environment(ctx context.Context, agent store.Agent, goal store.AgentGoal, runtimeReady bool) environment {
	env := environment{
		Runtime:      runtimetype.Describe(agent.RuntimeType),
		RuntimeReady: runtimeReady,
		// Handing over is only meaningful when somebody can open the runtime and
		// find the work where the agent left it: a persistent workspace for the work
		// to live in, and a runtime with a surface a person can actually use. Whether
		// this task started the Pod is beside the point — a person can start it.
		HandoffAllowed: agent.WorkspaceID != nil && *agent.WorkspaceID != "" && runtimetype.Describe(agent.RuntimeType).BrowserUI,
	}
	if agent.WorkspaceID != nil && *agent.WorkspaceID != "" {
		if workspace, err := o.store.WorkspaceByID(ctx, *agent.WorkspaceID, agent.OwnerID, true); err == nil {
			env.WorkspaceName = workspace.Name
		} else {
			o.logger.Debug("workspace name is unreadable for the prompt", "agent", agent.ID, "error", err)
		}
	}
	if agent.MCPBundleID != nil && *agent.MCPBundleID != "" {
		if servers, err := o.store.MCPServersForBundle(ctx, *agent.MCPBundleID); err == nil {
			for _, server := range servers {
				env.Tools = append(env.Tools, server.Name)
			}
		} else {
			o.logger.Debug("MCP bundle is unreadable for the prompt", "agent", agent.ID, "error", err)
		}
	}
	return env
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
func (o *Orchestrator) think(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, plan string, resume checkpoint, env environment) ([]string, Outcome) {
	// Everything the agent knows before it starts: its goal, what it can and
	// cannot reach from this loop, what it remembers from previous runs, the plan
	// it just made, and any approval decision that resumed this task.
	prelude := systemPromptWithEnvironment(agent, goal, env) + o.loadMemory(ctx, agent.ID)
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
	// The resumed work leads the transcript, so the model continues from it
	// instead of starting the task over.
	transcript := append([]string{}, resume.transcript...)
	// Step numbering continues across attempts, so the run record reads as one
	// piece of work rather than several that all start at 1.
	sequence := resume.steps
	for attemptStep := 1; attemptStep <= goal.MaxSteps; attemptStep++ {
		sequence++
		if err := ctx.Err(); err != nil {
			return transcript, Outcome{Status: store.TaskFailed, Failure: "최대 실행 시간을 초과했습니다.", Retryable: true}
		}
		prompt := stepPrompt(task, goal, transcript) + preamble
		startedAt := time.Now()
		stepCtx, stepSpan := telemetry.Start(ctx, "agent.step",
			attribute.Int("agenthub.step.sequence", sequence),
			attribute.String("agenthub.model.name", model.ModelName))
		output, usage, err := o.complete(stepCtx, step, prompt)
		elapsed := time.Since(startedAt).Milliseconds()
		stepSpan.SetAttributes(
			attribute.Int("agenthub.tokens.prompt", usage.PromptTokens),
			attribute.Int("agenthub.tokens.completion", usage.CompletionTokens),
			attribute.Int("agenthub.tokens.total", usage.TotalTokens))
		telemetry.Fail(stepSpan, err)
		stepSpan.End()

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
		// The count is this attempt's own work; what it inherited is recorded
		// separately, so a resumed run does not claim steps it never ran.
		run.StepCount = attemptStep
		run.TotalTokens += usage.TotalTokens

		if err != nil {
			o.event(ctx, *run, "step.failed", err.Error(), map[string]any{"sequence": sequence})
			// A model error is transient far more often than not — but a refusal by
			// the content scanner is not: the same prompt carries the same data, and
			// retrying would spend the whole budget arriving at the same answer.
			return transcript, Outcome{
				Status: store.TaskFailed, Failure: err.Error(),
				Retryable: !errors.Is(err, workflow.ErrBlocked),
			}
		}
		o.event(ctx, *run, "step.completed", record.Title, map[string]any{"sequence": sequence, "durationMs": elapsed, "tokens": usage.TotalTokens})
		// Each step is activity. Without this a run whose steps are long — an ACP
		// agent working through a repository, an investigation waiting on a cluster —
		// looks idle for exactly as long as it is busy.
		if run.RuntimeID != nil {
			o.store.TouchRuntime(ctx, *run.RuntimeID)
		}
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

		// A handoff stops the run the same way an approval does, and for the same
		// reason: the work is not finished and not failed, it is waiting for a
		// person. The difference is where they pick it up — the runtime's own
		// workspace rather than a review queue.
		if handoffs := directivesOfKind(output, directiveHandoff); env.HandoffAllowed && len(handoffs) > 0 {
			note := handoffNote(handoffs[0])
			if err := o.store.HandOffTask(ctx, task.ID, note); err != nil {
				return transcript, Outcome{Status: store.TaskFailed, Failure: "런타임 인계를 기록하지 못했습니다: " + err.Error(), Retryable: true}
			}
			sequence++
			if _, storeErr := o.store.AppendRunStep(ctx, store.AgentRunStep{
				RunID: run.ID, Sequence: sequence, Type: "completion",
				Title: "런타임 인계 요청", Output: note, Status: "succeeded",
			}); storeErr != nil {
				o.logger.Error("handoff step could not be recorded", "run", run.ID, "error", storeErr)
			}
			o.event(ctx, *run, "task.handoff", note, map[string]any{"agentId": agent.ID})
			transcript = append(transcript, note)
			return transcript, Outcome{Status: store.TaskHandoff, parked: ErrHandedOff, Note: note, Result: strings.Join(transcript, "\n\n")}
		}

		// Delegation results are fed back so the agent can carry on knowing what
		// was handed off and what was refused.
		if delegations := directivesOfKind(output, directiveDelegate); len(delegations) > 0 {
			notes := make([]string, 0, len(delegations))
			for _, directive := range delegations {
				notes = append(notes, o.delegate(ctx, *run, task, goal, directive))
			}
			note := "# 위임 결과\n" + strings.Join(notes, "\n")
			transcript = append(transcript, note)
			// Recorded as a step of its own, so a resumed attempt inherits what was
			// delegated instead of handing the same work over a second time.
			sequence++
			if _, storeErr := o.store.AppendRunStep(ctx, store.AgentRunStep{
				RunID: run.ID, Sequence: sequence, Type: "delegation",
				Title: fmt.Sprintf("위임 %d건", len(delegations)), Output: note, Status: "succeeded",
			}); storeErr != nil {
				o.logger.Error("delegation step could not be recorded", "run", run.ID, "error", storeErr)
			}
			continue
		}

		if declaresCompletion(output) {
			return transcript, Outcome{}
		}
	}
	// Running out of steps is the agent failing to converge, not infrastructure.
	return transcript, Outcome{Status: store.TaskFailed, Failure: fmt.Sprintf("최대 단계 수(%d)에 도달했지만 목표를 완료하지 못했습니다.", goal.MaxSteps)}
}

// completeStructured asks for a schema-constrained answer when the model client
// can, and asks in prose when it cannot — the caller validates either way.
func (o *Orchestrator) completeStructured(ctx context.Context, step workflow.Step, prompt string, schema workflow.Schema) (workflow.StructuredResult, error) {
	if structured, ok := o.completion.(workflow.StructuredCompleter); ok {
		return structured.CompleteStructured(ctx, step, prompt, schema)
	}
	output, usage, err := o.complete(ctx, step, prompt)
	return workflow.StructuredResult{Output: output, Usage: usage}, err
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
