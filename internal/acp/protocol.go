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
	// Images the block carried. A tool call is where they appear in practice —
	// a browser agent's screenshot is the evidence for what it says it saw, and
	// keeping only the sentence next to it throws the evidence away.
	Images []Image `json:"-"`
}

// Image is a picture the agent produced, as the wire carries it: base64 with its
// media type. It is not decoded here because whoever stores it has to decide what
// is too large, and that is not a decision the protocol layer can make.
type Image struct {
	MimeType string
	Data     string
}

func (b *ContentBlock) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}
	b.absorb(trimmed, 0)
	return nil
}

// wireBlock is every shape a block arrives in. A tool call's content is a union —
// `{"type":"content","content":{…}}` wrapping the block that matters — so the
// nested field is followed rather than assuming the outer object is the content.
type wireBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MimeType string          `json:"mimeType,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}

// absorb reads one block, or a list of them, into this one.
//
// The blocks are joined rather than the first one taken: an agent that splits its
// answer across blocks means all of them, and keeping one would quietly truncate
// it. The depth bound is not defensive tidiness — the nested field is read from
// whatever the agent sent, and an agent that nests content in itself would
// otherwise take the process down with it.
func (b *ContentBlock) absorb(raw []byte, depth int) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || depth > 4 {
		return
	}
	switch trimmed[0] {
	case '[':
		var many []json.RawMessage
		if json.Unmarshal(trimmed, &many) != nil {
			return
		}
		for _, item := range many {
			b.absorb(item, depth+1)
		}
	case '{':
		var one wireBlock
		if json.Unmarshal(trimmed, &one) != nil {
			return
		}
		if b.Type == "" && (one.Text != "" || one.Data != "") {
			b.Type = one.Type
		}
		b.Text += one.Text
		if one.Data != "" {
			b.Images = append(b.Images, Image{MimeType: one.MimeType, Data: one.Data})
		}
		b.absorb(one.Content, depth+1)
	}
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
	// RawInput is what the agent is about to run, in the tool's own shape. It is
	// kept because the title is usually the tool's *name* — `browser_execute`,
	// `developer__shell` — and the thing an operator wants to allow or refuse is
	// the command inside it.
	RawInput json.RawMessage `json:"rawInput,omitempty"`
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
