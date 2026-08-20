package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// The shapes on the wire, reduced to what the platform reads or has to send.
// Fields an agent sends that are not named here are ignored rather than rejected:
// the protocol grows, and an adapter that fails on an unknown field would break
// every time an agent implements more of it.

// InitializeResult is what the agent says it can do.
type InitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image           bool `json:"image"`
			Audio           bool `json:"audio"`
			EmbeddedContext bool `json:"embeddedContext"`
		} `json:"promptCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"authMethods"`
}

// MCPServer is one tool server handed to the agent when its session opens.
//
// The runtimes here do not need it: the operator writes the bound servers into
// the agent's own settings before the Pod starts, pointed at the in-Pod policy
// gateway, and the agent reads them whether it is driven by a terminal or by this
// protocol. Passing them again would give a session two copies of every tool.
//
// It stays because the protocol has this seam and the next agent may have no
// settings file of its own to write into — an agent started as a bare command,
// with the session request as the only place to say where its tools are.
type MCPServer struct {
	Name    string            `json:"name"`
	URL     string            `json:"url,omitempty"`
	HTTPURL string            `json:"httpUrl,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     []map[string]any  `json:"env,omitempty"`
}

// ContentBlock is a piece of a prompt or of an agent's answer.
//
// It reads either shape the wire uses. An agent's message carries one block;
// a tool call carries a list of them, and a real agent sends an empty list for a
// call that has produced nothing yet. Decoding only the first shape meant every
// tool call an agent made was dropped on the floor without a word — which is
// exactly the kind of silence this type exists to prevent.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (b *ContentBlock) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	type plain ContentBlock
	if trimmed[0] == '{' {
		var one plain
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return err
		}
		*b = ContentBlock(one)
		return nil
	}
	if trimmed[0] != '[' {
		return nil
	}
	var many []plain
	if err := json.Unmarshal(trimmed, &many); err != nil {
		return err
	}
	// The blocks are joined rather than the first one taken: an agent that splits
	// its answer across blocks means all of them, and keeping one would quietly
	// truncate it.
	var text strings.Builder
	for _, item := range many {
		text.WriteString(item.Text)
		if b.Type == "" {
			b.Type = item.Type
		}
	}
	b.Text = text.String()
	return nil
}

// SessionUpdate is one thing the agent said while working.
type SessionUpdate struct {
	SessionID     string       `json:"-"`
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
	// Tool calls, so a run record can show what the agent did rather than only
	// what it concluded.
	ToolCallID string `json:"toolCallId,omitempty"`
	Title      string `json:"title,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
	// Token accounting, when the agent reports it. `used` and `size` are the
	// protocol's own context occupancy — how full the window is, not what was
	// bought. Usage is the real spend, which agents report in their own extension
	// field because the protocol has nowhere to put it.
	Used  int   `json:"used,omitempty"`
	Size  int   `json:"size,omitempty"`
	Usage Usage `json:"-"`
}

// Usage is what one exchange actually cost, as the agent's own extension reports
// it. It is read from `_meta.usage` because that is where a real agent puts it;
// an agent that reports nothing leaves it zero, and a run with nothing reported
// is recorded as unmetered rather than credited with a guess.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// Total is what the platform meters, preferring the agent's own total and
// falling back to the two halves when it reports only those.
func (u Usage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

// PermissionRequest is the agent asking before it does something.
type PermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
	} `json:"toolCall"`
	Options []PermissionOption `json:"options"`
}

// PermissionOption is one answer the agent will accept. The kinds are the
// protocol's: allow_once, allow_always, reject_once, reject_always.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionOutcome is the client's answer.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// Allow and Deny build an outcome from the options the agent offered, preferring
// the narrowest one that says what the caller decided. An agent may offer only
// some of the kinds, so the choice is made from what is actually there rather
// than from a fixed id.
func Allow(options []PermissionOption) PermissionOutcome {
	return choose(options, []string{"allow_once", "allow_always"})
}

func Deny(options []PermissionOption) PermissionOutcome {
	return choose(options, []string{"reject_once", "reject_always"})
}

func choose(options []PermissionOption, kinds []string) PermissionOutcome {
	for _, kind := range kinds {
		for _, option := range options {
			if option.Kind == kind {
				return PermissionOutcome{Outcome: "selected", OptionID: option.OptionID}
			}
		}
	}
	// Nothing of that kind on offer: cancelling is the only honest answer, and it
	// ends the turn rather than picking something that means the opposite.
	return PermissionOutcome{Outcome: "cancelled"}
}

// Initialize negotiates the protocol version and learns what the agent can do.
//
// The client declares no filesystem and no terminal: the agent works inside its
// own Pod, where it has both directly, and an agent asking a remote client to
// read files it can open itself would be slower and no safer.
func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	var result InitializeResult
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return result, err
	}
	if result.ProtocolVersion != 0 && result.ProtocolVersion != ProtocolVersion {
		return result, errors.New("에이전트가 지원하는 ACP 버전이 달라 연결할 수 없습니다")
	}
	return result, nil
}

// Authenticate runs the agent's own login step when it says one is required.
func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	return c.call(ctx, "authenticate", map[string]any{"methodId": methodID}, nil)
}

// NewSession opens a conversation rooted at a working directory.
func (c *Client) NewSession(ctx context.Context, cwd string, servers []MCPServer) (string, error) {
	if servers == nil {
		servers = []MCPServer{}
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": servers}, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", errors.New("에이전트가 세션을 열어 주지 않았습니다")
	}
	return result.SessionID, nil
}

// Prompt sends one turn and returns why the agent stopped.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	var result struct {
		StopReason string `json:"stopReason"`
	}
	params := map[string]any{
		"sessionId": sessionID,
		"prompt":    []ContentBlock{{Type: "text", Text: text}},
	}
	if err := c.call(ctx, "session/prompt", params, &result); err != nil {
		return "", err
	}
	return result.StopReason, nil
}

// Cancel asks the agent to stop the current turn. It is a notification: the
// agent still ends the turn with its own stop reason.
func (c *Client) Cancel(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writer.Encode(message{JSONRPC: "2.0", Method: "session/cancel", Params: mustJSON(map[string]any{"sessionId": sessionID})})
}
