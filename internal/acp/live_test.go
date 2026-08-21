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

// The whole turn, against every real agent: it says something, reaches for a
// tool, asks permission, acts on the answer and finishes — with the model it
// talks to standing in for the platform's gateway.
//
// This is the test that found what no stand-in would have. Qwen Code's tool calls
// carry a list of content blocks where a message carries one, so decoding only
// the message shape dropped every tool call in silence. Started without an
// explicit approval mode, it approves its own tool calls and never asks. And
// Goose identifies its requests with a string rather than a number, which a
// client that insisted on numbers ignored — leaving the agent waiting for an
// answer to a permission request that was never coming.
func TestLiveAgentAsksBeforeUsingATool(t *testing.T) {
	// Skipped as a whole when there is no agent to drive, rather than passing with
	// every subtest skipped inside it. Go reports a parent whose children all
	// skipped as PASS, so a nightly run whose images failed to build looked
	// exactly like one that had proved something.
	configured := 0
	for _, under := range liveAgents {
		if os.Getenv(under.imageEnv) != "" {
			configured++
		}
	}
	if configured == 0 {
		t.Skip("set AGENTHUB_ACP_IMAGE or AGENTHUB_ACP_GOOSE_IMAGE to run this against a real agent")
	}
	for index, under := range liveAgents {
		t.Run(under.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			client, complaints := liveContainer(ctx, t, index, "http://127.0.0.1:7997")

			var mu sync.Mutex
			var said strings.Builder
			var toolCalls, spend int
			var toolStatus string
			client.Update = func(update SessionUpdate) {
				mu.Lock()
				defer mu.Unlock()
				spend += update.Usage.Total()
				switch update.SessionUpdate {
				case "agent_message_chunk":
					said.WriteString(update.Content.Text)
				case "tool_call":
					toolCalls++
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

			mu.Lock()
			calls := toolCalls
			mu.Unlock()
			if calls == 0 {
				t.Fatalf("the agent made no tool call at all, so there was nothing to ask about — %s", complaints.String())
			}
			if under.asks {
				select {
				case request := <-asked:
					if request.ToolCall.Title == "" {
						t.Error("the agent asked permission without saying what for")
					}
					if len(request.Options) == 0 {
						t.Fatal("the agent asked permission with no answer it would accept")
					}
					// The narrowest allow is preferred over "always", so one yes does
					// not become a standing yes for the rest of the session.
					allow := Allow(request.Options)
					if allow.OptionID == "" {
						t.Errorf("no allow option could be chosen from %+v", request.Options)
					} else if kindOf(request.Options, allow.OptionID) != "allow_once" {
						t.Errorf("allow chose %q, a %q option", allow.OptionID, kindOf(request.Options, allow.OptionID))
					}
					if deny := Deny(request.Options); deny.OptionID == "" {
						t.Errorf("no refusal could be chosen from %+v", request.Options)
					}
				default:
					t.Fatal("the agent used a tool without asking — the platform's permission policy would never be consulted")
				}
			}

			mu.Lock()
			defer mu.Unlock()
			// The agent's words, assembled from the stream. Which words differ by
			// agent and that is theirs to decide: Qwen Code streams what it says
			// before reaching for the tool and then stops, having been refused;
			// Goose says nothing until the turn is over and then reports. Requiring
			// one of them would be requiring a style rather than testing assembly.
			if !strings.Contains(said.String(), "만들겠습니다") && !strings.Contains(said.String(), "마쳤습니다") {
				t.Errorf("the agent's message was not assembled: %q", said.String())
			}
			if toolCalls == 0 {
				t.Error("no tool call reached the client — the run record would show an agent that did nothing")
			}
			// Refused, and the agent recorded that rather than doing it anyway.
			if toolStatus != "failed" {
				t.Errorf("tool status after a refusal = %q", toolStatus)
			}
			// Not every agent volunteers what a turn cost, and a run whose agent
			// says nothing is recorded as unmetered rather than credited with a
			// guess — so this is reported rather than required.
			t.Logf("%s reported %d tokens of spend", under.name, spend)
		})
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

# Agents name their file-writing tool differently — write_file in one, write in
# another — so the one to call is chosen from what the agent itself offered
# rather than from a name this test hopes it uses.
PREFERRED = ('write_file', 'write', 'edit', 'replace', 'str_replace', 'create_file')

def pick(tools):
    offered = [((t.get('function') or {}).get('name'), (t.get('function') or {}).get('parameters') or {})
               for t in tools if (t.get('function') or {}).get('name')]
    for want in PREFERRED:
        for name, params in offered:
            if name == want:
                return name, params
    for want in PREFERRED:
        for name, params in offered:
            if want in name:
                return name, params
    return None, None

def arguments(params):
    # Filled from the tool's own schema, so the call is well formed whatever the
    # agent calls its arguments.
    out = {}
    props = (params or {}).get('properties') or {}
    for key in ((params or {}).get('required') or []):
        kind = (props.get(key) or {}).get('type')
        if 'path' in key or 'file' in key:
            out[key] = '/tmp/acp-probe.txt'
        elif 'content' in key or 'text' in key:
            out[key] = 'hello from a permission test'
        elif kind in ('integer', 'number'):
            out[key] = 1
        elif kind == 'boolean':
            out[key] = False
        elif kind == 'array':
            out[key] = []
        else:
            out[key] = 'acp-probe'
    return out

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass

    def do_GET(self):
        self.reply({'object': 'list', 'data': [{'id': 'stand-in', 'object': 'model'}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        messages = body.get('messages') or []
        called = any(m.get('role') == 'tool' or m.get('tool_calls') for m in messages)
        tool, params = pick(body.get('tools') or [])
        if tool and not called:
            finish = 'tool_calls'
            delta = {'role': 'assistant', 'content': '파일을 하나 만들겠습니다. ', 'tool_calls': [{
                'index': 0, 'id': 'call_stand_in_1', 'type': 'function',
                'function': {'name': tool, 'arguments': json.dumps(arguments(params))}}]}
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

// liveAgents are the real agents these tests drive, each named the way the
// platform starts it. Two of them is what makes this table worth having: every
// difference between them — a string request id where the other used a number, a
// tool kind declared where the other declares "other" — was a bug or a documented
// behaviour found by running the same test against both.
var liveAgents = []struct {
	name string
	// imageEnv names the environment variable holding the image, so a site can
	// run these against whichever tags it has built.
	imageEnv string
	// command is what the container runs. It is the descriptor's own argv,
	// written out here rather than imported, so a change to one of them shows up
	// as a test that has to be looked at.
	command string
	// env binds the agent to the model gateway by the names it reads, with
	// %BASE% and %HOST% substituted for the stand-in model's address.
	env []string
	// asks is whether this agent sends session/request_permission when started
	// this way. Both do; the field exists so an agent that does not can be added
	// without the test pretending it does.
	asks bool
}{
	{
		name: "qwencode", imageEnv: "AGENTHUB_ACP_IMAGE",
		command: "cd /tmp && exec qwen --acp --approval-mode default",
		env:     []string{"OPENAI_API_KEY=stand-in", "OPENAI_BASE_URL=%BASE%", "OPENAI_MODEL=stand-in"},
		asks:    true,
	},
	{
		name: "goose", imageEnv: "AGENTHUB_ACP_GOOSE_IMAGE",
		// GOOSE_MODE=approve is what makes it ask, and it is an environment
		// variable rather than a flag — which is why the platform's wrapper sets
		// it rather than the descriptor's argv carrying it.
		command: "cd /tmp && exec goose acp",
		env: []string{"GOOSE_MODE=approve", "GOOSE_PROVIDER=openai", "GOOSE_MODEL=stand-in",
			"OPENAI_API_KEY=stand-in", "OPENAI_HOST=%HOST%", "OPENAI_BASE_PATH=v1/chat/completions"},
		asks: true,
	},
}

// liveContainer starts one agent in its image and returns a client speaking to
// it. The model address is substituted into the agent's own environment names.
func liveContainer(ctx context.Context, t *testing.T, agent int, modelHost string) (*Client, *strings.Builder) {
	t.Helper()
	under := liveAgents[agent]
	image := os.Getenv(under.imageEnv)
	if image == "" {
		t.Skipf("set %s to run this against the real %s agent", under.imageEnv, under.name)
	}
	args := []string{"run", "--rm", "-i", "--entrypoint", "sh"}
	for _, pair := range under.env {
		pair = strings.ReplaceAll(pair, "%BASE%", modelHost+"/v1")
		pair = strings.ReplaceAll(pair, "%HOST%", modelHost)
		args = append(args, "-e", pair)
	}
	// The stand-in model runs beside the agent, in the same container, reached on
	// loopback. That is not a shortcut — it is the only arrangement that does not
	// depend on how this particular machine bridges a container to its host, and a
	// test that passes or fails on somebody's firewall rule is not a test.
	script := `printf '%s' "$STAND_IN_MODEL" > /tmp/model.py && python3 /tmp/model.py & ` +
		`until curl -sf -m 1 http://127.0.0.1:7997/v1/models >/dev/null; do sleep 0.2; done; ` + under.command
	args = append(args, "-e", "STAND_IN_MODEL="+standInModelSource, image, "-c", script)
	command := exec.CommandContext(ctx, "docker", args...)
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
	t.Cleanup(func() { _ = command.Process.Kill() })
	client := New(stdout, stdin)
	go client.Run(ctx)
	return client, &complaints
}

func liveImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("AGENTHUB_ACP_IMAGE")
	if image == "" {
		t.Skip("set AGENTHUB_ACP_IMAGE to run this against a real agent")
	}
	return image
}
