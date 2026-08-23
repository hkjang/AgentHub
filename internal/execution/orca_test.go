package execution

import (
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
