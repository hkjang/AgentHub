// Package acp speaks the Agent Client Protocol.
//
// ACP is how an editor talks to a coding agent: JSON-RPC 2.0 over the agent's
// stdin and stdout, with the editor as the client and the agent as a subprocess.
// Zed defined it so that one editor could drive many agents; the same property is
// why it is here. Every agent added so far has meant an adapter — a start
// command, a configuration file, a way to read its answer. An agent that speaks
// ACP needs none of that: the platform already knows how to hold the
// conversation, so adding one becomes a matter of saying which command to run.
//
// This package is only the client half, and deliberately small: initialise, open
// a session, send one prompt, and answer what the agent asks while it works. It
// holds no opinion about policy — the caller decides what to permit — because
// permission is the platform's business and protocol plumbing is not the place
// for it.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ProtocolVersion is the major version this client implements.
const ProtocolVersion = 1

// Client is one conversation with one agent.
type Client struct {
	writer *json.Encoder
	reader *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rawResponse

	// Permission is asked whatever the agent wants to do that needs consent. The
	// caller answers from the Goal's settings; there is nobody to ask at three in
	// the morning, which is exactly why the answer has to be decided in advance.
	Permission func(request PermissionRequest) PermissionOutcome
	// Update receives everything the agent says while it works.
	Update func(update SessionUpdate)

	closed chan struct{}
	once   sync.Once
	readEr error
}

// New wires a client onto an agent's pipes.
func New(stdout io.Reader, stdin io.Writer) *Client {
	return &Client{
		writer:  json.NewEncoder(stdin),
		reader:  bufio.NewReaderSize(stdout, 1<<20),
		nextID:  1,
		pending: map[int]chan rawResponse{},
		closed:  make(chan struct{}),
	}
}

// message is one line on the wire. A JSON-RPC frame is a request, a response or
// a notification, and which one it is has to be worked out from what is present:
// the protocol does not label them.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("agent error %d: %s", e.Code, e.Message) }

type rawResponse struct {
	result json.RawMessage
	err    error
}

// Run reads the agent's side of the conversation until it ends.
//
// It has to run for as long as the client is used: an agent's answer to a request
// and its notifications arrive on the same stream, and a request the agent makes
// mid-turn has to be answered while the caller is still waiting for its own
// reply. Everything else here assumes this is running.
func (c *Client) Run(ctx context.Context) {
	defer c.once.Do(func() { close(c.closed) })
	for {
		line, err := c.reader.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(ctx, line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.readEr = err
			}
			c.failPending(err)
			return
		}
	}
}

func (c *Client) dispatch(ctx context.Context, line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return
	}
	var frame message
	if err := json.Unmarshal([]byte(trimmed), &frame); err != nil {
		// Agents print to stdout for other reasons — a banner, a warning. A line
		// that is not JSON-RPC is not an error, it is not ours.
		return
	}
	switch {
	case frame.Method != "" && frame.ID != nil:
		c.answer(ctx, frame)
	case frame.Method != "":
		c.notify(frame)
	case frame.ID != nil:
		c.complete(frame)
	}
}

// answer replies to something the agent asked the client to do.
func (c *Client) answer(ctx context.Context, frame message) {
	switch frame.Method {
	case "session/request_permission":
		var request PermissionRequest
		_ = json.Unmarshal(frame.Params, &request)
		outcome := PermissionOutcome{Outcome: "cancelled"}
		if c.Permission != nil {
			outcome = c.Permission(request)
		}
		c.reply(frame.ID, map[string]any{"outcome": outcome})
	case "fs/read_text_file", "fs/write_text_file", "terminal/create":
		// The client declares it cannot do these, so an agent asking anyway is
		// told plainly rather than left waiting.
		c.replyError(frame.ID, -32601, "this client does not provide "+frame.Method)
	default:
		c.replyError(frame.ID, -32601, "method not found: "+frame.Method)
	}
}

func (c *Client) notify(frame message) {
	if frame.Method != "session/update" || c.Update == nil {
		return
	}
	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(frame.Params, &envelope); err != nil {
		return
	}
	// The update is either nested under "update" or inlined in params, depending
	// on the agent; both are read rather than betting on one.
	body := envelope.Update
	if len(body) == 0 {
		body = frame.Params
	}
	// The kind is read on its own first, so an update whose body has a shape this
	// package has never seen still reaches the caller with what it is. Dropping it
	// entirely is how a real agent's tool calls went unrecorded once, and the run
	// looked like an agent that had done nothing.
	var kind struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(body, &kind); err != nil {
		return
	}
	update := SessionUpdate{SessionUpdate: kind.SessionUpdate}
	if err := json.Unmarshal(body, &update); err != nil {
		update = SessionUpdate{SessionUpdate: kind.SessionUpdate}
	}
	// Real token spend travels in the agent's own extension field, because the
	// protocol has nowhere to put it.
	var meta struct {
		Meta struct {
			Usage Usage `json:"usage"`
		} `json:"_meta"`
	}
	if json.Unmarshal(body, &meta) == nil {
		update.Usage = meta.Meta.Usage
	}
	update.SessionID = envelope.SessionID
	c.Update(update)
}

func (c *Client) complete(frame message) {
	c.mu.Lock()
	waiter, found := c.pending[*frame.ID]
	delete(c.pending, *frame.ID)
	c.mu.Unlock()
	if !found {
		return
	}
	if frame.Error != nil {
		waiter <- rawResponse{err: frame.Error}
		return
	}
	waiter <- rawResponse{result: frame.Result}
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil || errors.Is(err, io.EOF) {
		err = errors.New("에이전트가 응답을 끝내지 않고 종료했습니다")
	}
	for id, waiter := range c.pending {
		waiter <- rawResponse{err: err}
		delete(c.pending, id)
	}
}

// call sends a request and waits for its answer.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	waiter := make(chan rawResponse, 1)
	c.pending[id] = waiter
	err := c.writer.Encode(message{JSONRPC: "2.0", ID: &id, Method: method, Params: mustJSON(params)})
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("%s 요청을 보내지 못했습니다: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		if c.readEr != nil {
			return c.readEr
		}
		return errors.New("에이전트와의 연결이 끊겼습니다")
	case response := <-waiter:
		if response.err != nil {
			return response.err
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(response.result, out)
	}
}

func (c *Client) reply(id *int, result any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writer.Encode(message{JSONRPC: "2.0", ID: id, Result: mustJSON(result)})
}

func (c *Client) replyError(id *int, code int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writer.Encode(message{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: text}})
}

func mustJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
