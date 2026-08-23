package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// Every runner the database accepts needs its own branch in the validator.
//
// The validator ends with the flow runner's checks, reached by falling through
// everything else. A runner added without a branch lands there and is told to
// pick a flow — nonsense for it, and it sends whoever saved the Goal to the
// wrong screen looking for a setting that does not apply. That is exactly what
// happened to the protocol runner: the save was refused with "실행할 흐름을
// 선택해 주세요" and the Goal quietly stayed on the previous runner.
func TestEveryRunnerHasItsOwnBranch(t *testing.T) {
	// The list the database enforces, read from the migration that enforces it.
	migrations, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`CHECK \(runner IN \(([^)]*)\)\)`)
	runners := []string{}
	for _, entry := range migrations {
		body, err := os.ReadFile("../store/migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			runners = runners[:0]
			for _, raw := range strings.Split(match[1], ",") {
				runners = append(runners, strings.Trim(strings.TrimSpace(raw), "'"))
			}
		}
	}
	if len(runners) < 6 {
		t.Fatalf("read %d runner(s) from the migrations; this guard is not looking at them", len(runners))
	}

	source, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	validator := string(source)
	at := strings.Index(validator, "func validateRunner(")
	if at < 0 {
		t.Fatal("validateRunner is gone; this guard is reading nothing")
	}
	end := strings.Index(validator[at:], "\n// ")
	if end < 0 {
		end = len(validator) - at
	}
	body := validator[at : at+end]

	for _, runner := range runners {
		if runner == store.RunnerFlow {
			// The flow runner is the one the fall-through belongs to.
			continue
		}
		constant := runnerConstantName(runner)
		if constant == "" {
			t.Errorf("runner %q has no constant in the store package", runner)
			continue
		}
		// A branch, not a mention. The runner is also named in the list of what is
		// accepted at the top of the function, and checking for the name alone
		// would pass for a runner that is accepted and then falls through — which
		// is the bug this guard exists for.
		if !strings.Contains(body, "goal.Runner == store."+constant) {
			t.Errorf("runner %q (store.%s) has no branch in validateRunner; it would fall through and be told to pick a flow",
				runner, constant)
		}
	}

	// And the fall-through says so rather than asking a runner it does not know
	// for a flow.
	if !strings.Contains(body, "goal.Runner != store.RunnerFlow") {
		t.Error("the fall-through no longer refuses a runner it does not know; the next one added will be told to pick a flow")
	}
}

// runnerConstantName maps a database value to the constant that names it, so the
// guard reads the same list the store does rather than a copy.
func runnerConstantName(value string) string {
	for constant, name := range map[string]string{
		"RunnerProse": store.RunnerProse, "RunnerFlow": store.RunnerFlow, "RunnerCLI": store.RunnerCLI,
		"RunnerDify": store.RunnerDify, "RunnerACP": store.RunnerACP, "RunnerInvestigate": store.RunnerInvestigate,
		"RunnerReview": store.RunnerReview, "RunnerOrca": store.RunnerOrca, "RunnerRPC": store.RunnerRPC,
		"RunnerAgentServer": store.RunnerAgentServer,
	} {
		if name == value {
			return constant
		}
	}
	return ""
}
