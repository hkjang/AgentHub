package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// The parser above reads a record this package believes in. This one drives the
// real agent, in the image the platform ships, through the same wrapper an exec
// would use — so what is parsed is what the agent actually writes.
//
// Skipped unless an image is named, because it needs a container runtime:
//
//	AGENTHUB_HOLMES_IMAGE=agenthub-holmes:v0.1.0 go test ./internal/execution/ -run Live -v
func TestLiveInvestigationProducesTheRecordThisParserReads(t *testing.T) {
	image := os.Getenv("AGENTHUB_HOLMES_IMAGE")
	if image == "" {
		t.Skip("set AGENTHUB_HOLMES_IMAGE to run this against the real agent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The argv the runner builds, with the wrapper the image ships at the front of
	// it — read from the same function rather than repeated, so a command that
	// drifted from the one production uses would prove the wrong thing.
	goal := store.AgentGoal{MaxSteps: 3, ApprovalMode: "default"}
	argv := investigateCommand(runtimetype.Holmes, goal, resolvedModel{ModelName: "stand-in"}, "why is the checkout service failing?")

	script := `printf '%s' "$STAND_IN_MODEL" > /tmp/model.py && python3 /tmp/model.py & ` +
		`until curl -sf -m 1 http://127.0.0.1:7997/v1/models >/dev/null; do sleep 0.2; done; ` +
		`mkdir -p "$HOLMES_CONFIG_HOME" && printf '%s' "$HOLMES_CONFIG" > "$HOLMES_CONFIG_HOME/config.yaml"; ` +
		`exec "$@"`
	args := []string{"run", "--rm", "--entrypoint", "sh",
		// The security profile the platform actually runs these under. A wrapper
		// that needed to write next to the binary would pass everywhere except
		// production.
		"--read-only", "--tmpfs", "/tmp", "--tmpfs", "/home/agent",
		"-e", "OPENAI_API_KEY=stand-in",
		"-e", "HOLMES_CONFIG_HOME=/home/agent/.holmes",
		"-e", "ENABLED_BY_DEFAULT_TOOLSETS=internet",
		"-e", "HOLMES_CONFIG=" + standInHolmesConfig,
		"-e", "STAND_IN_MODEL=" + standInInvestigationModel,
		image, "-c", script, "sh"}
	args = append(args, argv...)

	command := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Skipf("no container runtime here: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("the wrapper failed: %v — %s", err, stderr.String())
	}

	// Stdout must be the record and nothing else. That is the wrapper's whole job:
	// the agent renders its answer for a person while it works, and a parser
	// hunting for JSON inside prose is a parser that breaks on the next release.
	if !json.Valid([]byte(strings.TrimSpace(stdout.String()))) {
		t.Fatalf("stdout was not one JSON document:\n%s", first(stdout.String(), 600))
	}
	report, err := parseInvestigation(stdout.String(), stderr.String(), 0)
	if err != nil {
		t.Fatalf("parse: %v — %s", err, first(stderr.String(), 400))
	}
	if report.Conclusion == "" {
		t.Error("the investigation reported no conclusion")
	}
	if len(report.Evidence) == 0 {
		t.Error("no evidence reached the run record, so the conclusion could not be checked")
	} else if report.Evidence[0].Tool == "" || report.Evidence[0].Toolset == "" {
		t.Errorf("evidence arrived without saying what produced it: %#v", report.Evidence[0])
	}
	// Real usage, which is what lets an investigation be metered like other work.
	if report.TotalTokens == 0 {
		t.Error("the agent reported no token usage")
	}
	t.Logf("conclusion %q, %d pieces of evidence from %v, %d tokens over %d model calls",
		first(report.Conclusion, 40), len(report.Evidence), report.toolsets(), report.TotalTokens, report.LLMCalls)
}

// first trims for a log line, counting characters rather than bytes: cutting
// Korean text mid-rune turns the message into mojibake exactly when somebody is
// reading it to find out what went wrong.
func first(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// standInHolmesConfig is the shape the operator generates: the model by the name
// the agent's client understands, and the endpoint it should reach.
const standInHolmesConfig = `{"model": "openai/stand-in", "api_base": "http://127.0.0.1:7997/v1"}`

// standInInvestigationModel answers like a model that decides to look something
// up before concluding, so the record carries a tool call to read back.
const standInInvestigationModel = `
import json, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_GET(self):
        self.reply({'object': 'list', 'data': [{'id': 'stand-in', 'object': 'model'}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        messages = body.get('messages') or []
        called = any(m.get('role') == 'tool' or m.get('tool_calls') for m in messages)
        names = [(t.get('function') or {}).get('name') for t in (body.get('tools') or [])]
        pick = next((n for n in names if n == 'fetch_webpage'), None)
        if pick and not called:
            finish = 'tool_calls'
            delta = {'role': 'assistant', 'content': '증거를 모으겠습니다.', 'tool_calls': [{
                'index': 0, 'id': 'call_stand_in_1', 'type': 'function',
                'function': {'name': pick,
                             'arguments': json.dumps({'url': 'http://127.0.0.1:7997/v1/models'})}}]}
        else:
            finish = 'stop'
            delta = {'role': 'assistant',
                     'content': '# 근본 원인\n\ncheckout 파드가 메모리 한도에 걸려 재시작했습니다.'}
        usage = {'prompt_tokens': 300, 'completion_tokens': 80, 'total_tokens': 380}
        frame = {'id': 'chat-stand-in', 'created': int(time.time()), 'model': body.get('model', 'stand-in')}
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
