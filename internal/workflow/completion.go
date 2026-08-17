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

// CompleteWithUsage also reports the token accounting the gateway returned, which
// is what makes a run's cost attributable per step.
func (m *ModelCompletion) CompleteWithUsage(ctx context.Context, step Step, prompt string) (string, Usage, error) {
	if strings.TrimSpace(step.ModelBaseURL) == "" || strings.TrimSpace(step.ModelName) == "" {
		return "", Usage{}, fmt.Errorf("agent %q has no model endpoint bound", step.AgentName)
	}
	messages := []chatMessage{}
	if strings.TrimSpace(step.SystemPrompt) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: step.SystemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})

	payload, err := json.Marshal(map[string]any{"model": step.ModelName, "messages": messages, "stream": false})
	if err != nil {
		return "", Usage{}, err
	}
	endpoint := strings.TrimRight(step.ModelBaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(step.ModelAPIKey) != "" {
		request.Header.Set("Authorization", "Bearer "+step.ModelAPIKey)
	}
	response, err := m.Client.Do(request)
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
