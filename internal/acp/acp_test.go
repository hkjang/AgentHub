package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stand-in agent, speaking the protocol over pipes exactly as a real one would
// over stdin and stdout. It exists because the failure this code has to survive
// is not a wrong struct tag — it is the ordering: an agent that asks the client a
// question in the middle of answering the client's question, and a client that
// deadlocks because it is waiting for its own reply.
type standIn struct {
	t       *testing.T
	in      *bufio.Reader
	out     io.Writer
	mu      sync.Mutex
	asked   []string
	permits []PermissionOutcome
}

func (a *standIn) send(value any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, _ := json.Marshal(value)
	_, _ = a.out.Write(append(raw, '\n'))
}

func (a *standIn) run() {
	for {
		line, err := a.in.ReadBytes('\n')
		if len(line) == 0 || err != nil {
			return
		}
		var frame message
		if json.Unmarshal(line, &frame) != nil {
			continue
		}
		a.asked = append(a.asked, frame.Method)
		switch frame.Method {
		case "initialize":
			a.send(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{"loadSession": true,
					"promptCapabilities": map[string]any{"image": false}},
				"authMethods": []any{},
			}})
		case "session/new":
			a.send(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": map[string]any{"sessionId": "sess_1"}})
		case "session/prompt":
			// A turn: say something, ask permission, act on the answer, finish.
			a.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess_1",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "확인하겠습니다. "}},
			}})
			permissionID := 9001
			a.send(map[string]any{"jsonrpc": "2.0", "id": permissionID, "method": "session/request_permission", "params": map[string]any{
				"sessionId": "sess_1",
				"toolCall":  map[string]any{"toolCallId": "call_1", "title": "write config.yaml", "kind": "edit"},
				"options": []map[string]any{
					{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
					{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
				},
			}})
			// The client's answer arrives on the same stream this loop reads.
			for {
				reply, readErr := a.in.ReadBytes('\n')
				if readErr != nil {
					return
				}
				var answer message
				if json.Unmarshal(reply, &answer) != nil || answer.ID == nil || *answer.ID != permissionID {
					continue
				}
				var outcome struct {
					Outcome PermissionOutcome `json:"outcome"`
				}
				_ = json.Unmarshal(answer.Result, &outcome)
				a.permits = append(a.permits, outcome.Outcome)
				break
			}
			a.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess_1",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "끝냈습니다."}},
			}})
			a.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess_1",
				"update":    map[string]any{"sessionUpdate": "usage_update", "used": 4200, "size": 200000},
			}})
			a.send(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func newPair(t *testing.T) (*Client, *standIn) {
	t.Helper()
	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	agent := &standIn{t: t, in: bufio.NewReader(agentReader), out: agentWriter}
	client := New(clientReader, clientWriter)
	go agent.run()
	return client, agent
}

// The whole turn, in the order a real agent produces it.
func TestPromptTurnSurvivesAMidTurnQuestion(t *testing.T) {
	client, agent := newPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go client.Run(ctx)

	var said strings.Builder
	var used int
	client.Update = func(update SessionUpdate) {
		if update.SessionUpdate == "agent_message_chunk" {
			said.WriteString(update.Content.Text)
		}
		if update.SessionUpdate == "usage_update" {
			used = update.Used
		}
	}
	client.Permission = func(request PermissionRequest) PermissionOutcome {
		if request.ToolCall.Title == "" || len(request.Options) == 0 {
			t.Errorf("the permission request arrived without what it is about: %#v", request)
		}
		return Allow(request.Options)
	}

	capabilities, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !capabilities.AgentCapabilities.LoadSession {
		t.Error("the agent's capabilities were not read")
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil || session != "sess_1" {
		t.Fatalf("session = %q, err = %v", session, err)
	}
	stop, err := client.Prompt(ctx, session, "설정 파일을 고쳐 주세요")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Errorf("stop reason = %q", stop)
	}
	if said.String() != "확인하겠습니다. 끝냈습니다." {
		t.Errorf("the agent's message was not assembled: %q", said.String())
	}
	if used != 4200 {
		t.Errorf("reported usage = %d", used)
	}
	if len(agent.permits) != 1 || agent.permits[0].Outcome != "selected" || agent.permits[0].OptionID != "allow" {
		t.Errorf("the agent did not receive a usable answer: %#v", agent.permits)
	}
}

// The answer to a permission request is chosen from what the agent offered, not
// from a fixed identifier: agents name their options differently and some do not
// offer every kind.
func TestPermissionAnswerIsChosenFromWhatIsOffered(t *testing.T) {
	options := []PermissionOption{
		{OptionID: "y", Name: "Yes", Kind: "allow_once"},
		{OptionID: "Y", Name: "Always", Kind: "allow_always"},
		{OptionID: "n", Name: "No", Kind: "reject_once"},
	}
	if got := Allow(options); got.OptionID != "y" || got.Outcome != "selected" {
		t.Errorf("allow chose %#v", got)
	}
	if got := Deny(options); got.OptionID != "n" {
		t.Errorf("deny chose %#v", got)
	}
	// An agent that offers no way to say no leaves cancelling as the only honest
	// answer — picking an allow option because it is the only one there would be
	// the platform saying yes on the operator's behalf.
	onlyAllow := []PermissionOption{{OptionID: "y", Kind: "allow_once"}}
	if got := Deny(onlyAllow); got.Outcome != "cancelled" || got.OptionID != "" {
		t.Errorf("deny with nothing to choose = %#v", got)
	}
}

// An agent that dies mid-turn must not leave the caller waiting for an answer
// that is never coming.
func TestACallFailsWhenTheAgentGoesAway(t *testing.T) {
	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, agentReader) }()
	client := New(clientReader, clientWriter)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go client.Run(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = agentWriter.Close()
	}()
	if _, err := client.Initialize(ctx); err == nil {
		t.Fatal("a call must fail when the agent's stream ends")
	}
}

// Agents print things that are not protocol — banners, warnings. A line that is
// not JSON-RPC is not an error.
func TestNoiseOnTheStreamIsIgnored(t *testing.T) {
	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, agentReader) }()
	client := New(clientReader, clientWriter)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go client.Run(ctx)
	updates := make(chan SessionUpdate, 1)
	client.Update = func(update SessionUpdate) { updates <- update }
	go func() {
		_, _ = agentWriter.Write([]byte("Loading extensions...\n"))
		_, _ = agentWriter.Write([]byte("{not json}\n"))
		_, _ = agentWriter.Write([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ok"}}}}` + "\n"))
	}()
	select {
	case update := <-updates:
		if update.Content.Text != "ok" {
			t.Fatalf("update = %#v", update)
		}
	case <-ctx.Done():
		t.Fatal("the update after the noise never arrived")
	}
}

// A tool call's content is a list where a message's content is one block. This
// went unnoticed until a real agent was driven end to end: the strict decode
// failed, the update was dropped, and a run that edited files looked like an
// agent that had done nothing at all.
func TestToolCallUpdatesSurviveTheShapeTheyArriveIn(t *testing.T) {
	client, _ := newPair(t)
	updates := make(chan SessionUpdate, 8)
	client.Update = func(update SessionUpdate) { updates <- update }

	frame := message{Method: "session/update", Params: []byte(`{"sessionId":"s","update":{` +
		`"sessionUpdate":"tool_call","toolCallId":"t1","status":"pending","title":"WriteFile",` +
		`"kind":"edit","content":[],"locations":[],` +
		`"_meta":{"usage":{"inputTokens":120,"outputTokens":30,"totalTokens":150}}}}`)}
	client.notify(frame)

	select {
	case update := <-updates:
		if update.SessionUpdate != "tool_call" || update.Title != "WriteFile" || update.Kind != "edit" {
			t.Fatalf("update = %#v", update)
		}
		// Spend travels in the agent's own extension field, because the protocol
		// has nowhere to put it.
		if update.Usage.Total() != 150 || update.Usage.InputTokens != 120 {
			t.Errorf("usage = %#v", update.Usage)
		}
	default:
		t.Fatal("the tool call never reached the client")
	}
}

// Content arrives as one block or as a list of them, and a list is joined rather
// than truncated to its first entry.
func TestContentIsReadInEitherShape(t *testing.T) {
	var one ContentBlock
	if err := json.Unmarshal([]byte(`{"type":"text","text":"안녕"}`), &one); err != nil || one.Text != "안녕" {
		t.Errorf("object form = %#v, err = %v", one, err)
	}
	var many ContentBlock
	if err := json.Unmarshal([]byte(`[{"type":"text","text":"안"},{"type":"text","text":"녕"}]`), &many); err != nil || many.Text != "안녕" {
		t.Errorf("list form = %#v, err = %v", many, err)
	}
	var empty ContentBlock
	if err := json.Unmarshal([]byte(`[]`), &empty); err != nil || empty.Text != "" {
		t.Errorf("empty list = %#v, err = %v", empty, err)
	}
	var absent ContentBlock
	if err := json.Unmarshal([]byte(`null`), &absent); err != nil {
		t.Errorf("null = %v", err)
	}
}

// An update whose body this package has never seen still reaches the caller with
// what kind of thing it was. Dropping it whole is how the tool calls above went
// missing, and counting it is the difference between a run that under-reports and
// one that reports nothing.
func TestAnUnfamiliarUpdateIsStillDelivered(t *testing.T) {
	client, _ := newPair(t)
	updates := make(chan SessionUpdate, 4)
	client.Update = func(update SessionUpdate) { updates <- update }
	client.notify(message{Method: "session/update", Params: []byte(
		`{"sessionId":"s","update":{"sessionUpdate":"invented_later","title":{"not":"a string"}}}`)})
	select {
	case update := <-updates:
		if update.SessionUpdate != "invented_later" {
			t.Fatalf("update = %#v", update)
		}
	default:
		t.Fatal("an update with an unfamiliar body was dropped")
	}
}
