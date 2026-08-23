package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// What this stands in for is the platform's answer to the server's gate: a
	// person, or a policy. The test supplies it directly so the gate itself is
	// what is being checked.
	answer func(pendingAction) bool
	asked  []pendingAction
	// cancelAfter makes this stand-in report the work called off on the nth
	// question. Zero means nobody ever cancels.
	cancelAfter int
	askCount    int
}

func (n *quietNotes) decide(_ context.Context, action pendingAction) (bool, error) {
	n.asked = append(n.asked, action)
	if n.answer == nil {
		return true, nil
	}
	return n.answer(action), nil
}

func (n *quietNotes) activity(_ context.Context, _ string, actions int, tools []string) {
	n.actions, n.tools = actions, tools
}

func (n *quietNotes) trouble(_ context.Context, _ string, err error) { n.failure = err }

// calledOff stands in for somebody pressing cancel. The default is that the work
// is still wanted, which is what every other case in this file assumes.
func (n *quietNotes) calledOff(context.Context) bool {
	if n.cancelAfter == 0 {
		return false
	}
	n.askCount++
	return n.askCount >= n.cancelAfter
}

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

// TestLiveAgentServerHoldsEachActionForAnAnswer is the property that makes this
// backend usable where it matters: the machine is somebody else's, and the
// platform still decides what runs on it.
//
// Both answers are exercised. An approval that only ever says yes proves the
// conversation continues, not that the gate exists — a server that ignored the
// answer entirely would pass that test.
func TestLiveAgentServerHoldsEachActionForAnAnswer(t *testing.T) {
	base := os.Getenv("AGENTHUB_AGENT_SERVER")
	callback := os.Getenv("AGENTHUB_AGENT_SERVER_CALLBACK")
	if base == "" || callback == "" {
		t.Skip("no agent server to talk to")
	}

	for _, decision := range []struct {
		name     string
		allow    bool
		ranIt    bool
		endsWell bool
	}{
		{name: "허용", allow: true, ranIt: true, endsWell: true},
		{name: "거부", allow: false, ranIt: false, endsWell: false},
	} {
		t.Run(decision.name, func(t *testing.T) {
			var mutex sync.Mutex
			ran := false
			gateway := toolGatewayFor(t, func(body map[string]any) {
				mutex.Lock()
				defer mutex.Unlock()
				// Whether the action actually happened, read from the tool result the
				// server sent back — not from the request that asked for it, which
				// carries the same text whether or not anything ran.
				if toolResultMentions(body, "AGENTHUB-ACTION-RAN") {
					ran = true
				}
			})
			defer gateway.Close()
			_, port, err := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Second)

			client := &agentServerClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 60 * time.Second}}
			notes := &quietNotes{answer: func(pendingAction) bool { return decision.allow }}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			goal := store.AgentGoal{MaxSteps: 4, MaxDurationSeconds: 90, AgentServerDir: "workspace/gate", ApprovalRequired: true}
			result, err := client.hold(ctx, notes, goal,
				"Run the command you are given, then finish.",
				resolvedModel{BaseURL: "http://" + callback + ":" + port + "/v1", ModelName: "openai/agenthub-model", APIKey: "agenthub-issued-key"})

			if len(notes.asked) == 0 {
				t.Fatal("the agent ran without asking; the gate was never reached")
			}
			// What the person is shown has to be the thing that would run. An empty
			// rendering is an approval of nothing.
			if !strings.Contains(notes.asked[0].Detail, "AGENTHUB-ACTION-RAN") {
				t.Errorf("the pending action was shown as %q, which does not say what would run", notes.asked[0].Detail)
			}
			if notes.asked[0].Tool == "" {
				t.Error("the pending action does not name the tool it would use")
			}

			mutex.Lock()
			defer mutex.Unlock()
			if ran != decision.ranIt {
				if decision.allow {
					t.Error("an approved command did not run")
				} else {
					t.Error("a refused command ran anyway; the answer did not reach the server")
				}
			}
			if decision.endsWell {
				if err != nil {
					t.Fatalf("the conversation did not finish: %v", err)
				}
				if result.Status != "finished" {
					t.Errorf("the conversation ended as %q", result.Status)
				}
				return
			}
			// A refusal is not a timeout and must not be reported as one: the agent
			// stops where it was refused, and the run has to say that is what
			// happened.
			if err == nil {
				t.Fatal("a refused run was reported as a success")
			}
			if !strings.Contains(err.Error(), "승인") {
				t.Errorf("a refused run failed with %q, which does not say it was refused", err)
			}
		})
	}
}

// toolGatewayFor stands in for the gateway and drives the agent toward one
// action: the first turn asks for a command, everything after it finishes.
func toolGatewayFor(t *testing.T, saw func(map[string]any)) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	turn := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		saw(body)
		mutex.Lock()
		turn++
		asking := turn == 2 && mentionsTool(body, "terminal")
		mutex.Unlock()
		if asking {
			writeCompletion(w, map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
				"id": "act_1", "type": "function",
				"function": map[string]any{"name": "terminal", "arguments": `{"command":"echo AGENTHUB-ACTION-RAN"}`},
			}}})
			return
		}
		if mentionsTool(body, "finish") {
			writeCompletion(w, map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
				"id": "fin_1", "type": "function",
				"function": map[string]any{"name": "finish", "arguments": `{"message":"게이트-확인"}`},
			}}})
			return
		}
		writeCompletion(w, map[string]any{"role": "assistant", "content": "게이트-확인"})
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	return server
}

func mentionsTool(body map[string]any, name string) bool {
	tools, _ := body["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if found, _ := function["name"].(string); found == name {
			return true
		}
	}
	return false
}

// toolResultMentions looks for text the machine produced, in the one place only
// the machine can have put it: a message the server sends back as a tool result.
func toolResultMentions(body map[string]any, text string) bool {
	messages, _ := body["messages"].([]any)
	for _, entry := range messages {
		message, _ := entry.(map[string]any)
		if role, _ := message["role"].(string); role != "tool" {
			continue
		}
		raw, _ := json.Marshal(message["content"])
		if strings.Contains(string(raw), text) {
			return true
		}
	}
	return false
}

// TestLiveCancellationReachesTheOtherMachine is the stop button doing something.
//
// Cancelling writes to a row in this deployment's database. The conversation is
// held on a machine that never hears about it, so without the platform carrying
// the news the agent goes on working — spending the site's model budget and
// touching a workspace — for a task somebody has already called off.
func TestLiveCancellationReachesTheOtherMachine(t *testing.T) {
	base := os.Getenv("AGENTHUB_AGENT_SERVER")
	callback := os.Getenv("AGENTHUB_AGENT_SERVER_CALLBACK")
	if base == "" || callback == "" {
		t.Skip("no agent server to talk to")
	}

	var mutex sync.Mutex
	turns := 0
	// A gateway that keeps the agent busy: every turn asks for another command, so
	// the conversation would run until the iteration limit if nothing stopped it.
	gateway := toolGatewayForever(t, func() {
		mutex.Lock()
		turns++
		mutex.Unlock()
	})
	defer gateway.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)

	client := &agentServerClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 60 * time.Second}}
	// Called off at the first asking, which happens five polls in.
	notes := &quietNotes{cancelAfter: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	goal := store.AgentGoal{MaxSteps: 50, MaxDurationSeconds: 150, AgentServerDir: "workspace/cancel"}
	result, err := client.hold(ctx, notes, goal, "Keep running the commands you are given.",
		resolvedModel{BaseURL: "http://" + callback + ":" + port + "/v1", ModelName: "openai/agenthub-model", APIKey: "agenthub-issued-key"})
	if err == nil {
		t.Fatal("a cancelled run reported success")
	}
	if !errors.Is(err, errAgentServerCalledOff) {
		t.Fatalf("a cancelled run failed for the wrong reason: %v", err)
	}
	if result.ConversationID == "" {
		t.Fatal("no conversation was started, so this proves nothing about stopping one")
	}

	// The machine's own answer, after the platform said stop. Anything still
	// running is the operator's cancel not having arrived.
	settled := ""
	for range 10 {
		time.Sleep(2 * time.Second)
		var info struct {
			ExecutionStatus string `json:"execution_status"`
		}
		if err := client.call(ctx, http.MethodGet, "/api/conversations/"+result.ConversationID, nil, &info); err != nil {
			t.Fatal(err)
		}
		settled = info.ExecutionStatus
		if settled != "running" {
			break
		}
	}
	if settled == "running" {
		t.Errorf("the conversation is still running on the server after the task was cancelled")
	}

	// And it stays stopped: a paused agent that quietly resumes is the same bug
	// with a delay.
	before := 0
	mutex.Lock()
	before = turns
	mutex.Unlock()
	time.Sleep(6 * time.Second)
	mutex.Lock()
	after := turns
	mutex.Unlock()
	if after != before {
		t.Errorf("the agent made %d more model calls after being stopped", after-before)
	}
}

// toolGatewayForever answers every turn with another command, so the agent never
// finishes on its own.
func toolGatewayForever(t *testing.T, saw func()) *httptest.Server {
	t.Helper()
	var count struct {
		sync.Mutex
		n int
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		saw()
		if mentionsTool(body, "terminal") {
			// A different command each turn. The server watches for an agent going in
			// circles and stops it — which would end the conversation for its own
			// reasons and make this test pass whether or not the platform's stop
			// works. It did exactly that until the commands stopped repeating.
			count.Lock()
			count.n++
			command := fmt.Sprintf(`{"command":"sleep 2 && echo STILL-WORKING-%d"}`, count.n)
			count.Unlock()
			writeCompletion(w, map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
				"id": fmt.Sprintf("keep_going_%d", count.n), "type": "function",
				"function": map[string]any{"name": "terminal", "arguments": command},
			}}})
			return
		}
		writeCompletion(w, map[string]any{"role": "assistant", "content": "…"})
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	return server
}
