package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cancelling a running task used to do nothing to the run.
//
// The row was marked cancelled and the claim cleared, and the agent carried on —
// spending tokens, calling tools, performing whatever state change somebody had
// just pressed a button to stop. Then the run finished and wrote its outcome over
// the top, so a task somebody cancelled ended up reading "completed".
//
// Three things had to be true and none of them were: the lease extension has to
// report whether the worker still holds the claim, the worker has to stop the run
// when it does not, and a finished run must not write over a cancellation.
func TestACancelledTaskStopsAndStaysCancelled(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)

	lease := functionText(t, source, "func (s *Store) ExtendTaskLease(")
	if !strings.Contains(lease, "(bool, error)") {
		t.Error("the lease extension does not report whether the claim is still held; the row count is the answer and it is being discarded")
	}
	if !strings.Contains(lease, "RowsAffected()") {
		t.Error("the lease extension does not look at what it updated")
	}
	if !strings.Contains(lease, "claimed_by=$2") {
		t.Error("the lease extension no longer matches on this worker holding the claim; a cancellation clears exactly that")
	}

	finish := functionText(t, source, "func (s *Store) FinishAgentTask(")
	if !strings.Contains(finish, "status<>'cancelled'") {
		t.Error("a finishing run overwrites a cancellation; the task somebody cancelled ends up saying whatever the run decided")
	}

	worker, err := os.ReadFile(filepath.Join("..", "execution", "worker.go"))
	if err != nil {
		t.Fatal(err)
	}
	keep := functionText(t, string(worker), "func (w *Worker) keepClaim(")
	if !strings.Contains(keep, "stopRun()") {
		t.Error("the worker notices it has lost the claim and does not stop the run")
	}
	// A database that cannot be read is not a cancellation.
	if !strings.Contains(keep, "continue") {
		t.Error("a failed lease extension is treated as a lost claim; a moment's connection trouble would abandon the work")
	}
	execute := functionText(t, string(worker), "func (w *Worker) execute(")
	// What matters is that the watcher can stop the run and that the worker can
	// tell afterwards why it stopped — not the exact shape of the call. This
	// asserted the literal `keepClaim(heartbeat, task.ID, stopRun)` and went red
	// the moment that argument became a closure, which is a guard failing over its
	// own wording rather than over the thing it guards.
	if !strings.Contains(execute, "keepClaim(heartbeat, task.ID,") {
		t.Error("the watcher is not given the task to watch")
	}
	if !strings.Contains(execute, "stopRun()") {
		t.Error("the run's cancel is not handed to the watcher, so nothing can stop the run")
	}
	if !strings.Contains(execute, "claimLost.Store(true)") || !strings.Contains(execute, "claimLost.Load()") {
		t.Error("the worker cannot tell a cancellation from the error the run died with; it would file one as a failure and retry it")
	}
	if !strings.Contains(execute, "ctx = run") {
		t.Error("the run does not use the cancellable context; stopping it would stop nothing")
	}
}

func functionText(t *testing.T, source, signature string) string {
	t.Helper()
	at := strings.Index(source, signature)
	if at < 0 {
		t.Fatalf("%s is gone; this guard is reading nothing", signature)
	}
	rest := source[at:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// And nothing may drag a cancelled task back out.
//
// The first version of this fix stopped the run and then let the worker's error
// path retry it, so the task somebody cancelled was queued again seconds later
// and ran to completion anyway — visible in the live check as a second "task
// claimed" for the same task. Every writer a stopped run can reach has to leave a
// cancelled task where it is: the finish, the retry, the defer, the hold, the
// handover and the approval park.
func TestNothingDragsACancelledTaskBack(t *testing.T) {
	writers := map[string][]string{
		"execution.go": {"FinishAgentTask", "RetryAgentTask", "BlockAgentTask", "HandOffTask"},
		"quota.go":     {"deferTaskSQL"},
		"control.go":   {"ParkTaskForApproval"},
	}
	for file, names := range writers {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, name := range names {
			var text string
			if strings.HasSuffix(name, "SQL") {
				at := strings.Index(source, "const "+name+" = ")
				if at < 0 {
					t.Fatalf("%s is gone; this guard is reading nothing", name)
				}
				text = source[at:]
				if end := strings.Index(text[strings.Index(text, "`")+1:], "`"); end >= 0 {
					text = text[:strings.Index(text, "`")+end+2]
				}
			} else {
				text = functionText(t, source, "func (s *Store) "+name+"(")
			}
			if !strings.Contains(text, "status<>'cancelled'") {
				t.Errorf("%s can move a cancelled task back out; the work somebody stopped starts again", name)
			}
		}
	}
}

// Cancelling a task has to stop the work it handed to other agents.
//
// An agent that delegates part of its goal creates a task of its own, tracked
// separately — the right way to run it and the wrong way to stop it. Cancelling
// the parent left every delegated child running: spending the same person's
// budget, changing whatever it was going to change, for a goal that had been
// called off. The relationship is recorded and the descendants are one query away.
func TestCancellingATaskStopsWhatItDelegated(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	cancel := functionText(t, string(body), "func (s *Store) CancelAgentTask(")
	if !strings.Contains(cancel, "RECURSIVE") || !strings.Contains(cancel, "parent_task_id") {
		t.Error("cancelling a task does not reach the tasks it delegated; they run on for a goal that was called off")
	}
	// Finished work is history and a cancellation is not a reason to rewrite it.
	if !strings.Contains(cancel, "'completed','cancelled','dead_letter','failed'") {
		t.Error("the cascade does not exclude work that already finished; a cancellation would rewrite history")
	}
	// The owner check has to hold for the descendants too, not only the task the
	// person named.
	if strings.Count(cancel, "owner_id=$2") < 2 {
		t.Error("the cascade does not check ownership; a delegated task belonging to somebody else could be cancelled")
	}
	// And the count is reported, because "cancelled" on a task that delegated three
	// sub-tasks used to mean one of the four.
	if !strings.Contains(cancel, "(int, error)") {
		t.Error("the cancellation does not report how much it stopped")
	}
}
