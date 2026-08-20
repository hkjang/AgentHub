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
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Running a task as an investigation.
//
// The other backends answer a question. This one answers it and hands back what
// it looked at: every query it ran against metrics, logs, alerts and runbooks,
// with the result of each. That difference is the whole reason it is a backend
// rather than a prompt — an investigation whose evidence cannot be checked is an
// opinion, and an opinion about why production broke is worth very little at
// three in the morning.
//
// So the conclusion becomes the run's answer, judged by the same evaluator as
// any other, and every tool call behind it becomes a step on the run's timeline.
// A person reading the run afterwards can see which query produced the number
// the conclusion rests on, and whether it succeeded.

// holmesBinary is the wrapper the runtime image ships. It exists because an exec
// has no shell and no working directory, and because the agent renders its
// answer for a person while writing the machine-readable record to a file: the
// wrapper makes stdout carry that record and nothing else.
const holmesBinary = "/usr/local/bin/agenthub-holmes-run"

// investigateEvidenceLimit bounds how much of one tool's result is stored. A
// query against a busy cluster can return megabytes, and the run record is not
// where that belongs — the point of keeping evidence is that somebody can see
// what was asked and whether it answered, not to copy the observability stack
// into Postgres.
const investigateEvidenceLimit = 4000

// runInvestigate executes one investigation and returns its conclusion as the
// transcript, which the evaluator then judges like any other.
func (o *Orchestrator) runInvestigate(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "조사를 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	step := workflow.Step{ID: "investigate", AgentID: agent.ID, AgentName: agent.Name}
	question := runnerInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, question)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		question = scanned
	}

	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime을 읽지 못했습니다: " + err.Error()}
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	ctx, span := telemetry.Start(ctx, "investigate.run",
		attribute.String("agenthub.runtime.id", acquired.runtimeID),
		attribute.String("agenthub.investigate.shell", investigateShellPolicy(goal)))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "investigate.started", "조사를 시작합니다.", map[string]any{
		"runtimeId": acquired.runtimeID, "maxSteps": goal.MaxSteps,
		"shell": investigateShellPolicy(goal),
	})
	result, execErr := o.spawner.Exec(ctx, spec, appRuntime.ExecRequest{Command: investigateCommand(goal, model, question)})
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, execErr)

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepInvestigate,
		Title: "조사", Input: question, Status: "succeeded", DurationMs: elapsed,
	}
	run.StepCount = 1
	if execErr != nil {
		record.Status, record.Error = "failed", execErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("investigate step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "investigate.failed", execErr.Error(), map[string]any{"runtimeId": acquired.runtimeID})
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "Runtime에서 조사를 실행하지 못했습니다: " + execErr.Error()}
	}

	report, parseErr := parseInvestigation(result.Stdout, result.Stderr, result.ExitCode)
	record.Output = report.Conclusion
	record.PromptTokens, record.CompletionTokens = report.PromptTokens, report.CompletionTokens
	if parseErr != nil {
		record.Status, record.Error = "failed", parseErr.Error()
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("investigate step could not be recorded", "run", run.ID, "error", storeErr)
	}
	// Real usage, reported by the agent itself, so an investigation is metered
	// like any other work.
	run.TotalTokens += report.TotalTokens
	run.ToolCalls += len(report.Evidence)
	o.recordEvidence(ctx, run, report)

	if parseErr != nil {
		o.event(ctx, *run, "investigate.failed", parseErr.Error(), map[string]any{
			"exitCode": result.ExitCode, "runtimeId": acquired.runtimeID,
		})
		return nil, Outcome{Status: store.TaskFailed, Failure: parseErr.Error(), Retryable: true}
	}

	answer := report.Conclusion
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}

	o.event(ctx, *run, "investigate.completed", "조사가 끝났습니다.", map[string]any{
		"durationMs": elapsed, "evidence": len(report.Evidence), "failedEvidence": report.failedEvidence(),
		"llmCalls": report.LLMCalls, "totalTokens": report.TotalTokens, "toolsets": report.toolsets(),
	})
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// investigateShellPolicy decides whether the investigator may run shell commands
// while it looks around.
//
// It reuses the Goal's approval mode, because it is the same question every
// other in-Pod backend asks — how much may this run change or reach — and a
// second setting would only be a second place to get it wrong. Reading metrics
// and logs is what an investigation is; running arbitrary commands to find out
// more is a different kind of act, and it stays behind the modes that were
// chosen deliberately.
func investigateShellPolicy(goal store.AgentGoal) string {
	switch cliApprovalMode(goal) {
	case "auto", "yolo":
		return "allow"
	}
	return "deny"
}

// investigateCommand builds argv. The Goal's step limit becomes the agent's own,
// so a limit set in the console is enforced by the thing doing the work.
func investigateCommand(goal store.AgentGoal, model resolvedModel, question string) []string {
	command := []string{holmesBinary}
	if investigateShellPolicy(goal) == "allow" {
		command = append(command, "--bash-always-allow")
	} else {
		command = append(command, "--bash-always-deny")
	}
	if goal.MaxSteps > 0 {
		command = append(command, "--max-steps", strconv.Itoa(goal.MaxSteps))
	}
	if model.ModelName != "" {
		// The provider prefix is how this agent's model client is told which
		// protocol to speak; the endpoint itself comes from the generated
		// configuration, which points at the platform's gateway.
		command = append(command, "--model", "openai/"+model.ModelName)
	}
	// Last, and after every flag: the question is a positional argument, and a
	// question that begins with a dash must not become one.
	return append(command, "--", question)
}

// investigation is what one run of the investigator produced.
type investigation struct {
	Conclusion       string
	Evidence         []evidence
	LLMCalls         int
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
}

// evidence is one thing the investigator looked at.
type evidence struct {
	Tool        string
	Toolset     string
	Description string
	Status      string
	Data        string
}

func (i investigation) failedEvidence() int {
	failed := 0
	for _, item := range i.Evidence {
		if item.Status != "" && item.Status != "success" {
			failed++
		}
	}
	return failed
}

func (i investigation) toolsets() []string {
	seen := map[string]bool{}
	names := []string{}
	for _, item := range i.Evidence {
		if item.Toolset == "" || seen[item.Toolset] {
			continue
		}
		seen[item.Toolset] = true
		names = append(names, item.Toolset)
	}
	return names
}

// investigationJSON is the record the agent writes, reduced to what the platform
// reads. Fields it writes that are not named here are ignored rather than
// rejected: the record grows, and an adapter that failed on an unknown field
// would break on the next release of something it does not control.
type investigationJSON struct {
	Result           string `json:"result"`
	NumLLMCalls      int    `json:"num_llm_calls"`
	TotalTokens      int    `json:"total_tokens"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ToolCalls        []struct {
		ToolName    string `json:"tool_name"`
		Description string `json:"description"`
		ToolsetName string `json:"toolset_name"`
		Result      struct {
			Status string          `json:"status"`
			Error  string          `json:"error"`
			Data   json.RawMessage `json:"data"`
		} `json:"result"`
	} `json:"tool_calls"`
}

// parseInvestigation turns one execution into either a conclusion with its
// evidence, or a failure a person can act on.
func parseInvestigation(stdout, stderr string, exitCode int) (investigation, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return investigation{}, investigationFailure(exitCode, stderr)
	}
	var record investigationJSON
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		// The wrapper's whole job is to put the record on stdout by itself, so
		// something other than the record arriving there is worth saying plainly
		// rather than reporting as "no output".
		return investigation{}, fmt.Errorf("조사 결과를 읽지 못했습니다(종료 코드 %d)%s", exitCode, investigateDetail(stderr))
	}

	report := investigation{
		Conclusion: strings.TrimSpace(record.Result), LLMCalls: record.NumLLMCalls,
		TotalTokens: record.TotalTokens, PromptTokens: record.PromptTokens, CompletionTokens: record.CompletionTokens,
	}
	for _, call := range record.ToolCalls {
		item := evidence{
			Tool: call.ToolName, Toolset: call.ToolsetName,
			Description: call.Description, Status: call.Result.Status,
			Data: evidenceData(call.Result.Data),
		}
		if item.Status == "" && call.Result.Error != "" {
			item.Status = "error"
		}
		// The error goes on last, after the data has been read into its final
		// shape. It used to go on first and be overwritten by the unwrap below,
		// which left a failed query on the timeline with nothing saying why.
		if call.Result.Error != "" {
			item.Data = strings.TrimSpace(call.Result.Error + "\n" + item.Data)
		}
		if len(item.Data) > investigateEvidenceLimit {
			item.Data = item.Data[:investigateEvidenceLimit] + "\n…(잘림)"
		}
		report.Evidence = append(report.Evidence, item)
	}
	if report.Conclusion == "" {
		return report, errors.New("조사가 끝났지만 결론이 비어 있습니다")
	}
	return report, nil
}

// evidenceData reads one tool's result into text. Tools return a JSON string of
// their output, an object, or nothing at all; a quoted string is unwrapped so the
// evidence reads as what the tool returned rather than as an escaped blob, and a
// null is nothing rather than the word "null".
func evidenceData(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return trimmed
}

// investigationFailure explains an execution that produced no record at all.
func investigationFailure(exitCode int, stderr string) error {
	if detail := investigateDetail(stderr); detail != "" {
		return fmt.Errorf("조사를 마치지 못했습니다(종료 코드 %d)%s", exitCode, detail)
	}
	return fmt.Errorf("조사를 마치지 못했습니다(종료 코드 %d). Runtime 로그를 확인해 주세요", exitCode)
}

// investigateDetail is why the run failed, in the agent's own words.
//
// Reading the last line is not enough: this agent renders a failure as a drawn
// box, so the last line is the bottom border and the line above it is a frame of
// source code. Taking that literally produced "조사를 마치지 못했습니다(종료 코드
// 1): ╰──────╯", which tells a person nothing at all while the actual reason —
// a configuration file it could not parse — sat a few lines up.
//
// So the search runs backwards for a line that carries a message rather than a
// frame, preferring one that names an error.
func investigateDetail(stderr string) string {
	lines := strings.Split(stderr, "\n")
	fallback := ""
	for index := len(lines) - 1; index >= 0; index-- {
		line := undecorate(lines[index])
		if line == "" {
			continue
		}
		if fallback == "" {
			fallback = line
		}
		// Python and its libraries name the failure this way, and it is the line a
		// person would point at if they were reading the whole traceback.
		if strings.Contains(line, "Error") || strings.Contains(line, "Exception") {
			return ": " + clip(line)
		}
	}
	if fallback == "" {
		return ""
	}
	return ": " + clip(fallback)
}

// undecorate strips the box drawing a rendered traceback is wrapped in, and
// reports an empty string for a line that was nothing but decoration.
func undecorate(line string) string {
	stripped := strings.Map(func(r rune) rune {
		// The box drawing block, which is all this agent uses to frame its output.
		if r >= 0x2500 && r <= 0x257F {
			return -1
		}
		return r
	}, line)
	return strings.TrimSpace(stripped)
}

func clip(text string) string {
	runes := []rune(text)
	if len(runes) <= 400 {
		return text
	}
	return string(runes[:400]) + "…"
}

// recordEvidence writes each thing the investigator looked at as its own step,
// after the investigation's own step, so the run reads in the order it happened.
func (o *Orchestrator) recordEvidence(ctx context.Context, run *store.AgentRun, report investigation) {
	for index, item := range report.Evidence {
		status := "succeeded"
		if item.Status != "" && item.Status != "success" {
			status = "failed"
		}
		title := strings.TrimSpace(item.Description)
		if title == "" {
			title = item.Tool
		}
		record := store.AgentRunStep{
			RunID: run.ID, Sequence: index + 2, Type: store.StepTool,
			Title: title, Input: item.Tool, Output: item.Data, Status: status,
		}
		if status == "failed" {
			record.Error = item.Status
		}
		if _, err := o.store.AppendRunStep(ctx, record); err != nil {
			o.logger.Error("investigation evidence could not be recorded", "run", run.ID, "error", err)
			return
		}
		run.StepCount++
	}
}
