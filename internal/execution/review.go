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
	RetryReport retryReport `json:"retry_report"`
}

type reviewItem struct {
	Path           string `json:"path"`
	Classification string `json:"classification"`
	// Reason is the engine's own word for why this file was not reviewed. It is
	// declared in the answer, which is a better place to read it from than the
	// log the engine also prints.
	Reason string `json:"reason"`
}

// retryReport is what the engine records about the model calls it made. When a
// review reads nothing, this is where the reason lives: an authentication class
// and a 401 is a revoked key, and saying so beats "1 of 1 selected item(s)
// failed" by the distance between a fact and a count.
type retryReport struct {
	Requests []struct {
		Attempts []struct {
			ErrorClass   string `json:"error_class"`
			FailurePhase string `json:"failure_phase"`
			StatusCode   int    `json:"status_code"`
		} `json:"attempts"`
	} `json:"requests"`
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

// resolveReviewTargets settles what this run compares.
//
// Only a Goal in `trigger` mode looks at the task: a Goal that names its
// branches means them, and letting a payload quietly redirect it would make a
// scheduled review of main reviewable into something else by anyone who can
// reach the webhook.
func resolveReviewTargets(goal store.AgentGoal, task store.AgentTask) (store.AgentGoal, error) {
	if goal.ReviewMode != "trigger" {
		return goal, nil
	}
	from, to, commit := reviewTargetsFromTask(task.Input)
	switch {
	case commit != "":
		if !safeRef(commit) {
			return goal, errors.New("트리거가 보낸 커밋 이름을 쓸 수 없습니다: " + trimmed(commit, 60))
		}
		goal.ReviewMode, goal.ReviewHeadRef = "commit", commit
	case from != "" && to != "":
		if !safeRef(from) || !safeRef(to) {
			return goal, errors.New("트리거가 보낸 브랜치 이름을 쓸 수 없습니다: " + trimmed(from, 40) + " → " + trimmed(to, 40))
		}
		goal.ReviewMode, goal.ReviewBaseRef, goal.ReviewHeadRef = "range", from, to
	default:
		return goal, errors.New("트리거가 리뷰할 대상을 알려주지 않았습니다. 웹훅 본문에 {\"from\":\"main\",\"to\":\"feature/x\"} 또는 {\"commit\":\"<sha>\"} 를 담아 주세요(base/head, sha 도 받습니다).")
	}
	return goal, nil
}

// trimmed shortens a value for a message without letting a whole payload into it.
func trimmed(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}

// reviewTargetsFromTask reads the change a task asks to review.
//
// A Goal in `trigger` mode names no branches: the task does. A CI job posting to
// the webhook trigger controls the body it sends, so it says which change to
// review — which means the platform does not have to know GitHub's payload shape
// from GitLab's from Bitbucket's, and a site with an internal Git server is not
// waiting for an adapter that names it.
//
// The last JSON object in the task input wins, because the webhook trigger
// appends the delivered payload after the trigger's own instruction and the
// payload is the part that changes per delivery.
func reviewTargetsFromTask(input string) (from, to, commit string) {
	for _, candidate := range jsonObjects(input) {
		// A forge's own body first: every one of them already says which branches
		// a pull request compares, and asking a site to translate that into two
		// fields is asking them to run a job whose only purpose is renaming.
		if forgeFrom, forgeTo, forgeCommit := scmTargets(candidate); forgeFrom != "" || forgeCommit != "" {
			from, to, commit = forgeFrom, forgeTo, forgeCommit
			continue
		}
		var payload struct {
			From   string `json:"from"`
			To     string `json:"to"`
			Base   string `json:"base"`
			Head   string `json:"head"`
			Commit string `json:"commit"`
			SHA    string `json:"sha"`
		}
		if err := json.Unmarshal([]byte(candidate), &payload); err != nil {
			continue
		}
		// base/head are what most forges call them; from/to are what the review
		// engine calls them. Both are accepted so a CI job can use either word
		// without the platform being clever about which one it meant.
		if payload.From != "" || payload.Base != "" {
			from = firstNonEmpty(payload.From, payload.Base)
		}
		if payload.To != "" || payload.Head != "" {
			to = firstNonEmpty(payload.To, payload.Head)
		}
		if payload.Commit != "" || payload.SHA != "" {
			commit = firstNonEmpty(payload.Commit, payload.SHA)
		}
	}
	return from, to, commit
}

// jsonObjects finds the top-level JSON objects in a block of text, in order.
//
// The task input is prose with a payload appended, not a document, so this reads
// what is there rather than requiring the whole input to be JSON.
func jsonObjects(input string) []string {
	found := []string{}
	depth, start, inString, escaped := 0, -1, false, false
	for index, char := range input {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case inString:
		case char == '{':
			if depth == 0 {
				start = index
			}
			depth++
		case char == '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					found = append(found, input[start:index+1])
					start = -1
				}
			}
		}
	}
	return found
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// safeRef refuses what cannot be a git ref.
//
// The command is executed as argv rather than through a shell, so this is not
// what stops an injection. It is what stops a payload field that happens to
// contain a sentence from becoming a review that failed for reasons nobody can
// read — and this input arrives from outside the platform, where the Goal's own
// refs came from a person at a form.
func safeRef(ref string) bool {
	if ref == "" || len(ref) > 200 {
		return false
	}
	return !strings.ContainsAny(ref, " \t\n\"'`$;|&<>\\")
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

	// A `trigger` Goal takes its refs from the task, so resolve them before the
	// command is built and fail here — naming what the payload must carry —
	// rather than reviewing the workspace and calling it a pull request.
	goal, targetErr := resolveReviewTargets(goal, task)
	if targetErr != nil {
		return nil, Outcome{Status: store.TaskFailed, Failure: targetErr.Error()}
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

	// On the step as well as the run: the usage report adds up steps, so tokens
	// kept only on the run are spend no report can see — on a run that says it was
	// metered.
	record.PromptTokens, record.CompletionTokens = parsed.Summary.InputTokens, parsed.Summary.OutputTokens

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
	// The files this run actually read, which is what resolution rests on. A
	// selected file that failed was not read, so it is not in this list.
	reviewed := reviewedPaths(parsed)
	coverage := store.ReviewRun{
		RunID: run.ID, Mode: parsed.Manifest.Input.Mode, BaseRef: parsed.Manifest.Input.RequestedFrom,
		HeadRef: parsed.Manifest.Input.RequestedHead, ResolvedBase: parsed.Manifest.Input.ResolvedBase,
		ResolvedHead:  parsed.Manifest.Input.ResolvedHead,
		FilesSelected: len(parsed.Manifest.Coverage.Selected), FilesReviewed: parsed.Summary.FilesReviewed,
		FilesFailed: len(parsed.Manifest.Coverage.Failed), SessionID: parsed.SessionID,
		EngineVersion: parsed.Manifest.Execution.OCRVersion, Status: parsed.Status, ReviewedPaths: reviewed,
	}
	if coverage.Mode == "" {
		coverage.Mode = reviewMode(goal)
	}
	if err := o.store.SaveReviewRun(ctx, coverage); err != nil {
		o.logger.Error("review coverage could not be recorded", "run", run.ID, "error", err)
	}
	for index := range findings {
		findings[index].Fingerprint = store.ReviewFingerprint(findings[index])
	}
	if err := o.store.SaveReviewFindings(ctx, findings); err != nil {
		record.Status, record.Error = "failed", err.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
		}
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "리뷰 결과를 저장하지 못했습니다: " + err.Error()}
	}

	// What this review read and no longer reports is fixed. It is the one place
	// the platform may say so without a person: a review that opened the file and
	// did not raise the finding is evidence, where a file nobody read is not.
	seen := make([]string, 0, len(findings))
	for index := range findings {
		seen = append(seen, findings[index].Fingerprint)
	}
	resolved, resolveErr := o.store.ResolveMissingFindings(ctx, agent.ID, run.ID, reviewed, seen)
	if resolveErr != nil {
		o.logger.Error("earlier findings could not be resolved", "run", run.ID, "error", resolveErr)
	}

	summary := reviewSummary(parsed, findings)
	if resolved > 0 {
		summary += fmt.Sprintf(" — 이전 지적 %d건은 더 이상 보고되지 않아 해결로 표시했습니다", resolved)
	}
	record.Output = summary
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("review step could not be recorded", "run", run.ID, "error", storeErr)
	}
	o.event(ctx, *run, "review.completed", summary, map[string]any{
		"durationMs": elapsed, "findings": len(findings),
		"filesReviewed": parsed.Summary.FilesReviewed, "filesFailed": len(parsed.Manifest.Coverage.Failed),
		"sessionId": parsed.SessionID, "totalTokens": parsed.Summary.TotalTokens, "resolved": resolved,
	})

	// And on the page the request came from, if its owner has stored a credential
	// for that host. A review nobody reads is a review that did not happen.
	o.announceReview(ctx, *run, task, agent.OwnerID, summary, findings)

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
	//
	// The engine's own summary says how many failed and not one word about why,
	// while the line above it on the same stream says exactly why — a refused
	// model credential, a timeout, a file it could not read. Reporting the count
	// alone sent somebody to look at the platform when their key had been
	// revoked.
	if parsed.Status != "complete" && len(findings) == 0 {
		failure := "리뷰가 끝나지 못했습니다: " + strings.TrimSpace(parsed.Message)
		if reason := reviewItemFailure(parsed); reason != "" {
			failure += " — " + reason
		}
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: failure}
	}
	return []string{summary}, Outcome{}
}

// reviewedPaths are the files this run actually read.
//
// A selected file that failed was not read. Counting it as read would let a file
// the engine could not open close every finding in it, which looks exactly like
// a morning's good work and is the opposite of one.
func reviewedPaths(parsed reviewResult) []string {
	failed := map[string]bool{}
	for _, item := range parsed.Manifest.Coverage.Failed {
		failed[item.Path] = true
	}
	paths := []string{}
	for _, item := range parsed.Manifest.Coverage.Selected {
		if !failed[item.Path] {
			paths = append(paths, item.Path)
		}
	}
	return paths
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
	case "range", "commit", "scan", "trigger":
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
	case "trigger":
		return "트리거가 지정한 변경분"
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

// reviewItemFailure says why the files were not reviewed, in the engine's terms.
//
// The engine's summary counts what failed and explains nothing; the answer it
// returns alongside carries both the reason it assigned each file and the class
// of the model call that failed. A revoked key reads as "authentication (401)",
// which is a different afternoon from "1 of 1 selected item(s) failed".
func reviewItemFailure(parsed reviewResult) string {
	reason := ""
	for _, item := range parsed.Manifest.Coverage.Failed {
		if trimmed := strings.TrimSpace(item.Reason); trimmed != "" {
			reason = trimmed
			break
		}
	}
	call := ""
	for _, request := range parsed.RetryReport.Requests {
		for _, attempt := range request.Attempts {
			class := strings.TrimSpace(attempt.ErrorClass)
			if class == "" && attempt.StatusCode == 0 {
				continue
			}
			switch {
			case class != "" && attempt.StatusCode > 0:
				call = fmt.Sprintf("%s (%d)", class, attempt.StatusCode)
			case class != "":
				call = class
			default:
				call = fmt.Sprintf("HTTP %d", attempt.StatusCode)
			}
		}
		if call != "" {
			break
		}
	}
	switch {
	case reason != "" && call != "":
		return reason + " — 모델 호출: " + call
	case reason != "":
		return reason
	}
	return call
}
