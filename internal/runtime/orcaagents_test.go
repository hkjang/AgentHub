package runtime

import (
	"os"
	"strings"
	"testing"
)

// The script that points the fabric's agents at the gateway.
//
// It exists because inheriting the environment is not the same as honouring it:
// codex 0.149.0 ignores OPENAI_BASE_URL and opens api.openai.com, and only a
// provider in its own configuration redirects it. That was measured, and the
// requests then arrived at the gateway carrying the platform's key.
//
// This is a source guard because the script runs inside a runtime image with an
// agent binary in it. What it guards are the two things that were wrong on the
// first attempt: a value TOML refuses, and a config written even when there is
// no gateway to point at.
func TestTheFabricsAgentsArePointedAtTheGateway(t *testing.T) {
	body, err := os.ReadFile("../../deploy/runtime/orca-agents-configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)

	if !strings.Contains(script, "AGENTHUB_MODEL_BASE_URL") {
		t.Error("the script does not read this deployment's model endpoint, so it cannot point anything at it")
	}
	if !strings.Contains(script, ".codex/config.toml") {
		t.Error("the script does not write the file codex actually reads; the environment variable alone does not redirect it")
	}
	if !strings.Contains(script, `wire_api = "responses"`) {
		t.Error(`the provider does not name the transport; codex refuses wire_api = "chat" outright`)
	}
	// Every value quoted. TOML refuses a bare string, and the refusal reads as
	// "Error loading config.toml" with no mention of the platform that wrote it —
	// which is exactly what the first version of this script produced.
	if strings.Contains(script, `model = "$model"`) || strings.Contains(script, "${model:+") {
		t.Error("a value is interpolated without quoting; TOML refuses a bare string and the agent will not start")
	}
	if !strings.Contains(script, `echo "model = \"$model\""`) {
		t.Error("the model line is not written with quotes around its value")
	}
	// No endpoint means no config rather than a config pointing nowhere: a
	// provider with an empty base_url is worse than none, because the agent
	// starts and fails somewhere further away from the cause.
	if !strings.Contains(script, `if [ -z "$base" ]`) {
		t.Error("the script writes a provider even when this deployment has no model endpoint")
	}
}
