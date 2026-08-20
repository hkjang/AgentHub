package operator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/acp"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The generated configuration handed to the real agent, with a real browser
// beside it — which is the only way to find out whether this runtime works at
// all.
//
// Three things here were discovered rather than assumed, and each would have
// shipped as a runtime that starts cleanly and fails on its first real task.
// Chromium does not start inside an unprivileged container with its own sandbox
// on. The agent's browser session cannot find a browser by itself, because it
// looks for a DevTools port file that current Chromium does not write — it has
// to be given the websocket URL, which is what the instructions file in the
// image is for. And the MCP block is the one OpenCode uses, because this agent
// is that agent's fork.
//
//	AGENTHUB_BROWSERCODE_IMAGE=agenthub-browsercode:v0.1.0 go test ./internal/operator/ -run Live -v
func TestLiveBrowserCodeDrivesABrowserWithTheGeneratedConfiguration(t *testing.T) {
	image := os.Getenv("AGENTHUB_BROWSERCODE_IMAGE")
	if image == "" {
		t.Skip("set AGENTHUB_BROWSERCODE_IMAGE to run this against the real agent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	var value spec
	value.Runtime.Type = runtimetype.BrowserCode
	value.Model.Name = "stand-in"
	value.Model.BaseURL = "http://127.0.0.1:7997/v1"
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://127.0.0.1:7998/mcp"}}
	generated := runtimeConfigs("agent-runtime-dev", "rt-1", value)[configBcode]

	directory := t.TempDir()
	for name, content := range map[string]string{
		"bcode.json": generated,
		"model.py":   standInBrowserModel,
		"mcp.py":     standInMCPServer,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// The browser the adapter starts, then the agent as a protocol peer — the same
	// two things the runtime runs, started the same way.
	script := `mkdir -p /home/agent/.config/bcode && cp /probe/bcode.json /home/agent/.config/bcode/bcode.json && ` +
		`chromium --headless --no-sandbox --disable-dev-shm-usage ` +
		`--remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 ` +
		`--user-data-dir=/home/agent/.chrome-profile about:blank >/tmp/chromium.log 2>&1 & ` +
		`python3 /probe/model.py >/out/model.log 2>&1 & python3 /probe/mcp.py >/out/mcp.log 2>&1 & ` +
		`until curl -sf -m 1 http://127.0.0.1:9222/json/version >/dev/null; do sleep 0.3; done; ` +
		`until curl -sf -m 1 http://127.0.0.1:7997/v1/models >/dev/null; do sleep 0.3; done; ` +
		`cd /workspace && exec ` + strings.Join(runtimetype.RunnerCommand(runtimetype.BrowserCode, runtimetype.RunnerACP), " ")

	out := t.TempDir()
	if err := os.Chmod(out, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-i",
		"-v", directory+":/probe:ro", "-v", out+":/out",
		"--entrypoint", "sh", image, "-c", script)
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

	client := acp.New(stdout, stdin)
	go client.Run(ctx)
	// The platform answers the agent's permission requests; here every one is
	// allowed, because what is under test is whether the browser works at all.
	client.Permission = func(request acp.PermissionRequest) acp.PermissionOutcome {
		return acp.Allow(request.Options)
	}
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v — %s", err, tail(complaints.String(), 400))
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatalf("session/new: %v — %s", err, tail(complaints.String(), 400))
	}
	if _, err := client.Prompt(ctx, session, "Attach to the browser and report the page url"); err != nil {
		t.Fatalf("session/prompt: %v — %s", err, tail(complaints.String(), 400))
	}
	output := []byte(complaints.String())

	log, readErr := os.ReadFile(filepath.Join(out, "model.log"))
	if readErr != nil {
		t.Fatalf("the stand-in model wrote nothing: %v\n%s", readErr, tail(string(output), 600))
	}
	transcript := string(log)

	// The bound MCP server's tool reached the model, namespaced under the server's
	// name — which is how this agent presents them.
	if !strings.Contains(transcript, "toolbox_agenthub_probe_tool") {
		t.Errorf("the bound MCP server's tools were not offered to the model:\n%s", tail(transcript, 400))
	}
	// The instructions file in the image reached the system prompt, which is the
	// only way the agent learns where the browser is. The marker is a fragment
	// that cannot be broken by the file's own line wrapping — the first attempt
	// looked for a phrase that spans a newline in the shipped markdown and
	// reported a failure that was not one.
	if !strings.Contains(transcript, "browser note: True") {
		t.Errorf("the browser instructions never reached the agent:\n%s", tail(transcript, 400))
	}
	// And the browser answered. This is the one that proves the runtime: a real
	// Chromium, driven over CDP from inside the container.
	if !strings.Contains(transcript, "TARGETS OK") {
		t.Fatalf("the agent could not drive the browser:\n%s\n--- container ---\n%s",
			tail(transcript, 800), tail(string(output), 800))
	}
	t.Log("the agent attached to the browser this image runs and queried its targets")
}

func tail(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return "…" + string(runes[len(runes)-limit:])
}

// standInBrowserModel answers like a model that reaches for the browser tool,
// using the snippet the image's instructions describe.
const standInBrowserModel = `
import json, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CODE = ("const info = await (await fetch('http://127.0.0.1:9222/json/version')).json();"
        "await session.connect({ wsUrl: info.webSocketDebuggerUrl });"
        "const r = await session._call('Target.getTargets', {});"
        "console.log('TARGETS OK ' + JSON.stringify(r).slice(0, 120));"
        "return 'ok';")

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_GET(self):
        self.reply({'object': 'list', 'data': [{'id': 'stand-in', 'object': 'model'}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        names = [(t.get('function') or {}).get('name') for t in (body.get('tools') or [])]
        messages = body.get('messages') or []
        print('tools: ' + ','.join(n for n in names if n), flush=True)
        system = ' '.join(str(m.get('content')) for m in messages if m.get('role') == 'system')
        print('browser note: ' + str('9222/json/version' in system), flush=True)
        called = any(m.get('role') == 'tool' or m.get('tool_calls') for m in messages)
        if called:
            print('tool said: ' + str(messages[-1].get('content'))[:400], flush=True)
        if 'browser_execute' in names and not called:
            finish = 'tool_calls'
            delta = {'role': 'assistant', 'content': '브라우저에 연결하겠습니다.', 'tool_calls': [{
                'index': 0, 'id': 'call_browser_1', 'type': 'function',
                'function': {'name': 'browser_execute',
                             'arguments': json.dumps({'code': CODE, 'description': 'attach and list targets'})}}]}
        else:
            finish, delta = 'stop', {'role': 'assistant', 'content': '브라우저를 확인했습니다.'}
        usage = {'prompt_tokens': 200, 'completion_tokens': 50, 'total_tokens': 250}
        frame = {'id': 'chat', 'created': int(time.time()), 'model': body.get('model', 'stand-in')}
        if not body.get('stream'):
            self.reply(dict(frame, object='chat.completion', usage=usage,
                            choices=[{'index': 0, 'message': delta, 'finish_reason': finish}]))
            return
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        for item in (dict(frame, object='chat.completion.chunk',
                          choices=[{'index': 0, 'delta': delta, 'finish_reason': None}]),
                     dict(frame, object='chat.completion.chunk', usage=usage,
                          choices=[{'index': 0, 'delta': {}, 'finish_reason': finish}])):
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
