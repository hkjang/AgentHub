package operator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The test beside this one checks the generated configuration against what this
// package believes the agent reads. This one checks it against the agent.
//
// It is the difference that matters here: every field in that file was a guess
// until it was handed to the real binary, and two of them were wrong on the first
// try — the address was written beside `config` instead of inside it, and the
// transport was spelled with an underscore. Both produced a runtime that started
// cleanly with a toolset that silently was not there.
//
// Skipped unless an image is named, because it needs a container runtime:
//
//	AGENTHUB_HOLMES_IMAGE=agenthub-holmes:v0.1.0 go test ./internal/operator/ -run Live -v
func TestLiveHolmesLoadsTheConfigurationThisOperatorGenerates(t *testing.T) {
	image := os.Getenv("AGENTHUB_HOLMES_IMAGE")
	if image == "" {
		t.Skip("set AGENTHUB_HOLMES_IMAGE to run this against the real agent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var value spec
	value.Runtime.Type = runtimetype.Holmes
	value.Model.Name = "stand-in"
	value.Model.BaseURL = "http://127.0.0.1:7997/v1"
	// The endpoint an in-Pod binding actually gets: loopback, because the tool
	// policy gateway runs beside the agent in the same Pod.
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://127.0.0.1:7998/mcp"}}
	generated := runtimeConfigs("agent-runtime-dev", "rt-1", value)[configHolmes]

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte(generated), 0o644); err != nil {
		t.Fatalf("write the generated configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mcp.py"), []byte(standInMCPServer), 0o644); err != nil {
		t.Fatalf("write the stand-in server: %v", err)
	}

	// The toolset listing is the agent's own answer to "what can I reach", which
	// is exactly the question the generated file is supposed to settle.
	script := `python3 /probe/mcp.py & sleep 1; ` +
		`mkdir -p /home/agent/.holmes && cp /probe/config.yaml /home/agent/.holmes/config.yaml; ` +
		`COLUMNS=200 holmes toolset list 2>/dev/null`
	command := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", directory+":/probe:ro",
		"-e", "OPENAI_API_KEY=stand-in",
		"-e", "HOLMES_CONFIG_HOME=/home/agent/.holmes",
		"-e", "ENABLED_BY_DEFAULT_TOOLSETS=internet",
		image, "sh", "-c", script)
	output, err := command.CombinedOutput()
	if err != nil && len(output) == 0 {
		t.Skipf("no container runtime here: %v", err)
	}
	listing := string(output)

	line := ""
	for _, candidate := range strings.Split(listing, "\n") {
		if strings.Contains(candidate, "toolbox") && strings.Contains(candidate, "│") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("the bound MCP server is not in the agent's toolset list:\n%s", listing)
	}
	if !strings.Contains(line, "enabled") {
		t.Fatalf("the agent could not use the bound MCP server: %s", strings.TrimSpace(line))
	}
	t.Logf("the agent loaded the generated configuration: %s", strings.TrimSpace(line))
}

// standInMCPServer answers just enough of the protocol for a client to load it
// and list its tools.
const standInMCPServer = `
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOOLS = [{"name": "agenthub_probe_tool", "description": "Proves the server was loaded.",
          "inputSchema": {"type": "object", "properties": {}, "required": []}}]

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        method = body.get('method')
        if method and method.startswith('notifications/'):
            self.send_response(202); self.end_headers(); return
        if method == 'initialize':
            result = {"protocolVersion": "2025-06-18",
                      "capabilities": {"tools": {"listChanged": False}},
                      "serverInfo": {"name": "agenthub-probe", "version": "0.1.0"}}
        elif method == 'tools/list':
            result = {"tools": TOOLS}
        elif method == 'tools/call':
            result = {"content": [{"type": "text", "text": "probe ok"}], "isError": False}
        else:
            result = {}
        payload = json.dumps({"jsonrpc": "2.0", "id": body.get('id'), "result": result}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        self.send_response(405); self.end_headers()

ThreadingHTTPServer(('127.0.0.1', 7998), Handler).serve_forever()
`
