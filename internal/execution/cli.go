package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Running a task as the runtime's own agent.
//
// The prose loop cannot edit a file. A flow can, if somebody drew one. A terminal
// coding agent already does — it has a tool loop, it runs in the workspace, and
// it has a headless mode meant exactly for this. So for those runtimes an
// autonomous task is that agent, executed in its own container, with the Goal's
// guardrails handed to it as its own budgets: max steps become session turns,
// max tool calls become its tool budget, max duration becomes its wall clock.
//
// What the platform keeps is what it always keeps — the run, the step, the
// artifacts, the verdict, the quota, the audit trail. What is different from the
// flow runner is that this one reports real token usage, so a CLI run is metered
// like any other work rather than described as unmetered.

// ApprovalModes are what the runtime accepts. `yolo` is the only one that
// changes files without asking, which is why it is a deliberate choice on the
// Goal rather than a default. It is exported because the API validates against
// the same list, and two copies would drift into a mode that saves and then is
// rejected by the agent at three in the morning.
var ApprovalModes = []string{"plan", "default", "auto-edit", "auto", "yolo"}

// Exit codes the agent uses to say which guardrail stopped it. They are the
// runtime's own contract, and they are the difference between "try again" and
// "raise the limit".
const (
	cliExitTurnLimit = 53
	cliExitBudget    = 55
	cliExitInterrupt = 130
)

// runCLI executes the task as the runtime's own agent and returns its answer as
// the transcript, which the evaluator then judges like any other.
func (o *Orchestrator) runCLI(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "에이전트를 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	step := workflow.Step{ID: "cli", AgentID: agent.ID, AgentName: agent.Name}
	prompt := runnerInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, prompt)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		prompt = scanned
	}

	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime을 읽지 못했습니다: " + err.Error()}
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	adapter, adapterErr := adapterFor(agent.RuntimeType)
	if adapterErr != nil {
		return nil, Outcome{Status: store.TaskFailed, Failure: adapterErr.Error()}
	}
	command := adapter.Command(runtimetype.RunnerCommand(agent.RuntimeType, runtimetype.RunnerCLI), goal, model, prompt)
	ctx, span := telemetry.Start(ctx, "cli.run",
		attribute.String("agenthub.runtime.id", acquired.runtimeID),
		attribute.String("agenthub.cli.approval_mode", approvalMode(goal)))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "cli.started", "Runtime의 에이전트를 실행합니다.", map[string]any{
		"runtimeId": acquired.runtimeID, "approvalMode": approvalMode(goal),
		"maxTurns": goal.MaxSteps, "maxToolCalls": goal.MaxToolCalls, "maxDurationSeconds": goal.MaxDurationSeconds,
	})
	result, execErr := o.spawner.Exec(ctx, spec, appRuntime.ExecRequest{Command: command})
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, execErr)

	// The command line is recorded without the prompt: the prompt is the step's
	// input and repeating it here would double every task's record.
	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepCLI,
		Title: "에이전트 실행", Input: prompt, Status: "succeeded", DurationMs: elapsed,
	}
	run.StepCount = 1
	if execErr != nil {
		record.Status, record.Error = "failed", execErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("cli step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "cli.failed", execErr.Error(), map[string]any{"runtimeId": acquired.runtimeID})
		// Reaching the container failed, which is infrastructure rather than the
		// agent failing its task.
		return nil, Outcome{Status: store.TaskFailed, Failure: runtimeExecFailure("에이전트", execErr, goal), Retryable: true}
	}

	parsed, parseErr := adapter.Parse(result.Stdout, result.Stderr, result.ExitCode)
	// Scanned before it is written down: what the scanner refuses must not reach
	// this platform's own database on its way to being refused.
	inspected, inspectErr := o.inspectAnswer(ctx, step, parsed.Result)
	record.Output = inspected
	if inspectErr != nil {
		record.Output = ""
	}
	// On the step, not only on the run: the usage report adds up steps, so tokens
	// recorded only on the run are spend no report can see — on a run that says it
	// was metered.
	record.PromptTokens, record.CompletionTokens = parsed.InputTokens, parsed.OutputTokens
	if parseErr != nil {
		record.Status, record.Error = "failed", parseErr.Error()
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("cli step could not be recorded", "run", run.ID, "error", storeErr)
	}
	// Unlike a flow, this reports real usage, so it is metered like any other work
	// — when the agent actually filled the field. An agent that ran and reported
	// nothing leaves the run unmetered rather than looking free.
	run.TotalTokens += parsed.TotalTokens
	if parsed.TotalTokens > 0 {
		run.Metering = store.MeteringAgent
	} else {
		run.Metering = store.MeteringUnmetered
	}

	if parseErr != nil {
		o.event(ctx, *run, "cli.failed", parseErr.Error(), map[string]any{
			"exitCode": result.ExitCode, "runtimeId": acquired.runtimeID,
		})
		return nil, Outcome{Status: store.TaskFailed, Failure: parseErr.Error(), Retryable: adapter.Retryable(result.ExitCode)}
	}

	if inspectErr != nil {
		return nil, Outcome{Status: store.TaskFailed, Failure: inspectErr.Error(),
			Retryable: !errors.Is(inspectErr, workflow.ErrBlocked)}
	}
	answer := inspected

	o.event(ctx, *run, "cli.completed", "에이전트 실행이 끝났습니다.", map[string]any{
		"durationMs": elapsed, "turns": parsed.Turns, "toolCalls": parsed.ToolCalls,
		"linesAdded": parsed.LinesAdded, "linesRemoved": parsed.LinesRemoved,
		"totalTokens": parsed.TotalTokens, "sessionId": parsed.SessionID,
	})
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// cliApprovalMode is the mode this Goal runs with, defaulting to the runtime's
// own default rather than to the one that changes files without asking.
func approvalMode(goal store.AgentGoal) string {
	for _, mode := range ApprovalModes {
		if goal.ApprovalMode == mode {
			return mode
		}
	}
	return "default"
}

// qwenCodeCLI drives Qwen Code, and the JupyterLab image that ships the same
// agent beside the notebooks.
type qwenCodeCLI struct{}

// Command builds argv. The Goal's guardrails become the agent's own budgets, so a
// limit set in the console is enforced by the thing doing the work rather than by
// a timeout that kills it half way.
func (qwenCodeCLI) Command(base []string, goal store.AgentGoal, model resolvedModel, prompt string) []string {
	command := append(append([]string{}, base...),
		"-p", prompt, "--approval-mode", approvalMode(goal), "--output-format", "json")
	if goal.MaxSteps > 0 {
		command = append(command, "--max-session-turns", strconv.Itoa(goal.MaxSteps))
	}
	if goal.MaxToolCalls > 0 {
		command = append(command, "--max-tool-calls", strconv.Itoa(goal.MaxToolCalls))
	}
	if goal.MaxDurationSeconds > 0 {
		// A little under the platform's own deadline, so the agent stops itself and
		// reports why instead of being cut off mid-sentence.
		budget := goal.MaxDurationSeconds - 10
		if budget < 30 {
			budget = goal.MaxDurationSeconds
		}
		command = append(command, "--max-wall-time", strconv.Itoa(budget)+"s")
	}
	if model.ModelName != "" {
		command = append(command, "-m", model.ModelName)
	}
	return command
}

func (qwenCodeCLI) Parse(stdout, stderr string, exitCode int) (cliRun, error) {
	return parseCLIRun(stdout, stderr, exitCode)
}

func (qwenCodeCLI) Retryable(exitCode int) bool { return retryableCLIExit(exitCode) }

// cliRun is what one headless run produced.
type cliRun struct {
	Result       string
	SessionID    string
	Turns        int
	ToolCalls    int
	LinesAdded   int
	LinesRemoved int
	TotalTokens  int
	InputTokens  int
	OutputTokens int
}

// cliJSON is the answer's shape, reduced to what the platform reads. The agent
// emits an array: a system message, the assistant's messages, and one result.
type cliJSON struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	NumTurns  int    `json:"num_turns"`
	Usage     struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Stats struct {
		Tools struct {
			TotalCalls int `json:"totalCalls"`
		} `json:"tools"`
		Files struct {
			LinesAdded   int `json:"totalLinesAdded"`
			LinesRemoved int `json:"totalLinesRemoved"`
		} `json:"files"`
	} `json:"stats"`
}

// cliError is the object the agent writes to stderr when a guardrail stopped it.
// It is on stderr rather than in the answer because there is no answer: stdout is
// empty, and without reading this the run would be recorded as "no output".
type cliError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// parseCLIRun turns one execution into either an answer or a failure a person can
// act on.
func parseCLIRun(stdout, stderr string, exitCode int) (cliRun, error) {
	var messages []cliJSON
	decodeErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &messages)

	var result *cliJSON
	for index := range messages {
		if messages[index].Type == "result" {
			result = &messages[index]
		}
	}

	if exitCode != 0 || result == nil || result.IsError {
		return cliRun{}, cliFailure(exitCode, result, stderr, decodeErr)
	}
	run := cliRun{
		Result: result.Result, SessionID: result.SessionID, Turns: result.NumTurns,
		ToolCalls:  result.Stats.Tools.TotalCalls,
		LinesAdded: result.Stats.Files.LinesAdded, LinesRemoved: result.Stats.Files.LinesRemoved,
		TotalTokens: result.Usage.TotalTokens, InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
	}
	if strings.TrimSpace(run.Result) == "" {
		return run, errors.New("에이전트가 실행됐지만 결과 메시지가 비어 있습니다")
	}
	// An agent that could not reach the model writes the failure where its answer
	// belongs and still reports success: exit 0, is_error false, and the whole
	// answer is `[API Error: ...]`. The task was then recorded as completed, with
	// the error as its result — which is a run that did nothing wearing the badge
	// of one that worked.
	//
	// Observed against a real runtime: the agent answered "[API Error: Streaming
	// request received a non-SSE response …]" and the task said completed.
	if failure := cliAnswerIsFailure(run.Result); failure != "" {
		return run, errors.New(failure)
	}
	return run, nil
}

// cliAnswerIsFailure reports the agent's own error when the answer is one.
//
// Only when the answer *is* the report, never when it mentions one: an agent
// asked to explain a log may quote the same words, and refusing that run would
// be the opposite mistake.
func cliAnswerIsFailure(result string) string {
	trimmed := strings.TrimSpace(result)
	if !strings.HasPrefix(trimmed, "[API Error:") {
		return ""
	}
	return "에이전트가 모델을 부르지 못했습니다: " + firstLine(trimmed)
}

// cliFailure explains what stopped the run, preferring the agent's own words.
func cliFailure(exitCode int, result *cliJSON, stderr string, decodeErr error) error {
	detail := cliStderrMessage(stderr)
	switch exitCode {
	case cliExitTurnLimit:
		return fmt.Errorf("에이전트가 최대 단계 수에 도달해 중단했습니다%s", cliDetailSuffix(detail))
	case cliExitBudget:
		return fmt.Errorf("에이전트가 실행 예산(시간 또는 도구 호출)을 초과해 중단했습니다%s", cliDetailSuffix(detail))
	case cliExitInterrupt:
		return errors.New("에이전트 실행이 중단되었습니다")
	}
	if killed := killedContainer(exitCode); killed != "" {
		return fmt.Errorf("에이전트가 %s%s", killed, cliDetailSuffix(detail))
	}
	if result != nil && result.IsError {
		return fmt.Errorf("에이전트가 오류로 끝났습니다: %s", firstLine(result.Result))
	}
	if detail != "" {
		return fmt.Errorf("에이전트 실행이 실패했습니다(종료 코드 %d): %s", exitCode, detail)
	}
	if decodeErr != nil {
		return fmt.Errorf("에이전트 출력을 읽지 못했습니다(종료 코드 %d)", exitCode)
	}
	return fmt.Errorf("에이전트 실행이 실패했습니다(종료 코드 %d)", exitCode)
}

func cliDetailSuffix(detail string) string {
	if detail == "" {
		return "."
	}
	return ": " + detail
}

// cliStderrMessage reads the structured error the agent writes, and falls back to
// the last line of whatever it said — warnings about MCP servers that would not
// start live here too, and they are usually the reason.
func cliStderrMessage(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	if start := strings.Index(trimmed, "{"); start >= 0 {
		var failure cliError
		if err := json.Unmarshal([]byte(trimmed[start:]), &failure); err == nil && failure.Error.Message != "" {
			return failure.Error.Message
		}
	}
	lines := strings.Split(trimmed, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 400 {
		last = last[:400] + "…"
	}
	return last
}

// retryableCLIExit decides whether another attempt could end differently.
//
// A guardrail is not worth retrying: the same task with the same limits stops in
// the same place, and the answer is to raise the limit or narrow the task.
// Anything else is treated as a bad moment in the runtime.
func retryableCLIExit(exitCode int) bool {
	switch exitCode {
	case cliExitTurnLimit, cliExitBudget, cliExitInterrupt:
		return false
	}
	return true
}
