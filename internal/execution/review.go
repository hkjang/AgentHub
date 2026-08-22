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

// Reviewing code, as its own backend.
//
// Every other runner ends with an answer and hands it to the evaluator. This one
// ends with a list of observations that each point at a file and a line and
// carry a severity, and the useful thing to do with that is not to summarise it.
// A console can show eleven findings worst-first, let somebody accept or dismiss
// each, and fail the task when something serious is still open. It cannot do any
// of that with a paragraph.
//
// The engine does the parts that should not be guessed. It reads the diff and
// decides which files are in scope, resolves which rules apply to each, and
// locates every comment on a real line by matching the code the model quoted
// against the diff rather than trusting a model to count lines. AgentHub decides
// what to compare, who may see the result, which model answers, and what happens
// when the answer is bad.

// reviewResult is the engine's JSON document. Only the fields the platform
// stores are named: an engine that adds a field must not break a review, and one
// that removes a field the platform relies on has to fail loudly, which is what
// the checks after parsing are for.
type reviewResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	LLM     struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"llm"`
	Summary struct {
		FilesReviewed int `json:"files_reviewed"`
		Comments      int `json:"comments"`
		TotalTokens   int `json:"total_tokens"`
		InputTokens   int `json:"input_tokens"`
		OutputTokens  int `json:"output_tokens"`
	} `json:"summary"`
	Comments  []reviewComment `json:"comments"`
	SessionID string          `json:"session_id"`
	Manifest  struct {
		SchemaVersion string `json:"schema_version"`
		TerminalState string `json:"terminal_state"`
		Input         struct {
			Mode          string `json:"mode"`
			RequestedFrom string `json:"requested_from"`
			RequestedHead string `json:"requested_head"`
			ResolvedBase  string `json:"resolved_base"`
			ResolvedHead  string `json:"resolved_head"`
		} `json:"input"`
		Execution struct {
			OCRVersion string `json:"ocr_version"`
		} `json:"execution"`
		Coverage struct {
			Selected []reviewItem `json:"selected"`
			Failed   []reviewItem `json:"failed"`
		} `json:"coverage"`
	} `json:"manifest"`
}

type reviewItem struct {
	Path           string `json:"path"`
	Classification string `json:"classification"`
}

type reviewComment struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code"`
	ExistingCode   string `json:"existing_code"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
}

// reviewCommand builds the engine's argv from the Goal.
//
// The Goal says what to compare and the engine is told nothing else about how to
// review — the rules are the engine's, and a platform that also had opinions
// about them would be two sets of rules disagreeing in production. The JSON
// format and the agent audience are not options: they are how a program reads
// this, and a review whose output was meant for a human terminal cannot become
// findings.
func reviewCommand(base []string, goal store.AgentGoal) []string {
	command := append([]string(nil), base...)
	switch goal.ReviewMode {
	case "range":
		command = append(command, "review", "--from", goal.ReviewBaseRef, "--to", goal.ReviewHeadRef)
	case "commit":
		command = append(command, "review", "--commit", goal.ReviewHeadRef)
	case "scan":
		command = append(command, "scan")
		if path := strings.TrimSpace(goal.ReviewPath); path != "" {
			command = append(command, "--path", path)
		}
	default:
		command = append(command, "review")
	}
	if exclude := strings.TrimSpace(goal.ReviewExclude); exclude != "" {
		command = append(command, "--exclude", exclude)
	}
	// The Goal's own guardrails become the engine's budgets, so a limit somebody
	// set in the console is enforced by the thing doing the work rather than by a
	// timeout that kills it half way through.
	if goal.TokenBudget > 0 {
		command = append(command, "--max-tokens-budget", strconv.FormatInt(goal.TokenBudget, 10))
	}
	if goal.MaxDurationSeconds > 0 {
		minutes := goal.MaxDurationSeconds / 60
		if minutes < 1 {
			minutes = 1
		}
		command = append(command, "--timeout", strconv.Itoa(minutes))
	}
	return append(command, "--format", "json", "--audience", "agent")
}

// runReview reviews the workspace and keeps what it found.
func (o *Orchestrator) runReview(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "리뷰를 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	command := runtimetype.RunnerCommand(agent.RuntimeType, runtimetype.RunnerReview)
	if len(command) == 0 {
		return nil, Outcome{Status: store.TaskFailed,
			Failure: runtimetype.Describe(agent.RuntimeType).Label + " 런타임은 코드 리뷰를 실행할 수 없습니다."}
	}
	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime을 읽지 못했습니다: " + err.Error()}
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	argv := reviewCommand(command, goal)
	ctx, span := telemetry.Start(ctx, "review.run",
		attribute.String("agenthub.runtime.id", acquired.runtimeID),
		attribute.String("agenthub.review.mode", reviewMode(goal)))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "review.started", "코드 리뷰를 실행합니다.", map[string]any{
		"runtimeId": acquired.runtimeID, "mode": reviewMode(goal),
		"baseRef": goal.ReviewBaseRef, "headRef": goal.ReviewHeadRef,
	})
	result, execErr := o.spawner.Exec(ctx, spec, appRuntime.ExecRequest{Command: argv})
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, execErr)

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepReview,
		Title: "코드 리뷰", Input: reviewTarget(goal), Status: "succeeded", DurationMs: elapsed,
	}
	run.StepCount = 1
	if execErr != nil {
		record.Status, record.Error = "failed", execErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "review.failed", execErr.Error(), map[string]any{"runtimeId": acquired.runtimeID})
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "Runtime에서 리뷰 엔진을 실행하지 못했습니다: " + execErr.Error()}
	}

	parsed, parseErr := parseReview(result.Stdout, result.Stderr)
	if parseErr != nil {
		record.Status, record.Error = "failed", parseErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "review.failed", parseErr.Error(), map[string]any{"exitCode": result.ExitCode})
		return nil, Outcome{Status: store.TaskFailed, Failure: parseErr.Error()}
	}

	// A review reports real usage, so it is metered like any other work. A run
	// that reported nothing is left unmetered rather than looking free.
	run.TotalTokens += parsed.Summary.TotalTokens
	if parsed.Summary.TotalTokens > 0 {
		run.Metering = store.MeteringAgent
	} else {
		run.Metering = store.MeteringUnmetered
	}

	findings := make([]store.ReviewFinding, 0, len(parsed.Comments))
	for _, comment := range parsed.Comments {
		findings = append(findings, store.ReviewFinding{
			RunID: run.ID, TaskID: task.ID, AgentID: agent.ID, OwnerID: agent.OwnerID,
			FilePath: comment.Path, StartLine: comment.StartLine, EndLine: comment.EndLine,
			Severity: strings.ToLower(comment.Severity), Category: strings.ToLower(comment.Category),
			Message: comment.Content, ExistingCode: comment.ExistingCode, Suggestion: comment.SuggestionCode,
			Source: "open-code-review",
		})
	}
	// The coverage goes in first. A review with no findings and a review that
	// read nothing both produce an empty list, and they mean opposite things —
	// so what was covered is recorded even when saving the findings fails.
	coverage := store.ReviewRun{
		RunID: run.ID, Mode: parsed.Manifest.Input.Mode, BaseRef: parsed.Manifest.Input.RequestedFrom,
		HeadRef: parsed.Manifest.Input.RequestedHead, ResolvedBase: parsed.Manifest.Input.ResolvedBase,
		ResolvedHead:  parsed.Manifest.Input.ResolvedHead,
		FilesSelected: len(parsed.Manifest.Coverage.Selected), FilesReviewed: parsed.Summary.FilesReviewed,
		FilesFailed: len(parsed.Manifest.Coverage.Failed), SessionID: parsed.SessionID,
		EngineVersion: parsed.Manifest.Execution.OCRVersion, Status: parsed.Status,
	}
	if coverage.Mode == "" {
		coverage.Mode = reviewMode(goal)
	}
	if err := o.store.SaveReviewRun(ctx, coverage); err != nil {
		o.logger.Error("review coverage could not be recorded", "run", run.ID, "error", err)
	}
	if err := o.store.SaveReviewFindings(ctx, findings); err != nil {
		record.Status, record.Error = "failed", err.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
		}
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "리뷰 결과를 저장하지 못했습니다: " + err.Error()}
	}

	summary := reviewSummary(parsed, findings)
	record.Output = summary
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
	}
	o.event(ctx, *run, "review.completed", summary, map[string]any{
		"durationMs": elapsed, "findings": len(findings),
		"filesReviewed": parsed.Summary.FilesReviewed, "filesFailed": len(parsed.Manifest.Coverage.Failed),
		"sessionId": parsed.SessionID, "totalTokens": parsed.Summary.TotalTokens,
	})

	// The gate. A review that finds something serious and reports success is a
	// gate nobody can rely on, so the Goal's floor decides the task's verdict —
	// and an empty floor means this deployment has not asked for a gate, which is
	// the state a deployment should have to choose its way out of.
	if blocking := blockingFindings(findings, goal.ReviewFailOn); len(blocking) > 0 {
		return []string{summary}, Outcome{
			Status:  store.TaskFailed,
			Failure: fmt.Sprintf("%s 이상 지적이 %d건 있습니다: %s", severityLabel(goal.ReviewFailOn), len(blocking), firstFindings(blocking, 3)),
		}
	}
	// A review whose files all failed found nothing because it read nothing.
	if parsed.Status != "complete" && len(findings) == 0 {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "리뷰가 끝나지 못했습니다: " + strings.TrimSpace(parsed.Message)}
	}
	return []string{summary}, Outcome{}
}

// parseReview reads the engine's document.
//
// The engine writes its JSON to stdout and its progress to stderr, and with
// --audience agent it writes the document to both. Taking stdout first and
// falling back keeps working if that changes; failing to find a document at all
// has to be an error rather than an empty review, because an empty review reads
// as "nothing wrong with this code".
func parseReview(stdout, stderr string) (reviewResult, error) {
	for _, candidate := range []string{stdout, stderr} {
		document := strings.TrimSpace(candidate)
		start := strings.Index(document, "{")
		if start < 0 {
			continue
		}
		// Decoded as one value rather than as the whole string: when every file
		// fails the engine prints its session id and a plain-text error after the
		// document, and Unmarshal refuses a document with anything after it. That
		// run is exactly the one whose JSON has to be read — it is what says the
		// review read nothing rather than found nothing.
		var parsed reviewResult
		if err := json.NewDecoder(strings.NewReader(document[start:])).Decode(&parsed); err != nil {
			continue
		}
		if parsed.Status == "" {
			continue
		}
		return parsed, nil
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if len(detail) > 400 {
		detail = detail[:400]
	}
	return reviewResult{}, errors.New("리뷰 결과를 읽지 못했습니다: " + detail)
}

// blockingFindings are the ones at or above the Goal's floor that nobody has
// dealt with yet. An empty floor blocks nothing.
func blockingFindings(findings []store.ReviewFinding, failOn string) []store.ReviewFinding {
	if strings.TrimSpace(failOn) == "" {
		return nil
	}
	blocking := []store.ReviewFinding{}
	for _, finding := range findings {
		if store.ReviewSeverityAtLeast(finding.Severity, failOn) {
			blocking = append(blocking, finding)
		}
	}
	return blocking
}

func reviewMode(goal store.AgentGoal) string {
	switch goal.ReviewMode {
	case "range", "commit", "scan":
		return goal.ReviewMode
	}
	return "workspace"
}

// reviewTarget describes what was reviewed, for the step's own record.
func reviewTarget(goal store.AgentGoal) string {
	switch reviewMode(goal) {
	case "range":
		return goal.ReviewBaseRef + " → " + goal.ReviewHeadRef
	case "commit":
		return "커밋 " + goal.ReviewHeadRef
	case "scan":
		if path := strings.TrimSpace(goal.ReviewPath); path != "" {
			return "전체 점검: " + path
		}
		return "저장소 전체 점검"
	}
	return "작업공간의 변경분"
}

// reviewSummary is the one line a person reads before opening the findings.
func reviewSummary(parsed reviewResult, findings []store.ReviewFinding) string {
	counts := map[string]int{}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	parts := []string{}
	for _, severity := range store.ReviewSeverities {
		if counts[severity] > 0 {
			parts = append(parts, severityLabel(severity)+" "+strconv.Itoa(counts[severity]))
		}
	}
	head := fmt.Sprintf("파일 %d개를 리뷰해 지적 %d건", parsed.Summary.FilesReviewed, len(findings))
	if len(parts) > 0 {
		head += " (" + strings.Join(parts, ", ") + ")"
	}
	if failed := len(parsed.Manifest.Coverage.Failed); failed > 0 {
		head += fmt.Sprintf(" — 파일 %d개는 리뷰하지 못했습니다", failed)
	}
	return head
}

func severityLabel(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "심각"
	case "high":
		return "높음"
	case "medium":
		return "보통"
	case "low":
		return "낮음"
	}
	return severity
}

// firstFindings names a few of them, because a verdict that says only "three
// blocking findings" sends somebody to another screen to learn what they are.
func firstFindings(findings []store.ReviewFinding, limit int) string {
	names := []string{}
	for index, finding := range findings {
		if index >= limit {
			names = append(names, fmt.Sprintf("외 %d건", len(findings)-limit))
			break
		}
		names = append(names, fmt.Sprintf("%s:%d", finding.FilePath, finding.StartLine))
	}
	return strings.Join(names, ", ")
}

// reviewInspection is where a review's text meets the content scanner. The
// findings carry code and a model's words about it, and both leave the platform
// when somebody publishes them.
func (o *Orchestrator) reviewInspection(ctx context.Context, step workflow.Step, text string) (string, error) {
	if o.flowInspector == nil {
		return text, nil
	}
	return o.flowInspector.Inbound(ctx, step, text)
}
