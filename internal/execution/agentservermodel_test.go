package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// OpenHands routes through LiteLLM, which refuses a model with no provider in
// front of it: "LLM Provider NOT provided. You passed model=stub". This
// platform's endpoints are named the way the deployment names them, so a
// perfectly good endpoint failed every conversation — observed against a real
// 1.43.1 server on a real cluster.
func TestTheModelIsNamedTheWayTheServersClientInsists(t *testing.T) {
	for given, want := range map[string]string{
		"stub":                    "openai/stub",
		"qwen3-coder":             "openai/qwen3-coder",
		"gpt-4o-mini":             "openai/gpt-4o-mini",
		"anthropic/claude-sonnet": "anthropic/claude-sonnet",
		"openai/gpt-4o":           "openai/gpt-4o",
		"":                        "",
	} {
		if got := agentServerModel(given); got != want {
			t.Errorf("%q was sent as %q, want %q", given, got, want)
		}
	}
}

// And it reaches the request, not just the helper.
func TestTheStartRequestCarriesTheProvider(t *testing.T) {
	body := agentServerStart(goalWithNothingSet(), "안녕", resolvedModel{ModelName: "stub", BaseURL: "http://gw/v1", APIKey: "k"})
	agent, _ := body["agent"].(map[string]any)
	llm, _ := agent["llm"].(map[string]any)
	if llm["model"] != "openai/stub" {
		t.Fatalf("the conversation was started with model %v", llm["model"])
	}
}

// A failed conversation says why on its own timeline. Reporting only that it
// failed threw that away and left somebody comparing deployments to learn what
// one line already said.
func TestAFailedConversationSaysWhatTheServerSaid(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The state update follows the error on a real timeline — this is the
		// order the live server produced — so the last event is not the error
		// and picking by position rather than by kind reports the wrong thing.
		_, _ = w.Write([]byte(`{"items":[
			{"kind":"SystemPromptEvent"},
			{"kind":"ConversationErrorEvent","code":"LLMBadRequestError","detail":"litellm.BadRequestError: LLM Provider NOT provided.\nYou passed model=stub"},
			{"kind":"ConversationStateUpdateEvent","detail":"execution_status -> error"}]}`))
	}))
	defer forge.Close()
	client := &agentServerClient{base: forge.URL, http: forge.Client()}
	detail := client.conversationError(context.Background(), "c1")
	if !strings.Contains(detail, "LLM Provider NOT provided") {
		t.Fatalf("the server's own words are not carried: %q", detail)
	}
	if strings.Contains(detail, "\n") {
		t.Errorf("a run's line carries a newline: %q", detail)
	}
	if strings.Contains(detail, "execution_status") {
		t.Errorf("an event that is not the error was reported as one: %q", detail)
	}
}

// Nothing to find is not something to invent.
func TestAConversationWithNoErrorAddsNothing(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"kind":"SystemPromptEvent"},{"kind":"MessageEvent"}]}`))
	}))
	defer forge.Close()
	client := &agentServerClient{base: forge.URL, http: forge.Client()}
	if detail := client.conversationError(context.Background(), "c1"); detail != "" {
		t.Fatalf("a conversation with no error produced %q", detail)
	}
}

// A timeline that cannot be read is not a diagnosis either.
func TestAnUnreadableTimelineAddsNothing(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refused, and still carrying a body — a proxy's cached page, an error
		// envelope. What decides is the status, not what happens to parse.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"items":[{"kind":"ConversationErrorEvent","detail":"어제 다른 대화의 오류"}]}`))
	}))
	defer forge.Close()
	client := &agentServerClient{base: forge.URL, http: forge.Client()}
	if detail := client.conversationError(context.Background(), "c1"); detail != "" {
		t.Fatalf("an unreadable timeline produced %q", detail)
	}
}

func goalWithNothingSet() store.AgentGoal { return store.AgentGoal{} }
