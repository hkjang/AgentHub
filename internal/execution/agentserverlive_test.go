package execution

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// What a real agent server does with what this platform sends it.
//
// The adapter was written from a server's API description, and a description is
// what somebody meant rather than what the machine does — reading it is how the
// history page size ended up at 200, which the server answers with a 500. This
// drives the actual client against an actual server, and stands a gateway in the
// test process so the model call comes back here to be looked at.
//
// It is skipped when there is no server to talk to. Point it at one with:
//
//	AGENTHUB_AGENT_SERVER=http://127.0.0.1:18000 \
//	AGENTHUB_AGENT_SERVER_CALLBACK=host.docker.internal \
//	go test ./internal/execution -run LiveAgentServer

type quietNotes struct {
	actions int
	tools   []string
	failure error
}

func (n *quietNotes) activity(_ context.Context, _ string, actions int, tools []string) {
	n.actions, n.tools = actions, tools
}

func (n *quietNotes) trouble(_ context.Context, _ string, err error) { n.failure = err }

// seenCall is one model call as the gateway saw it.
type seenCall struct {
	Authorization string
	Model         string
}

func TestLiveAgentServerHoldsAConversation(t *testing.T) {
	base := os.Getenv("AGENTHUB_AGENT_SERVER")
	if base == "" {
		t.Skip("no agent server to talk to")
	}
	// How the server reaches back to this process. It is not localhost: the server
	// is on another machine, which is the whole point of the backend.
	callback := os.Getenv("AGENTHUB_AGENT_SERVER_CALLBACK")
	if callback == "" {
		t.Skip("no address the server can call this test back on")
	}

	var mutex sync.Mutex
	calls := []seenCall{}
	gateway := gatewayFor(t, func(authorization string, body map[string]any) {
		mutex.Lock()
		defer mutex.Unlock()
		model, _ := body["model"].(string)
		calls = append(calls, seenCall{Authorization: authorization, Model: model})
	})
	defer gateway.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + callback + ":" + port + "/v1"
	// A moment for the address to become reachable from outside this machine.
	// Without it the agent's first model call can be refused, and it reports that
	// as the model being unavailable rather than as a race in this test.
	time.Sleep(2 * time.Second)

	client := &agentServerClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 60 * time.Second}}
	notes := &quietNotes{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	goal := store.AgentGoal{MaxSteps: 3, MaxDurationSeconds: 120, AgentServerDir: "workspace/live"}
	result, err := client.hold(ctx, notes, goal,
		"Reply with exactly AGENTHUB-SERVER-OK and finish.",
		resolvedModel{BaseURL: endpoint, ModelName: "openai/agenthub-model", APIKey: "agenthub-issued-key"})
	if err != nil {
		t.Fatalf("a live agent server did not hold the conversation: %v", err)
	}
	if !strings.Contains(result.Answer, "AGENTHUB-SERVER-OK") {
		t.Errorf("the server's answer did not come back: %q", result.Answer)
	}
	if result.Status != "finished" {
		t.Errorf("the conversation ended as %q", result.Status)
	}
	// What it cost, as the server itself reports it. Zero would mean this platform
	// meters work it cannot see, which is why the run reads the server's metrics
	// rather than counting turns.
	if result.Tokens <= 0 {
		t.Errorf("the server reported no usage; the run would be billed as free")
	}
	if notes.actions <= 0 {
		t.Errorf("nothing the agent did reached this run's timeline (%v)", notes.failure)
	}

	// The point of the backend: the work ran on a machine this deployment does not
	// own, and the model call still came through this deployment's gateway with
	// this deployment's credential. Environment variables do not do this — the
	// server reads those when it starts, on a machine somebody else configured.
	mutex.Lock()
	defer mutex.Unlock()
	if len(calls) == 0 {
		t.Fatal("the agent never called the gateway; it was pointed somewhere else")
	}
	for _, call := range calls {
		if call.Authorization != "Bearer agenthub-issued-key" {
			t.Errorf("a model call carried %q instead of this deployment's credential", call.Authorization)
		}
		if !strings.Contains(call.Model, "agenthub-model") {
			t.Errorf("a model call asked for %q, which this deployment did not choose", call.Model)
		}
	}
}

// gatewayFor stands in for this deployment's gateway, on an address the server
// can reach.
func gatewayFor(t *testing.T, saw func(authorization string, body map[string]any)) *httptest.Server {
	t.Helper()
	// IPv4 on every interface, because the server calling back is on another
	// machine. Named as tcp4 rather than tcp: the wildcard would give a dual-stack
	// socket, which some host networks do not publish outward at all.
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		saw(r.Header.Get("Authorization"), body)
		// A tool call the agent recognises, so the conversation finishes instead of
		// running until the iteration limit.
		message := map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "finish", "arguments": `{"message":"AGENTHUB-SERVER-OK"}`},
		}}}
		if !mentionsFinish(body) {
			message = map[string]any{"role": "assistant", "content": "AGENTHUB-SERVER-OK"}
		}
		writeCompletion(w, message)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	return server
}

func mentionsFinish(body map[string]any) bool {
	tools, _ := body["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if name, _ := function["name"].(string); name == "finish" {
			return true
		}
	}
	return false
}

func writeCompletion(w http.ResponseWriter, message map[string]any) {
	reason := "stop"
	if _, calls := message["tool_calls"]; calls {
		reason = "tool_calls"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-live", "object": "chat.completion", "created": 1,
		"model":   "openai/agenthub-model",
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": reason}},
		"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 6, "total_tokens": 18},
	})
}
