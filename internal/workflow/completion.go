package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ModelCompletion calls the OpenAI-compatible chat endpoint each agent is bound
// to. That interface is the common denominator across every model gateway the
// platform supports — vLLM, Ollama and anything else registered as an endpoint.
type ModelCompletion struct {
	Client *http.Client
	// Inspector sees every prompt on its way out and every answer on its way
	// back. It is an interface rather than a concrete scanner because this
	// package has no business knowing what a resident registration number is —
	// it only has to know that this is the one place every model call passes
	// through, which is why the check belongs here and not in each caller.
	Inspector Inspector
}

// ErrBlocked is what an inspector returns when the text may not cross. It is a
// sentinel because the answer is deterministic: retrying the same prompt cannot
// produce a different decision, and treating it as a transient model error would
// cycle the task through its whole retry budget to reach the same refusal.
var ErrBlocked = errors.New("전송이 차단되었습니다")

// Inspector inspects, and may rewrite or refuse, text crossing the boundary
// between the platform and a model.
type Inspector interface {
	// Outbound returns the text to send, or an error to refuse the call.
	Outbound(ctx context.Context, step Step, text string) (string, error)
	// Inbound is the same for what came back. Refusing here means the answer is
	// not handed to the agent.
	Inbound(ctx context.Context, step Step, text string) (string, error)
}

// WithInspector attaches one, and returns the completion so a constructor reads
// as one expression.
func (m *ModelCompletion) WithInspector(inspector Inspector) *ModelCompletion {
	m.Inspector = inspector
	return m
}

func NewModelCompletion() *ModelCompletion {
	// Individual steps are bounded by the run's overall deadline; this timeout
	// only stops one hung request from holding a slot forever.
	return &ModelCompletion{Client: &http.Client{Timeout: 5 * time.Minute}}
}

// Usage is the OpenAI-compatible token accounting every gateway reports.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (m *ModelCompletion) Complete(ctx context.Context, step Step, prompt string) (string, error) {
	output, _, err := m.CompleteWithUsage(ctx, step, prompt)
	return output, err
}

// Schema is the shape a structured answer has to have.
//
// A decision the platform acts on — which branch a router chose, whether a goal
// was met — used to be read out of prose, and prose is not a protocol: a router
// that wrote "이 건은 배포팀에 보내지 않습니다" selected 배포팀, because the name
// appeared. Asking the gateway to constrain the answer to a schema removes the
// guessing, and the caller validates what comes back either way.
type Schema struct {
	// Name identifies the schema to the gateway; some log or cache by it.
	Name string
	// Body is the JSON Schema itself.
	Body map[string]any
}

// StructuredResult is a structured answer and how much to trust its shape.
type StructuredResult struct {
	Output string
	Usage  Usage
	// Validated is true when the gateway accepted the request carrying the schema,
	// rather than refusing it. It is not proof the answer was constrained: a
	// gateway that ignores response_format also accepts the request. It is the
	// strongest thing the client can know from the outside, which is why the caller
	// validates the answer either way.
	Validated bool
}

// StructuredCompleter is the optional capability a Completion can offer: ask for
// an answer that matches a schema. Implementations that cannot are simply asked
// in prose instead, so a gateway without response_format support keeps working.
type StructuredCompleter interface {
	CompleteStructured(ctx context.Context, step Step, prompt string, schema Schema) (StructuredResult, error)
}

// CompleteStructured asks for JSON matching the schema.
//
// Not every OpenAI-compatible gateway implements response_format, and an offline
// site cannot choose its gateway freely, so a rejection is not a failure: the same
// prompt is sent again without the constraint and the answer comes back marked
// unvalidated. Anything else — a timeout, a 500 — is a real error.
func (m *ModelCompletion) CompleteStructured(ctx context.Context, step Step, prompt string, schema Schema) (StructuredResult, error) {
	output, usage, err := m.complete(ctx, step, prompt, &schema)
	if err == nil {
		return StructuredResult{Output: output, Usage: usage, Validated: true}, nil
	}
	if !schemaUnsupported(err) {
		return StructuredResult{}, err
	}
	output, usage, plainErr := m.complete(ctx, step, prompt, nil)
	if plainErr != nil {
		return StructuredResult{}, plainErr
	}
	return StructuredResult{Output: output, Usage: usage, Validated: false}, nil
}

// schemaUnsupported reports whether the gateway refused the request itself rather
// than failing to answer it. A 4xx is the gateway saying it does not understand
// what was asked, which is exactly the case worth retrying without the schema.
func schemaUnsupported(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, status := range []string{"returned 400", "returned 404", "returned 415", "returned 422", "returned 501"} {
		if strings.Contains(message, status) {
			return true
		}
	}
	return strings.Contains(message, "response_format")
}

// CompleteWithUsage also reports the token accounting the gateway returned, which
// is what makes a run's cost attributable per step.
func (m *ModelCompletion) CompleteWithUsage(ctx context.Context, step Step, prompt string) (string, Usage, error) {
	return m.complete(ctx, step, prompt, nil)
}

func (m *ModelCompletion) complete(ctx context.Context, step Step, prompt string, schema *Schema) (string, Usage, error) {
	if strings.TrimSpace(step.ModelBaseURL) == "" || strings.TrimSpace(step.ModelName) == "" {
		return "", Usage{}, fmt.Errorf("agent %q has no model endpoint bound", step.AgentName)
	}
	// Both halves are inspected: the instruction is written by a person and the
	// prompt is assembled from task input and tool output, and a credential can
	// arrive through either.
	systemPrompt, prompt, err := m.inspectOutbound(ctx, step, step.SystemPrompt, prompt)
	if err != nil {
		return "", Usage{}, err
	}
	messages := []chatMessage{}
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})

	request := map[string]any{"model": step.ModelName, "messages": messages, "stream": false}
	if schema != nil {
		request["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": schema.Name, "strict": true, "schema": schema.Body,
			},
		}
	}
	payload, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		return "", Usage{}, marshalErr
	}
	endpoint := strings.TrimRight(step.ModelBaseURL, "/") + "/chat/completions"
	httpRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if requestErr != nil {
		return "", Usage{}, requestErr
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(step.ModelAPIKey) != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+step.ModelAPIKey)
	}
	response, callErr := m.Client.Do(httpRequest)
	if callErr != nil {
		return "", Usage{}, callErr
	}
	defer func() { _ = response.Body.Close() }()

	var decoded struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage Usage `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", Usage{}, fmt.Errorf("model gateway returned %d with an unreadable body", response.StatusCode)
	}
	if response.StatusCode >= 400 {
		message := decoded.Error.Message
		if message == "" {
			message = response.Status
		}
		return "", Usage{}, fmt.Errorf("model gateway returned %d: %s", response.StatusCode, message)
	}
	if len(decoded.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("model gateway returned no completion")
	}
	answer := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if m.Inspector != nil {
		inspected, err := m.Inspector.Inbound(ctx, step, answer)
		if err != nil {
			// The tokens were spent, so they are still reported: refusing the answer
			// must not also hide what it cost.
			return "", decoded.Usage, err
		}
		answer = inspected
	}
	return answer, decoded.Usage, nil
}

// inspectOutbound runs the inspector over both halves of the request.
func (m *ModelCompletion) inspectOutbound(ctx context.Context, step Step, systemPrompt, prompt string) (string, string, error) {
	if m.Inspector == nil {
		return systemPrompt, prompt, nil
	}
	inspectedSystem, err := m.Inspector.Outbound(ctx, step, systemPrompt)
	if err != nil {
		return "", "", err
	}
	inspectedPrompt, err := m.Inspector.Outbound(ctx, step, prompt)
	if err != nil {
		return "", "", err
	}
	return inspectedSystem, inspectedPrompt, nil
}
