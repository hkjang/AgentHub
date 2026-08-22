package execution

import (
	"fmt"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// One headless agent's command line and output, per runtime.
//
// The CLI backend was written for Qwen Code and read as though it were written
// for CLI agents in general: cliCommand appended `-p`, `--approval-mode`,
// `--output-format json`, `--max-session-turns`, `--max-tool-calls` and
// `--max-wall-time` to whatever wrapper the descriptor named, and parseCLIRun
// expected the JSON array Qwen Code emits.
//
// Those are one agent's flags and one agent's output format. A second runtime
// declaring `cli` in its descriptor would have been handed Qwen Code's command
// line — flags it has never heard of — and the failure would arrive as a usage
// error from a binary the operator never typed. Nothing would have said which
// part was wrong.
//
// So the backend asks the runtime how it is spoken to. What stays shared is
// everything that is genuinely the platform's: the prompt, the DLP scan on both
// sides, the run step, the metering decision, the artifacts, the verdict.
type cliAdapter interface {
	// Command builds argv for one run, starting from the wrapper the image ships.
	Command(base []string, goal store.AgentGoal, model resolvedModel, prompt string) []string
	// Parse turns one execution into an answer or into a failure somebody can act
	// on. The answer is what the evaluator judges.
	Parse(stdout, stderr string, exitCode int) (cliRun, error)
	// Retryable says whether this exit code is worth another attempt. A guardrail
	// the agent enforced is not: the same limits would stop it in the same place.
	Retryable(exitCode int) bool
}

// cliAdapters is which agent each runtime speaks. A runtime whose descriptor
// offers RunnerCLI and which is missing here is a runtime the platform cannot
// drive, and it says so rather than guessing.
var cliAdapters = map[string]cliAdapter{
	runtimetype.QwenCode: qwenCodeCLI{},
	runtimetype.Jupyter:  qwenCodeCLI{},
}

// adapterFor names the agent this runtime is driven as.
func adapterFor(runtimeType string) (cliAdapter, error) {
	adapter, ok := cliAdapters[runtimeType]
	if !ok {
		return nil, fmt.Errorf("%s 런타임을 헤드리스로 실행하는 방법이 정의되어 있지 않습니다", runtimeType)
	}
	return adapter, nil
}
