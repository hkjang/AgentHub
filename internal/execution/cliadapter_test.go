package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// A runtime that offers the CLI backend has to have an agent the platform knows
// how to speak to.
//
// The backend was written for Qwen Code and read as though it were written for
// CLI agents in general: whatever wrapper the descriptor named was handed `-p`,
// `--approval-mode`, `--output-format json`, `--max-session-turns`,
// `--max-tool-calls` and `--max-wall-time`, and its output was parsed as the JSON
// array Qwen Code emits. A second runtime declaring `cli` would have been given
// another agent's command line, and the failure would have arrived as a usage
// error from a binary nobody typed.
func TestEveryCLIRuntimeHasAnAdapter(t *testing.T) {
	offered := 0
	for _, descriptor := range runtimetype.Descriptors() {
		if !runtimetype.SupportsRunner(descriptor.Type, runtimetype.RunnerCLI) {
			continue
		}
		offered++
		if _, err := adapterFor(descriptor.Type); err != nil {
			t.Errorf("%s offers the CLI backend and nothing knows how to drive it: %v", descriptor.Type, err)
		}
	}
	if offered == 0 {
		t.Fatal("no runtime offers the CLI backend; this guard is reading nothing")
	}
}

// And the reverse: an adapter for a runtime that does not offer the backend is
// either a runtime somebody forgot to finish or an adapter nobody can reach.
func TestEveryAdapterBelongsToARuntimeThatOffersTheBackend(t *testing.T) {
	for runtime := range cliAdapters {
		if !runtimetype.SupportsRunner(runtime, runtimetype.RunnerCLI) {
			t.Errorf("%s has a CLI adapter but its descriptor does not offer the CLI backend; nothing can reach it", runtime)
		}
	}
}

// An unknown runtime is refused rather than handed somebody else's flags. This is
// the whole point of the seam: the answer to "how do I run this headlessly" is
// either known or said out loud.
func TestAnUnknownRuntimeIsRefusedRatherThanGuessed(t *testing.T) {
	adapter, err := adapterFor("something-nobody-taught-it")
	if err == nil {
		t.Fatalf("an unknown runtime was given an adapter: %#v", adapter)
	}
	if !strings.Contains(err.Error(), "something-nobody-taught-it") {
		t.Errorf("the refusal does not name the runtime: %v", err)
	}
}

// The Goal's guardrails have to reach the agent in that agent's own vocabulary.
// Qwen Code's are asserted next door; this one pins that the adapter starts from
// the wrapper the image ships rather than inventing a path, because the wrapper is
// what supplies the working directory and the PATH an exec does not have.
func TestTheAdapterStartsFromTheImagesWrapper(t *testing.T) {
	base := runtimetype.RunnerCommand(runtimetype.QwenCode, runtimetype.RunnerCLI)
	if len(base) == 0 {
		t.Fatal("the descriptor names no CLI wrapper for Qwen Code")
	}
	command := qwenCodeCLI{}.Command(base, store.AgentGoal{}, resolvedModel{}, "작업")
	if len(command) < len(base) || command[0] != base[0] {
		t.Errorf("the command does not begin with the image's wrapper: %v", command)
	}
	// And it must not scribble on the descriptor's own slice: the next run would
	// inherit the last run's flags.
	again := runtimetype.RunnerCommand(runtimetype.QwenCode, runtimetype.RunnerCLI)
	if len(again) != len(base) {
		t.Errorf("building a command mutated the descriptor's argv: %v", again)
	}
}
