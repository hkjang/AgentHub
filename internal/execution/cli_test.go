package execution

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A real answer from Qwen Code 0.21, trimmed to the fields the platform reads.
// It is captured rather than written from the documentation because the parts
// that matter here — where the answer lives, what the usage block is called —
// are the runtime's contract, not ours.
const realCLIResponse = `[
 {"type":"system","subtype":"init","uuid":"2e500d14","session_id":"2e500d14","cwd":"/workspace","tools":["read_file","write_file"]},
 {"type":"assistant","uuid":"a1","session_id":"2e500d14","message":{"role":"assistant","content":[{"type":"text","text":"2입니다."}]}},
 {"type":"result","subtype":"success","session_id":"2e500d14","is_error":false,"duration_ms":230,"num_turns":1,
  "result":"2입니다.",
  "usage":{"input_tokens":22,"output_tokens":14,"cache_read_input_tokens":0,"total_tokens":36},
  "stats":{"tools":{"totalCalls":3,"totalSuccess":3},"files":{"totalLinesAdded":12,"totalLinesRemoved":4}}}
]`

func TestParseCLIRunReadsARealAnswer(t *testing.T) {
	run, err := parseCLIRun(realCLIResponse, "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.Result != "2입니다." || run.Turns != 1 || run.SessionID != "2e500d14" {
		t.Fatalf("run = %#v", run)
	}
	// Unlike a flow, this one reports usage, which is why a CLI run is metered.
	if run.TotalTokens != 36 || run.InputTokens != 22 || run.OutputTokens != 14 {
		t.Errorf("token usage was not read: %#v", run)
	}
	if run.ToolCalls != 3 || run.LinesAdded != 12 || run.LinesRemoved != 4 {
		t.Errorf("what the agent actually did was not read: %#v", run)
	}
}

// The guardrail cases write nothing to stdout and a structured error to stderr.
// Reading only stdout would record "no output" for a run that stopped for a
// reason the person can act on.
func TestParseCLIRunExplainsGuardrails(t *testing.T) {
	budget := `{"error":{"type":"FatalBudgetExceededError","message":"Run aborted: wall-clock budget of 5s exceeded (--max-wall-time).","code":55}}`
	_, err := parseCLIRun("", budget, 55)
	if err == nil || !strings.Contains(err.Error(), "예산") {
		t.Fatalf("budget error = %v", err)
	}
	if !strings.Contains(err.Error(), "wall-clock budget") {
		t.Errorf("the agent's own words should survive: %v", err)
	}
	if retryableCLIExit(55) {
		t.Error("a budget that was exceeded once is exceeded again on a retry")
	}

	_, err = parseCLIRun("", `{"error":{"type":"FatalTurnLimitedError","message":"Reached max session turns","code":53}}`, 53)
	if err == nil || !strings.Contains(err.Error(), "단계") {
		t.Fatalf("turn limit error = %v", err)
	}
	if retryableCLIExit(53) {
		t.Error("a turn limit is not a transient failure")
	}
	// Anything else is the runtime having a bad moment, which is worth retrying.
	if !retryableCLIExit(1) {
		t.Error("an unexplained failure should be retried once")
	}
	if retryableCLIExit(cliExitInterrupt) {
		t.Error("an interrupted run must not be retried automatically")
	}
}

// An answer that parses but says the agent failed is a failure, not a result.
func TestParseCLIRunRejectsAnErrorResult(t *testing.T) {
	body := `[{"type":"result","subtype":"error","is_error":true,"result":"API key rejected"}]`
	if _, err := parseCLIRun(body, "", 0); err == nil || !strings.Contains(err.Error(), "API key rejected") {
		t.Fatalf("error result = %v", err)
	}
	// And an exit that says success with nothing in it is not a success either.
	body = `[{"type":"result","subtype":"success","is_error":false,"result":"   "}]`
	if _, err := parseCLIRun(body, "", 0); err == nil {
		t.Fatal("an empty answer must not be recorded as a result")
	}
	// Output that is not the expected array at all still produces a usable message.
	if _, err := parseCLIRun("not json", "", 1); err == nil {
		t.Fatal("unparseable output must be reported")
	}
}

// The Goal's guardrails are handed to the agent as its own budgets, so a limit
// set in the console is enforced by the thing doing the work rather than by a
// timeout that cuts it off mid-sentence.
func TestCLICommandCarriesTheGoalsGuardrails(t *testing.T) {
	goal := store.AgentGoal{
		MaxSteps: 12, MaxToolCalls: 40, MaxDurationSeconds: 600, CLIApprovalMode: "auto-edit",
	}
	command := cliCommand(goal, resolvedModel{ModelName: "qwen3-coder"}, "작업 내용")
	joined := strings.Join(command, " ")
	for _, want := range []string{
		cliBinary, "-p", "작업 내용",
		"--approval-mode auto-edit", "--output-format json",
		"--max-session-turns 12", "--max-tool-calls 40", "-m qwen3-coder",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("command is missing %q: %v", want, command)
		}
	}
	// A little under the platform's own deadline, so the agent stops itself and
	// says why instead of being killed.
	if !strings.Contains(joined, "--max-wall-time 590s") {
		t.Errorf("wall time budget = %v", command)
	}
	// The prompt is one argument. A shell string would turn a task title with a
	// quote in it into a command.
	if index := indexOf(command, "-p"); index < 0 || command[index+1] != "작업 내용" {
		t.Errorf("the prompt must be passed as one argument: %v", command)
	}
}

// An omitted or unknown approval mode must not become the one that changes files
// without asking.
func TestCLIApprovalModeDefaultsToAsking(t *testing.T) {
	for _, value := range []string{"", "unknown", "YOLO"} {
		if got := cliApprovalMode(store.AgentGoal{CLIApprovalMode: value}); got != "default" {
			t.Errorf("approval mode for %q = %q", value, got)
		}
	}
	for _, value := range CLIApprovalModes {
		if got := cliApprovalMode(store.AgentGoal{CLIApprovalMode: value}); got != value {
			t.Errorf("approval mode %q was rewritten to %q", value, got)
		}
	}
}

// A task with no runtime fails as infrastructure: the agent lives in the Pod.
func TestRunCLIWithoutARuntimeIsRetryable(t *testing.T) {
	orchestrator := &Orchestrator{}
	_, outcome := orchestrator.runCLI(t.Context(), &store.AgentRun{}, store.AgentTask{}, store.Agent{},
		store.AgentGoal{Runner: store.RunnerCLI}, resolvedModel{}, nil)
	if outcome.Status != store.TaskFailed || !outcome.Retryable {
		t.Fatalf("outcome = %#v", outcome)
	}
}

// The wall-time budget must stay a positive duration even for a short goal.
func TestCLIWallTimeStaysPositiveForShortGoals(t *testing.T) {
	command := cliCommand(store.AgentGoal{MaxDurationSeconds: 30}, resolvedModel{}, "x")
	index := indexOf(command, "--max-wall-time")
	if index < 0 {
		t.Fatalf("no wall time budget: %v", command)
	}
	value := strings.TrimSuffix(command[index+1], "s")
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		t.Fatalf("wall time budget %q is not a positive duration", command[index+1])
	}
}

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}
