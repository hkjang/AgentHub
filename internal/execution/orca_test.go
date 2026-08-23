package execution

import (
	"errors"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The fabric answers JSON for every command, including refusals, so a refusal is
// a code and a sentence rather than an exit status somebody has to interpret.
// These envelopes are the shapes the real CLI produced against a running
// runtime — including the two refusals the runner's order exists to avoid.
func TestTheFabricsRefusalsAreReadAsRefusals(t *testing.T) {
	session := &orcaSession{}
	for _, one := range []struct {
		name     string
		document string
		names    string
	}{
		{
			"an orchestration command with no sender terminal",
			`{"id":"local","ok":false,"error":{"code":"no_active_sender_terminal","message":"Could not determine the sender terminal for this orchestration command."}}`,
			"no_active_sender_terminal",
		},
		{
			"a task command before a Run is bound",
			`{"id":"local","ok":false,"error":{"code":"run_required","message":"No Run is bound. Use orchestration run-create or run-use first."}}`,
			"run_required",
		},
		{
			"a flag the command does not have",
			`{"id":"local","ok":false,"error":{"code":"invalid_argument","message":"Unknown flag --title for command: orchestration run-create"}}`,
			"invalid_argument",
		},
	} {
		err := session.readEnvelope(one.document, "", nil)
		if err == nil {
			t.Errorf("%s was read as success", one.name)
			continue
		}
		if !strings.Contains(err.Error(), one.names) {
			t.Errorf("%s: the failure does not carry the fabric's code: %s", one.name, err)
		}
	}
}

// Output that is not an envelope is a broken runtime, not a refusal, and has to
// read as one — otherwise a fabric that failed to start looks like a task the
// fabric declined.
func TestOutputThatIsNotAnEnvelopeIsNotARefusal(t *testing.T) {
	session := &orcaSession{}
	if err := session.readEnvelope("", "orca: command not found", nil); err == nil {
		t.Fatal("a runtime that answered nothing was read as success")
	} else if strings.Contains(err.Error(), "거절") {
		t.Errorf("a missing runtime is reported as the fabric refusing: %s", err)
	}
}

// A success envelope's result has to reach the caller, or every id the runner
// needs is empty and the next command fails for a reason that is not the real
// one.
func TestASuccessCarriesTheResult(t *testing.T) {
	session := &orcaSession{}
	var task orcaTask
	document := `{"id":"local","ok":true,"result":{"task":{"id":"task_9bfdb0bfe27a","run_id":"run_aa0c4e5be95e","created_by_terminal_handle":"term_1a36c999","status":"ready"}}}`
	if err := session.readEnvelope(document, "", &task); err != nil {
		t.Fatal(err)
	}
	if task.Task.ID != "task_9bfdb0bfe27a" || task.Task.RunID != "run_aa0c4e5be95e" {
		t.Fatalf("the fabric's ids did not reach the runner: %+v", task.Task)
	}
	if task.Task.CreatedByTerminalHandle == "" {
		t.Error("the provenance the platform stores was dropped; a record that cannot be looked up in the fabric is a claim")
	}
}

// Two tasks with the same title must not land in the same checkout, and a title
// is a person's sentence which may contain anything at all.
func TestEachTaskGetsItsOwnCheckoutName(t *testing.T) {
	first := orcaWorkspaceName(store.AgentTask{ID: "1bf732e0-7dbd-43fc-b5d3-9e5f2cdc5a7e", Title: "리팩터링"})
	second := orcaWorkspaceName(store.AgentTask{ID: "9ac0114d-2f81-4c33-9d2e-71e0b6a3c512", Title: "리팩터링"})
	if first == second {
		t.Fatalf("two tasks share a checkout name: %s", first)
	}
	for _, name := range []string{first, second} {
		if strings.ContainsAny(name, " /\\:'\"`$;|&<>") {
			t.Errorf("a checkout name carries characters a path or a command line would not survive: %s", name)
		}
		if !strings.HasPrefix(name, "agenthub-") {
			t.Errorf("a checkout the platform made is not marked as its own: %s", name)
		}
	}
	// A title with anything in it must not reach the name.
	hostile := orcaWorkspaceName(store.AgentTask{ID: "abc", Title: "; rm -rf / #"})
	if strings.Contains(hostile, "rm") {
		t.Errorf("the task's title reached the checkout name: %s", hostile)
	}
}

// The refusal an operator will actually meet has to say what to do about it,
// rather than repeat the fabric's code.
func TestTheAccountRefusalSaysWhatToDo(t *testing.T) {
	err := orcaWorkerFailure("claude", errors.New(`실행 패브릭이 거절했습니다(agent_unconfigured): A configured --agent is required when worker-start creates a terminal.`))
	if err == nil {
		t.Fatal("a refusal became no error at all")
	}
	for _, want := range []string{"claude", "orca account add", "계정"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not carry %q: %s", want, err)
		}
	}
	if strings.Contains(err.Error(), "agent_unconfigured") {
		t.Errorf("the message repeats the fabric's code instead of saying what to do: %s", err)
	}
	fenced := orcaWorkerFailure("codex", errors.New(`실행 패브릭이 거절했습니다(consumer_fenced): worker-start requires the coordinator terminal currently bound to the Task Run.`))
	if !strings.Contains(fenced.Error(), "코디네이터") {
		t.Errorf("a fenced refusal is not explained: %s", fenced)
	}
	// Inventing an explanation for a refusal nobody has seen would be worse than
	// passing it through.
	other := orcaWorkerFailure("codex", errors.New("실행 패브릭이 거절했습니다(quota_exhausted): out of runs"))
	if !strings.Contains(other.Error(), "quota_exhausted") {
		t.Errorf("an unfamiliar refusal lost its detail: %s", other)
	}
}

// Two workers on one task must not land in the same checkout, which is the exact
// thing separate checkouts exist to prevent.
func TestEachWorkerGetsItsOwnCheckout(t *testing.T) {
	session := &orcaSession{worktreeName: "agenthub-1bf732e07dbd"}
	first, second := session.workerName("claude"), session.workerName("codex")
	if first == second {
		t.Fatalf("two workers share a checkout: %s", first)
	}
	for _, name := range []string{first, second} {
		if !strings.HasPrefix(name, "agenthub-1bf732e07dbd-") {
			t.Errorf("a worker's checkout is not tied to its task: %s", name)
		}
		if strings.ContainsAny(name, " /\\:'\"`$;|&<>") {
			t.Errorf("a checkout name carries characters a path would not survive: %s", name)
		}
	}
}

// The names go on a command line. One that is a sentence would fail with a
// message about flags rather than about the agent.
func TestOnlyThingsThatCouldBeAnAgentIdSurvive(t *testing.T) {
	got := strings.Join(OrcaAgentNames(" Claude , codex ,, ; rm -rf / , opencode-2 "), ",")
	if got != "claude,codex,rm-rf,opencode-2" {
		t.Fatalf("the list was cleaned to %q", got)
	}
	if len(OrcaAgentNames("")) != 0 {
		t.Error("an empty list produced an agent")
	}
}

// Accepting a dispatch and running a worker are different events.
//
// Measured against a live fabric: worker-start answered ok with a dispatch id,
// and the same dispatch was `failed` / `agent_prompt_stalled` eighteen seconds
// later because the agent was not installed on the host. A runner that counted
// the ok would report starting workers that never ran.
func TestAnAcceptedDispatchIsNotARunningWorker(t *testing.T) {
	session := &orcaSession{}
	document := `{"id":"local","ok":true,"result":{"dispatch":{"id":"ctx_89f6893800fe","status":"failed","last_failure":"agent_prompt_stalled","dispatched_at":"2026-08-23 05:48:20","completed_at":"2026-08-23 05:48:38"}}}`
	var shown struct {
		Dispatch struct {
			Status      string `json:"status"`
			LastFailure string `json:"last_failure"`
		} `json:"dispatch"`
	}
	if err := session.readEnvelope(document, "", &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Dispatch.Status != "failed" {
		t.Fatalf("the fabric said failed and the runner read %q", shown.Dispatch.Status)
	}
	if shown.Dispatch.LastFailure == "" {
		t.Error("the reason the worker failed was dropped; the operator is left with a status and no cause")
	}
	if status, detail := orcaWorkerOutcome(shown.Dispatch.Status, shown.Dispatch.LastFailure); status != "failed" || detail == "" {
		t.Errorf("a settled failure was read as %q/%q", status, detail)
	}
	// A record the fabric has not settled is neither failed nor running.
	if status, _ := orcaWorkerOutcome("", ""); status != "dispatched" {
		t.Errorf("an unsettled dispatch was read as %q", status)
	}
	// Anything else keeps the fabric's own word rather than being flattened.
	if status, _ := orcaWorkerOutcome("running", ""); status != "running" {
		t.Errorf("a running worker was read as %q", status)
	}
}
