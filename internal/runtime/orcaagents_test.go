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

// And it answers the prompt that stops the agent from starting at all.
//
// The fabric refuses to start an agent whose terminal is waiting on a keypress,
// and codex offers an update the moment a newer one is published — so an image
// that ran workers the day it was built stops running them days later, with
// `Agent startup blocked: codex-update-prompt` and nothing an operator can do
// about it from here.
//
// Measured on a cluster: every worker failed at startup with exactly that, and
// started normally once `dismissed_version` was recorded.
func TestTheFabricsAgentsAreNotBlockedByAnUpdateOffer(t *testing.T) {
	body, err := os.ReadFile("../../deploy/runtime/orca-agents-configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if !strings.Contains(script, "version.json") {
		t.Error("the script does not touch the file codex keeps the offer in, so the prompt still blocks every worker")
	}
	if !strings.Contains(script, "dismissed_version") {
		t.Error("the offer is not recorded as seen; codex asks again and the fabric fails the worker")
	}
	// Recording the installed version as dismissed would answer a prompt codex
	// never shows. What blocks a worker is the newer version codex found, and
	// that is the one in latest_version.
	if !strings.Contains(script, `state["dismissed_version"] = latest`) {
		t.Error("the version dismissed is not the one codex is offering")
	}
	// The whole point is that it runs again on a Pod started long after the image
	// was built, so it cannot be a build step.
	if strings.Contains(script, "RUN ") {
		t.Error("the answer is baked at build time, which is exactly when it is not needed")
	}
}
