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
		{Agent: "codex", WorkerName: "agenthub-t-codex"},
		{Agent: "claude", WorkerName: "agenthub-t-claude"},
	}
	settled := map[string]string{"codex": "completed", "claude": "failed"}
	details := map[string]string{"claude": "agent_prompt_stalled"}
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
	lines := orcaWorkerSummary([]orcaWorkerRef{{Agent: "codex", WorkerName: "agenthub-t-codex"}}, map[string]string{}, map[string]string{})
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

// The fabric answers the command that lists workers in one vocabulary and the
// command that shows one in another: dispatchId and workerState against
// dispatch.status and worker.state. This platform read the second shape from the
// first command's sibling and got nothing at all — every worker said
// "dispatched" until the run's time ran out, which is how a wait that could
// never end shipped.
func TestTheFabricsListingIsReadInItsOwnWords(t *testing.T) {
	body, err := os.ReadFile("orca.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, want := range []string{`json:"dispatchId"`, `json:"workerState"`, `json:"worktreeId"`} {
		if !strings.Contains(source, want) {
			t.Errorf("the listing is not read by its own field name %s", want)
		}
	}
	// The listing is what the wait reads: asking per dispatch is what could not
	// be answered.
	at := strings.Index(source, "func (s *orcaSession) waitForWorkers(")
	wait := source[at:]
	if end := strings.Index(wait, "\n// orcaWorkerSummary"); end >= 0 {
		wait = wait[:end]
	}
	if !strings.Contains(wait, "s.workerStates(ctx)") {
		t.Error("the wait does not read the fabric's worker listing")
	}
}

// A worker is matched to its agent by the checkout this platform asked for,
// because that is the one name both sides agree on.
func TestAWorkerIsMatchedByTheNameThePlatformChose(t *testing.T) {
	session := &orcaSession{worktreeName: "agenthub-abc123"}
	if got := session.workerName("codex"); got != "agenthub-abc123-codex" {
		t.Fatalf("the checkout name is %q", got)
	}
}

// The listing says which workers are done; the record says what of. Reporting
// "failed" without "agent_prompt_stalled" is the count without the fact.
func TestTheReasonComesFromTheWorkersOwnRecord(t *testing.T) {
	body, err := os.ReadFile("orca.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, `json:"last_error"`) {
		t.Error("the worker's own reason is never read")
	}
	at := strings.Index(source, "func (s *orcaSession) waitForWorkers(")
	wait := source[at:]
	if end := strings.Index(wait, "\n// orcaWorkerSummary"); end >= 0 {
		wait = wait[:end]
	}
	if !strings.Contains(wait, "s.workerStatus(ctx, state.dispatchID)") {
		t.Error("a settled worker is reported without asking why")
	}
}

// Every worker having failed is not a finished task. The run said completed
// while its only worker read "failed — agent_prompt_stalled": the dispatch
// reported as the work again, one layer further in.
func TestARunWhoseWorkersAllFailedIsNotASuccess(t *testing.T) {
	if !orcaAllFailed([]string{"codex: failed — agent_prompt_stalled"}) {
		t.Error("one failed worker passed as success")
	}
	if !orcaAllFailed([]string{"codex: failed", "claude: cancelled"}) {
		t.Error("two failed workers passed as success")
	}
	// One that got there is enough: the fabric exists to try several ways.
	if orcaAllFailed([]string{"codex: failed — stalled", "claude: completed"}) {
		t.Error("a run with a working answer was failed")
	}
	// A word this platform has not seen is not success. A run that passes on an
	// unfamiliar state passes on anything.
	if !orcaAllFailed([]string{"codex: something_new"}) {
		t.Error("an unknown state was read as success")
	}
	// No workers at all is not a failure — the Goal may name nobody, and the
	// checkout is still recorded for a person to attach to.
	if orcaAllFailed(nil) {
		t.Error("a run with no workers was failed")
	}
}
