package execution

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// Handing a task to the fabric and calling that done reported the dispatch as
// the work: the run said "워커 N개를 붙였습니다" and completed while the agents
// were still typing, so a worker that failed two minutes later did so behind a
// task marked successful.
func TestAWorkerStillGoingIsNotAFinishedWorker(t *testing.T) {
	for _, state := range []string{"dispatched", "running", "starting", "pending", "queued", "in_progress", "WORKING"} {
		if !orcaInFlight[strings.ToLower(state)] {
			t.Errorf("%q would end the wait while the worker is still going", state)
		}
	}
	for _, state := range []string{"completed", "failed", "agent_prompt_stalled", "cancelled"} {
		if orcaInFlight[state] {
			t.Errorf("%q would be waited on for ever", state)
		}
	}
	// A word this platform has never seen ends the wait and is reported as
	// itself. Waiting for ever on an unknown state is how a worker slot is lost.
	if orcaInFlight["something_new"] {
		t.Error("an unknown state is treated as still running")
	}
}

// Each worker's end is reported in the fabric's own words, named by its agent —
// two workers with one summary is the fan-out reported as a single thing.
func TestEachWorkerIsReportedByName(t *testing.T) {
	workers := []orcaWorkerRef{
		{Agent: "codex", DispatchID: "d1"},
		{Agent: "claude", DispatchID: "d2"},
	}
	settled := map[string]string{"d1": "completed", "d2": "failed"}
	details := map[string]string{"d2": "agent_prompt_stalled"}
	lines := orcaWorkerSummary(workers, settled, details)
	if len(lines) != 2 {
		t.Fatalf("got %d lines for two workers: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "codex: completed") {
		t.Errorf("the first worker reads %q", lines[0])
	}
	if !strings.Contains(lines[1], "claude") || !strings.Contains(lines[1], "agent_prompt_stalled") {
		t.Errorf("the failed worker does not carry the fabric's words: %q", lines[1])
	}
}

// A worker that never settled says so rather than being counted as finished.
func TestAnUnsettledWorkerSaysSo(t *testing.T) {
	lines := orcaWorkerSummary([]orcaWorkerRef{{Agent: "codex", DispatchID: "d1"}}, map[string]string{}, map[string]string{})
	if len(lines) != 1 || !strings.Contains(lines[0], "아직 끝나지 않았습니다") {
		t.Fatalf("got %v", lines)
	}
}

// The work belongs to the task, so the Goal's own limit is what bounds the wait
// — and a Goal with no limit still cannot wait for ever.
func TestTheWaitIsBoundedByTheGoal(t *testing.T) {
	if got := orcaWorkerLimit(store.AgentGoal{MaxDurationSeconds: 120}); got.Seconds() != 120 {
		t.Errorf("a Goal limited to two minutes waits %v", got)
	}
	unlimited := orcaWorkerLimit(store.AgentGoal{})
	if unlimited <= 0 || unlimited > 4*60*60*1e9 {
		t.Errorf("a Goal with no limit waits %v", unlimited)
	}
}

// A wait that ran out of time says so the way the rest of the platform does. A
// live run said "context deadline exceeded", which is Go's words for a person
// looking at a task list.
func TestAWaitThatRanOutSaysItInTheSameWords(t *testing.T) {
	body, err := os.ReadFile("orca.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *orcaSession) waitForWorkers(")
	if at < 0 {
		t.Fatal("the wait is gone; this guard is reading nothing")
	}
	wait := source[at:]
	if end := strings.Index(wait, "\n// orcaWorkerSummary"); end >= 0 {
		wait = wait[:end]
	}
	if strings.Contains(wait, "ctx.Err()\n") && !strings.Contains(wait, "runtimeExecFailure(") {
		t.Error("a cancelled or timed-out wait returns Go's own error to a person")
	}
	if !strings.Contains(wait, "runtimeExecFailure(") {
		t.Error("the wait does not explain its ending the way the other backends do")
	}
}
