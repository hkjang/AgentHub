package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The stand-in agent in acp_test.go proves the client against the protocol as
// this package understands it, which is exactly the thing that could be wrong.
// This one proves it against a real agent — Qwen Code, the same binary the
// runtime image ships — started the same way the platform starts it.
//
// It is skipped unless an image is named, because it needs a container runtime
// and because a unit test suite that pulls images is a suite people stop running.
//
//	AGENTHUB_ACP_IMAGE=agenthub-qwencode:v0.2.0 go test ./internal/acp/ -run Live -v
func TestLiveAgentSpeaksTheProtocol(t *testing.T) {
	image := liveImage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--entrypoint", "sh",
		"-e", "OPENAI_API_KEY=probe-not-a-real-key", "-e", "OPENAI_BASE_URL=http://127.0.0.1:9/v1",
		"-e", "OPENAI_MODEL=probe", image,
		"-c", "cd /tmp && exec qwen --acp")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var complaints strings.Builder
	command.Stderr = &complaints
	if err := command.Start(); err != nil {
		t.Skipf("no container runtime here: %v", err)
	}
	defer func() { _ = command.Process.Kill() }()

	client := New(stdout, stdin)
	go client.Run(ctx)

	capabilities, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v — %s", err, complaints.String())
	}
	if capabilities.ProtocolVersion != ProtocolVersion {
		t.Errorf("the agent negotiated protocol %d, this client speaks %d",
			capabilities.ProtocolVersion, ProtocolVersion)
	}
	// An agent that needs credentials says so here rather than by failing later,
	// which is what the runner turns into a message naming what to configure.
	t.Logf("agent capabilities: loadSession=%v authMethods=%d",
		capabilities.AgentCapabilities.LoadSession, len(capabilities.AuthMethods))

	session, err := client.NewSession(ctx, "/tmp", nil)
	if err != nil {
		t.Fatalf("session/new: %v — %s", err, complaints.String())
	}
	if session == "" {
		t.Fatal("the agent opened a session with no identifier")
	}
	// Cancelling a session it just opened is the one exchange that needs no model
	// endpoint, so this test proves the conversation without needing credentials.
	client.Cancel(session)
}

// The other half of the handshake: an agent whose credentials are missing refuses
// to open a session and says which method it wants. The runner turns that into a
// message naming what to configure, and it can only do that if the refusal
// arrives as an error rather than as a hang.
func TestLiveAgentRefusesWithoutCredentials(t *testing.T) {
	image := liveImage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// No model credentials in the environment at all, which is what a runtime with
	// no model endpoint bound to it looks like.
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--entrypoint", "sh", image,
		"-c", "cd /tmp && exec qwen --acp")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Skipf("no container runtime here: %v", err)
	}
	defer func() { _ = command.Process.Kill() }()

	client := New(stdout, stdin)
	go client.Run(ctx)
	capabilities, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(capabilities.AuthMethods) == 0 {
		t.Skip("this agent advertises no authentication methods")
	}
	if _, err := client.NewSession(ctx, "/tmp", nil); err == nil {
		t.Fatal("a session opened with no credentials — this test can no longer prove the refusal path")
	}
	// The advertised method is what the runner tries next, and what it names in
	// the failure when trying does not help either.
	if err := client.Authenticate(ctx, capabilities.AuthMethods[0].ID); err != nil {
		t.Logf("authenticate(%q) refused as well: %v", capabilities.AuthMethods[0].ID, err)
	}
}

// The whole turn, against the real agent: it says something, reaches for a tool,
// asks permission, acts on the answer and finishes — with the model it talks to
// standing in for the platform's gateway.
//
// This is the test that found two things no stand-in would have. The agent's tool
// calls carry a list of content blocks where a message carries one, so decoding
// only the message shape dropped every tool call in silence. And started without
// an explicit approval mode this agent approves its own tool calls and never
// asks at all — the client was never consulted, which would have made an
// unattended run's permission policy decorative.
func TestLiveAgentAsksBeforeUsingATool(t *testing.T) {
	image := liveImage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--entrypoint", "sh",
		"-e", "OPENAI_API_KEY=stand-in", "-e", "OPENAI_BASE_URL=http://127.0.0.1:7997/v1",
		"-e", "OPENAI_MODEL=stand-in", "-e", "STAND_IN_MODEL="+standInModelSource, image,
		// The stand-in model starts first and the agent takes over the process, so
		// the container ends when the conversation does. The agent's argv is the one
		// the runtime descriptor names, approval mode included.
		"-c", `printf '%s' "$STAND_IN_MODEL" > /tmp/model.py && python3 /tmp/model.py & `+
			`until curl -sf -m 1 http://127.0.0.1:7997/v1/models >/dev/null; do sleep 0.2; done; `+
			`cd /tmp && exec qwen --acp --approval-mode default`)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var complaints strings.Builder
	command.Stderr = &complaints
	if err := command.Start(); err != nil {
		t.Skipf("no container runtime here: %v", err)
	}
	defer func() { _ = command.Process.Kill() }()

	client := New(stdout, stdin)
	go client.Run(ctx)

	var mu sync.Mutex
	var said strings.Builder
	var toolCalls, spend int
	var toolKind, toolStatus string
	client.Update = func(update SessionUpdate) {
		mu.Lock()
		defer mu.Unlock()
		spend += update.Usage.Total()
		switch update.SessionUpdate {
		case "agent_message_chunk":
			said.WriteString(update.Content.Text)
		case "tool_call":
			toolCalls++
			toolKind = update.Kind
		case "tool_call_update":
			if update.Status != "" {
				toolStatus = update.Status
			}
		}
	}
	asked := make(chan PermissionRequest, 4)
	client.Permission = func(request PermissionRequest) PermissionOutcome {
		asked <- request
		return Deny(request.Options)
	}

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v — %s", err, complaints.String())
	}
	session, err := client.NewSession(ctx, "/tmp", nil)
	if err != nil {
		t.Fatalf("session/new: %v — %s", err, complaints.String())
	}
	stop, err := client.Prompt(ctx, session, "Create a file called acp-probe.txt with a greeting.")
	if err != nil {
		t.Fatalf("session/prompt: %v — %s", err, complaints.String())
	}
	if stop != "end_turn" {
		t.Errorf("stop reason = %q", stop)
	}

	select {
	case request := <-asked:
		if request.ToolCall.Title == "" {
			t.Error("the agent asked permission without saying what for")
		}
		if len(request.Options) == 0 {
			t.Fatal("the agent asked permission with no answer it would accept")
		}
		// The narrowest allow is preferred over "always", so one yes does not become
		// a standing yes for the rest of the session.
		if allow := Allow(request.Options); allow.OptionID == "" {
			t.Errorf("no allow option could be chosen from %+v", request.Options)
		} else if kindOf(request.Options, allow.OptionID) != "allow_once" {
			t.Errorf("allow chose %q, a %q option", allow.OptionID, kindOf(request.Options, allow.OptionID))
		}
	default:
		t.Fatal("the agent used a tool without asking — the platform's permission policy would never be consulted")
	}

	mu.Lock()
	defer mu.Unlock()
	// What the agent said before it reached for the tool. It does not go on to
	// announce a result here, because it was refused — which is the point.
	if !strings.Contains(said.String(), "파일을 하나 만들겠습니다") {
		t.Errorf("the agent's message was not assembled: %q", said.String())
	}
	if toolCalls == 0 {
		t.Error("no tool call reached the client — the run record would show an agent that did nothing")
	}
	if toolKind != "edit" {
		t.Errorf("tool kind = %q, want the protocol's own kind for a file change", toolKind)
	}
	// Refused, and the agent recorded that rather than doing it anyway.
	if toolStatus != "failed" {
		t.Errorf("tool status after a refusal = %q", toolStatus)
	}
	if spend == 0 {
		t.Error("the agent reported no token spend, so a real run would be recorded as unmetered")
	}
}

func kindOf(options []PermissionOption, id string) string {
	for _, option := range options {
		if option.OptionID == id {
			return option.Kind
		}
	}
	return ""
}

// standInModelSource is an OpenAI-compatible endpoint that makes the agent reach
// for a tool. It does not guess tool names: the agent sends its tool list with
// every request, so the answer is a call to whichever tool the agent itself
// offered for writing a file.
//
// It runs inside the agent's own container, beside the agent, and is reached on
// loopback. That is not a shortcut — it is the only arrangement that does not
// depend on how this particular machine bridges a container to its host, and a
// test that passes or fails on somebody's firewall rule is not a test.
const standInModelSource = `
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass

    def do_GET(self):
        self.reply({'object': 'list', 'data': [{'id': 'stand-in', 'object': 'model'}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        messages = body.get('messages') or []
        called = any(m.get('role') == 'tool' or m.get('tool_calls') for m in messages)
        tool = next((t['function']['name'] for t in (body.get('tools') or [])
                     if (t.get('function') or {}).get('name') == 'write_file'), None)
        if tool and not called:
            finish = 'tool_calls'
            delta = {'role': 'assistant', 'content': '파일을 하나 만들겠습니다. ', 'tool_calls': [{
                'index': 0, 'id': 'call_stand_in_1', 'type': 'function',
                'function': {'name': tool,
                             'arguments': json.dumps({'file_path': '/tmp/acp-probe.txt', 'content': 'hello\n'})}}]}
        else:
            finish, delta = 'stop', {'role': 'assistant', 'content': '요청한 작업을 마쳤습니다.'}
        usage = {'prompt_tokens': 120, 'completion_tokens': 30, 'total_tokens': 150}
        frame = {'id': 'chat-stand-in', 'created': int(time.time()), 'model': body.get('model', 'stand-in')}
        if not body.get('stream'):
            self.reply(dict(frame, object='chat.completion', usage=usage,
                            choices=[{'index': 0, 'message': delta, 'finish_reason': finish}]))
            return
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        chunk = dict(frame, object='chat.completion.chunk',
                     choices=[{'index': 0, 'delta': delta, 'finish_reason': None}])
        done = dict(frame, object='chat.completion.chunk', usage=usage,
                    choices=[{'index': 0, 'delta': {}, 'finish_reason': finish}])
        for item in (chunk, done):
            self.wfile.write(('data: ' + json.dumps(item) + '\n\n').encode())
        self.wfile.write(b'data: [DONE]\n\n')
        self.wfile.flush()

    def reply(self, payload):
        raw = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

ThreadingHTTPServer(('127.0.0.1', 7997), Handler).serve_forever()
`

func liveImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("AGENTHUB_ACP_IMAGE")
	if image == "" {
		t.Skip("set AGENTHUB_ACP_IMAGE to run this against a real agent")
	}
	return image
}
