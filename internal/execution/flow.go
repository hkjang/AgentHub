package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Running a task as a flow instead of as a conversation.
//
// The prose loop reasons at the model gateway and hands anything it cannot do to
// a person. A Langflow agent does not need that compromise: the flow somebody
// drew in its editor is the program, and the platform can execute it and keep
// what came back. What stays the same is everything around it — the run record,
// the artifacts, the completion verdict, the quota and the audit trail — because
// those are the platform's job either way, and a flow that ran outside them
// would be work nobody can account for.

// FlowInspector is the data-loss seam for text entering and leaving a flow. It
// is the same interface the model client uses, so one set of detectors and one
// policy decide both boundaries.
type FlowInspector interface {
	Outbound(ctx context.Context, step workflow.Step, text string) (string, error)
	Inbound(ctx context.Context, step workflow.Step, text string) (string, error)
}

// WithFlowInspector attaches the content scanner. Without one a flow run is not
// scanned, which is the same contract the model client has: the worker wires it
// up, and a deployment that has not configured DLP is not silently told it has.
func (o *Orchestrator) WithFlowInspector(inspector FlowInspector) *Orchestrator {
	o.flowInspector = inspector
	return o
}

const (
	// flowResponseLimit bounds what is read from one run. A flow can return its
	// whole graph state, and an unbounded read would put it in the worker's memory
	// and then in the run record.
	flowResponseLimit = 4 << 20
	// flowTextLimit bounds what is stored as the step's output.
	flowTextLimit = 200_000
	// flowRequestTimeout is the ceiling for one flow run when the Goal sets no
	// duration of its own. Langflow's own worker timeout defaults to 300s.
	flowRequestTimeout = 10 * time.Minute
)

// runFlow executes the Goal's flow once and returns its answer as the run's
// transcript, which the evaluator then judges exactly as it judges reasoning.
func (o *Orchestrator) runFlow(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		// Retryable: this is the Pod not being there, not the flow being wrong.
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "흐름을 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	// The content check comes before the runtime is even addressed: a task that
	// must not leave the platform has no business being sent to a flow engine, and
	// finding that out first costs nothing.
	step := workflow.Step{ID: "flow", AgentID: agent.ID, AgentName: agent.Name}
	input := flowInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, input)
		if scanErr != nil {
			// A refusal by the scanner is not worth retrying: the same task carries
			// the same data and would be refused again.
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		input = scanned
	}

	connection, err := o.flowConnection(ctx, agent, acquired.runtimeID)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "Runtime에 연결하지 못해 흐름을 실행할 수 없습니다: " + err.Error()}
	}

	ctx, span := telemetry.Start(ctx, "flow.run",
		attribute.String("agenthub.flow.id", goal.FlowID),
		attribute.String("agenthub.runtime.id", acquired.runtimeID))
	defer span.End()

	sequence := 1
	startedAt := time.Now()
	o.event(ctx, *run, "flow.started", "Langflow 흐름을 실행합니다.", map[string]any{"flowId": goal.FlowID, "runtimeId": acquired.runtimeID})
	answer, usage, err := o.callFlow(ctx, connection, goal, task, input)
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, err)

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: sequence, Type: "flow",
		Title: "흐름 실행", Input: input, Output: answer, Status: "succeeded", DurationMs: elapsed,
	}
	if err != nil {
		record.Status, record.Error = "failed", err.Error()
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("flow step could not be recorded", "run", run.ID, "error", storeErr)
	}
	run.StepCount = sequence
	if err != nil {
		o.event(ctx, *run, "flow.failed", err.Error(), map[string]any{"flowId": goal.FlowID})
		return nil, Outcome{Status: store.TaskFailed, Failure: err.Error(), Retryable: retryableFlowError(err)}
	}

	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}

	// Whatever the runtime said about tokens is passed through as it said it. The
	// platform does not meter a flow's model calls — they happen inside the flow,
	// against whatever endpoint its own components point at — and turning an
	// unverified field into a billed number would be worse than reporting none.
	details := map[string]any{"flowId": goal.FlowID, "durationMs": elapsed, "chars": len(answer)}
	if len(usage) > 0 {
		details["runtimeReportedUsage"] = usage
	}
	o.event(ctx, *run, "flow.completed", "흐름 실행이 끝났습니다.", details)
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// flowConnection resolves the address and token of the runtime holding the flow.
func (o *Orchestrator) flowConnection(ctx context.Context, agent store.Agent, runtimeID string) (appRuntime.Connection, error) {
	instance, err := o.store.RuntimeByID(ctx, runtimeID, agent.OwnerID, true)
	if err != nil {
		return appRuntime.Connection{}, err
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return appRuntime.Connection{}, err
	}
	return o.spawner.Connection(ctx, spec)
}

// flowInput is what the flow receives as its input value: the task, and the
// standing instructions the Goal carries. A flow's author decides what to do with
// it, so it is plain text rather than the prose loop's scaffolding.
func flowInput(task store.AgentTask, goal store.AgentGoal) string {
	var b strings.Builder
	b.WriteString(task.Title)
	if strings.TrimSpace(task.Input) != "" {
		b.WriteString("\n\n")
		b.WriteString(task.Input)
	}
	if strings.TrimSpace(goal.Description) != "" {
		b.WriteString("\n\n[목표]\n")
		b.WriteString(goal.Description)
	}
	if strings.TrimSpace(goal.Constraints) != "" {
		b.WriteString("\n\n[제약]\n")
		b.WriteString(goal.Constraints)
	}
	return b.String()
}

// flowError carries the HTTP status so the retry decision does not have to parse
// a message.
type flowError struct {
	status  int
	message string
}

func (e flowError) Error() string { return e.message }

// retryableFlowError decides whether another attempt could end differently.
//
// A 4xx is the platform asking for something wrong — an unknown flow id, a
// rejected credential — and repeating it wastes the retry budget. 429 is the
// exception, and anything at or above 500, or no answer at all, is the runtime
// having a bad moment.
func retryableFlowError(err error) bool {
	var failure flowError
	if errors.As(err, &failure) {
		return failure.status == http.StatusTooManyRequests || failure.status >= http.StatusInternalServerError
	}
	if errors.Is(err, workflow.ErrBlocked) {
		return false
	}
	return true
}

// callFlow performs the run request and returns the answer text together with
// whatever the runtime reported about token usage.
func (o *Orchestrator) callFlow(ctx context.Context, connection appRuntime.Connection, goal store.AgentGoal, task store.AgentTask, input string) (string, map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"input_value": input,
		"input_type":  "chat",
		"output_type": "chat",
		// The task id keeps a flow's own memory components on one conversation
		// across retries, which is what makes resuming meaningful.
		"session_id":       task.ID,
		"output_component": goal.FlowOutputComponent,
	})
	if err != nil {
		return "", nil, err
	}
	endpoint := strings.TrimSuffix(connection.Endpoint, "/") + "/api/v1/run/" + goal.FlowID
	requestCtx, cancel := context.WithTimeout(ctx, flowRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	// Two credentials, one secret. The in-Pod proxy in front of the runtime checks
	// Basic auth; Langflow itself checks x-api-key even with automatic login on.
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("agenthub:"+connection.Token)))
	request.Header.Set("x-api-key", connection.Token)

	response, err := flowHTTPClient.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("흐름 실행 요청이 실패했습니다: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, flowResponseLimit))
	if err != nil {
		return "", nil, fmt.Errorf("흐름 응답을 읽지 못했습니다: %w", err)
	}
	if response.StatusCode >= 300 {
		return "", nil, flowError{status: response.StatusCode, message: fmt.Sprintf("흐름 실행이 거부되었습니다(HTTP %d): %s", response.StatusCode, flowDetail(payload))}
	}
	answer, usage, ok := flowAnswer(payload, goal.FlowOutputComponent)
	if !ok {
		return "", nil, flowError{status: response.StatusCode, message: "흐름이 실행되었지만 읽을 수 있는 출력이 없습니다. 흐름에 Chat Output 같은 출력 컴포넌트가 있는지 확인해 주세요."}
	}
	return answer, usage, nil
}

// flowHTTPClient is shared so runs reuse connections to the same runtime. The
// per-request deadline does the bounding.
var flowHTTPClient = &http.Client{Timeout: flowRequestTimeout + time.Minute}

// flowDetail pulls the runtime's own explanation out of an error body, which is
// where Langflow puts the component that failed.
func flowDetail(payload []byte) string {
	var envelope struct {
		Detail any `json:"detail"`
	}
	text := strings.TrimSpace(string(payload))
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Detail != nil {
		if message, isText := envelope.Detail.(string); isText {
			text = message
		} else if encoded, marshalErr := json.Marshal(envelope.Detail); marshalErr == nil {
			text = string(encoded)
		}
	}
	if len(text) > 2000 {
		text = text[:2000] + "…"
	}
	return text
}

// flowRunResponse is the shape of a Langflow run, reduced to what the platform
// reads. The fields it does not name are left alone rather than rejected: a flow
// returns whatever its components produce, and a stricter reader would fail on
// somebody's perfectly good graph.
type flowRunResponse struct {
	SessionID string `json:"session_id"`
	Outputs   []struct {
		Outputs []flowComponentOutput `json:"outputs"`
	} `json:"outputs"`
}

type flowComponentOutput struct {
	ComponentID string `json:"component_id"`
	Results     struct {
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
	} `json:"results"`
	Outputs struct {
		Message struct {
			Message string `json:"message"`
		} `json:"message"`
	} `json:"outputs"`
	Artifacts struct {
		Message string `json:"message"`
	} `json:"artifacts"`
	Messages []struct {
		Message string `json:"message"`
	} `json:"messages"`
	TokenUsage map[string]any `json:"token_usage"`
}

// flowAnswer extracts the flow's answer.
//
// Langflow reports the same text in several places depending on which component
// produced it, so this reads them in order of how specific they are rather than
// betting on one. When the Goal names an output component that one wins, which is
// what makes a flow with two outputs usable.
func flowAnswer(payload []byte, component string) (string, map[string]any, bool) {
	var response flowRunResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", nil, false
	}
	var chosen *flowComponentOutput
	var usage map[string]any
	for group := range response.Outputs {
		for index := range response.Outputs[group].Outputs {
			candidate := &response.Outputs[group].Outputs[index]
			if len(candidate.TokenUsage) > 0 && usage == nil {
				usage = candidate.TokenUsage
			}
			if component != "" && candidate.ComponentID != component {
				continue
			}
			if outputText(candidate) == "" {
				continue
			}
			// The last one wins: a graph lists its outputs in execution order, so
			// the final component is the one that answered.
			chosen = candidate
		}
	}
	if chosen == nil {
		return "", usage, false
	}
	text := outputText(chosen)
	if len(text) > flowTextLimit {
		text = text[:flowTextLimit] + "\n\n…(출력이 잘렸습니다)"
	}
	return text, usage, true
}

func outputText(output *flowComponentOutput) string {
	for _, candidate := range []string{output.Results.Message.Text, output.Outputs.Message.Message, output.Artifacts.Message} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	for _, message := range output.Messages {
		if strings.TrimSpace(message.Message) != "" {
			return message.Message
		}
	}
	return ""
}
