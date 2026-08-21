package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Running a task as an application the site already has.
//
// Dify is not one container but a dozen, and reproducing that topology inside a
// Pod would mean carrying a fork of somebody else's deployment. What a site has
// is a Dify they already run; what they want here is for a task to call one of
// its apps and for the answer to land in a run record with the platform's rules
// around it — the content scanner on the way out and back, the completion
// verdict, the quota, the audit trail.
//
// So this runner starts nothing. It is the one place in the execution plane
// where the work happens somewhere the platform does not control, which is worth
// saying out loud: the app's own model calls are not the platform's to meter, and
// what it does with the text it is given is the app author's decision.

// difyTimeout bounds one call when the Goal sets no duration of its own.
const difyTimeout = 10 * time.Minute

// difyResponseLimit is what will be read from one answer.
const difyResponseLimit = 4 << 20

var difyHTTPClient = &http.Client{Timeout: difyTimeout + time.Minute}

// runExternalApp calls the application and returns its answer as the transcript,
// which the evaluator then judges like any other.
func (o *Orchestrator) runExternalApp(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal) ([]string, Outcome) {
	app, secret, err := o.store.ExternalAppByID(ctx, goal.ExternalAppID)
	if err != nil {
		// A missing app is a configuration mistake, not a hiccup: retrying cannot
		// make the row exist.
		return nil, Outcome{Status: store.TaskFailed, Failure: "연결된 외부 앱을 찾을 수 없습니다. Goal에서 앱을 다시 선택해 주세요."}
	}
	if !app.Enabled {
		return nil, Outcome{Status: store.TaskFailed, Failure: fmt.Sprintf("외부 앱 %s 가 비활성 상태입니다.", app.Name)}
	}
	if strings.TrimSpace(secret) == "" {
		return nil, Outcome{Status: store.TaskFailed, Failure: fmt.Sprintf("외부 앱 %s 의 API 키가 설정되어 있지 않습니다.", app.Name)}
	}

	step := workflow.Step{ID: "external", AgentID: agent.ID, AgentName: agent.Name}
	input := runnerInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, input)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		input = scanned
	}

	ctx, span := telemetry.Start(ctx, "external.run",
		attribute.String("agenthub.external.provider", app.Provider),
		attribute.String("agenthub.external.kind", app.AppKind))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "external.started", fmt.Sprintf("외부 앱 %s 을(를) 실행합니다.", app.Name), map[string]any{
		"app": app.Name, "provider": app.Provider, "kind": app.AppKind,
	})
	answer, usage, callErr := callDifyApp(ctx, app, secret, goal, task, input)
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, callErr)

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepExternal,
		Title: "외부 앱 실행: " + app.Name, Input: input, Output: answer, Status: "succeeded", DurationMs: elapsed,
	}
	run.StepCount = 1
	if callErr != nil {
		record.Status, record.Error = "failed", callErr.Error()
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("external app step could not be recorded", "run", run.ID, "error", storeErr)
	}
	if callErr != nil {
		o.event(ctx, *run, "external.failed", callErr.Error(), map[string]any{"app": app.Name})
		return nil, Outcome{Status: store.TaskFailed, Failure: callErr.Error(), Retryable: retryableFlowError(callErr)}
	}

	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}

	// Dify counts the tokens its own app spent and says so. It is recorded as the
	// app reported it and kept out of the platform's own metering, because those
	// tokens were bought against that deployment's model provider rather than the
	// endpoint this platform holds.
	run.Metering = store.MeteringUnmetered
	details := map[string]any{"app": app.Name, "durationMs": elapsed, "chars": len(answer)}
	if len(usage) > 0 {
		details["appReportedUsage"] = usage
	}
	o.event(ctx, *run, "external.completed", "외부 앱 실행이 끝났습니다.", details)
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// callDifyApp performs the request. The two kinds answer at different endpoints
// and put the result in different places, which is why the kind is stored with
// the app rather than guessed per call.
func callDifyApp(ctx context.Context, app store.ExternalApp, secret string, goal store.AgentGoal, task store.AgentTask, input string) (string, map[string]any, error) {
	base := strings.TrimSuffix(strings.TrimSpace(app.BaseURL), "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	// The end user Dify attributes the call to. The task's owner would leak an
	// internal identifier into somebody else's system, so the agent is who asks.
	user := "agenthub-agent-" + task.AgentID

	var endpoint string
	var payload map[string]any
	switch app.AppKind {
	case "chat":
		endpoint = base + "/chat-messages"
		payload = map[string]any{
			"query": input, "inputs": map[string]any{}, "response_mode": "blocking", "user": user,
			// The task id keeps a chat app's own memory on one conversation across
			// retries, the same way the flow runner uses it as a session.
			"conversation_id": "",
		}
	default:
		endpoint = base + "/workflows/run"
		key := strings.TrimSpace(goal.ExternalInputKey)
		if key == "" {
			key = "input"
		}
		payload = map[string]any{
			"inputs": map[string]any{key: input}, "response_mode": "blocking", "user": user,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	timeout := difyTimeout
	if goal.MaxDurationSeconds > 0 {
		timeout = time.Duration(goal.MaxDurationSeconds) * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)

	response, err := difyHTTPClient.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("외부 앱 호출이 실패했습니다: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, difyResponseLimit))
	if err != nil {
		return "", nil, fmt.Errorf("외부 앱 응답을 읽지 못했습니다: %w", err)
	}
	if response.StatusCode >= 300 {
		return "", nil, flowError{status: response.StatusCode, message: fmt.Sprintf("외부 앱이 요청을 거부했습니다(HTTP %d): %s", response.StatusCode, flowDetail(raw))}
	}
	return difyAnswer(app.AppKind, raw)
}

// difyChatResponse and difyWorkflowResponse are the two answers, reduced to what
// the platform reads.
type difyChatResponse struct {
	Answer   string `json:"answer"`
	Metadata struct {
		Usage map[string]any `json:"usage"`
	} `json:"metadata"`
}

type difyWorkflowResponse struct {
	Data struct {
		ID          string         `json:"id"`
		Status      string         `json:"status"`
		Outputs     map[string]any `json:"outputs"`
		Error       string         `json:"error"`
		ElapsedTime float64        `json:"elapsed_time"`
		TotalTokens int            `json:"total_tokens"`
		TotalSteps  int            `json:"total_steps"`
	} `json:"data"`
}

// difyAnswer extracts the result. A workflow that failed says so in its own body
// with HTTP 200, so the status is checked rather than assumed from the code.
func difyAnswer(kind string, raw []byte) (string, map[string]any, error) {
	if kind == "chat" {
		var parsed difyChatResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", nil, errors.New("외부 앱 응답을 해석하지 못했습니다")
		}
		if strings.TrimSpace(parsed.Answer) == "" {
			return "", parsed.Metadata.Usage, errors.New("외부 앱이 빈 답변을 돌려주었습니다")
		}
		return parsed.Answer, parsed.Metadata.Usage, nil
	}

	var parsed difyWorkflowResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, errors.New("외부 앱 응답을 해석하지 못했습니다")
	}
	usage := map[string]any{}
	if parsed.Data.TotalTokens > 0 {
		usage["total_tokens"] = parsed.Data.TotalTokens
	}
	if parsed.Data.TotalSteps > 0 {
		usage["total_steps"] = parsed.Data.TotalSteps
	}
	if parsed.Data.Status != "" && parsed.Data.Status != "succeeded" {
		reason := parsed.Data.Error
		if reason == "" {
			reason = parsed.Data.Status
		}
		// A workflow that stopped or failed answered with HTTP 200 and said so in
		// the body; recording that as a result would be recording a failure as
		// work done.
		return "", usage, fmt.Errorf("외부 앱 워크플로가 %s 상태로 끝났습니다: %s", parsed.Data.Status, firstLine(reason))
	}
	text := difyOutputText(parsed.Data.Outputs)
	if strings.TrimSpace(text) == "" {
		return "", usage, errors.New("외부 앱이 결과를 돌려주지 않았습니다. 워크플로에 출력 변수가 있는지 확인해 주세요.")
	}
	return text, usage, nil
}

// difyOutputText turns a workflow's named outputs into the transcript.
//
// A workflow's outputs are whatever its author named them, so there is no field
// to read: a single one is used as it is, and several are kept together with
// their names rather than one being picked and the rest dropped.
func difyOutputText(outputs map[string]any) string {
	if len(outputs) == 0 {
		return ""
	}
	if len(outputs) == 1 {
		for _, value := range outputs {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	encoded, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return ""
	}
	return string(encoded)
}
