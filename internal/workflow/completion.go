package workflow

import (
	"bytes"
	"context"
	"encoding/json"
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
	messages := []chatMessage{}
	if strings.TrimSpace(step.SystemPrompt) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: step.SystemPrompt})
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
	payload, err := json.Marshal(request)
	if err != nil {
		return "", Usage{}, err
	}
	endpoint := strings.TrimRight(step.ModelBaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(step.ModelAPIKey) != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+step.ModelAPIKey)
	}
	response, err := m.Client.Do(httpRequest)
	if err != nil {
		return "", Usage{}, err
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
	return strings.TrimSpace(decoded.Choices[0].Message.Content), decoded.Usage, nil
}
