package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Asking for an approval and telling nobody about it is the same as not asking.
//
// The reviewer on an approval is the requester's manager, and manager_id is an
// org-chart field most deployments never fill in. Both places that request an
// approval notified the reviewer only when there was one, so with no org chart the
// task parked at the gate, the requester was told it was waiting, and the approval
// sat in a queue that only an administrator could see — and only if they thought
// to look. Nothing anywhere said "nobody was told".
//
// So no caller may write that condition itself: they go through NotifyApprovers,
// which falls back to the administrators who are allowed to decide it.
func TestNobodyAsksForAnApprovalAndTellsNobody(t *testing.T) {
	byHand := regexp.MustCompile(`ReviewerID != nil`)
	requests := regexp.MustCompile(`CreateApproval\(`)
	sites := 0
	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == "node_modules" || name == ".git" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		source := string(body)
		for _, at := range requests.FindAllStringIndex(source, -1) {
			if strings.HasPrefix(source[strings.LastIndex(source[:at[0]], "\n")+1:at[0]], "func ") {
				continue
			}
			sites++
			window := source[at[1]:min(len(source), at[1]+1200)]
			if !strings.Contains(window, "NotifyApprovers(") {
				t.Errorf("%s asks for an approval without going through NotifyApprovers; on a deployment with no org chart nobody hears about it", path)
			}
			if byHand.MatchString(window) {
				t.Errorf("%s decides for itself whether an approval has a reviewer; that is the check that left nobody notified", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 2 {
		t.Fatalf("only %d approval request(s) found; this guard is not reading the tree", sites)
	}
}

// And the fallback has to be people who can actually answer. An administrator can
// decide an unassigned approval — DecideApproval allows it — and nobody else can,
// so anybody else would be a notification that leads to a button its reader is
// refused by.
func TestTheApprovalFallbackGoesToWhoeverCanDecide(t *testing.T) {
	body, err := os.ReadFile("approval.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) NotifyApprovers(")
	if at < 0 {
		t.Fatal("NotifyApprovers is gone; the guard above is checking for nothing")
	}
	fallback := source[at:]
	if end := strings.Index(fallback, "\n}\n"); end >= 0 {
		fallback = fallback[:end]
	}
	if !strings.Contains(fallback, `role='admin'`) {
		t.Error("the fallback no longer picks administrators; whoever it picks must be allowed to decide an unassigned approval")
	}
	if !strings.Contains(fallback, `status='active'`) {
		t.Error("the fallback notifies deactivated accounts; a notification nobody can read is the state this exists to prevent")
	}
}

// Cancelling work cancels the decision it was waiting for.
//
// A task parked on an approval left that approval pending for ever. The
// reviewer went on being asked about work somebody had called off, and
// answering did nothing: the query that resumes a task matches only rows still
// waiting, and a cancelled one never matches again. Three pending approvals
// were sitting in one deployment when this was written.
func TestCancellingWorkCancelsTheDecisionItWaitedFor(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) CancelAgentTask(")
	if at < 0 {
		t.Fatal("cancellation is gone; this guard is reading nothing")
	}
	cancel := source[at:]
	if end := strings.Index(cancel, "\n// TaskWasCancelled"); end >= 0 {
		cancel = cancel[:end]
	}
	if !strings.Contains(cancel, "UPDATE approvals") {
		t.Error("a cancelled task leaves its approval pending, so a reviewer is asked about work that is over")
	}
	if !strings.Contains(cancel, "a.status='pending'") {
		t.Error("cancellation rewrites approvals somebody already decided")
	}
	// The descendants are cancelled too, so their approvals must be as well —
	// the whole point of the tree query one statement above.
	if !strings.Contains(cancel[strings.Index(cancel, "UPDATE approvals"):], "tree") {
		t.Error("only the parent's approval is cancelled; a delegated child's is left pending")
	}
}

// How long a runtime has been in the state it is in is read from updated_at, and
// every screen that lists runtimes observes them.
//
// An observation that saw nothing new was still written, so a Pod stuck starting
// was re-stamped every few seconds by whoever was watching it — and the row that
// reports a runtime half-started for ten minutes could not fire while anybody was
// looking. Measured against the platform's own query: a runtime forty minutes
// into starting reported as nothing wrong when observed a moment earlier, and as
// stuck when not.
func TestAnObservationThatSawNothingDoesNotAgeTheRuntime(t *testing.T) {
	body, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) UpdateRuntimeObserved(")
	if at < 0 {
		t.Fatal("the observation writer is gone; this guard is reading nothing")
	}
	writer := source[at:]
	if end := strings.Index(writer, "\nfunc "); end >= 0 {
		writer = writer[:end]
	}
	compare := strings.Index(writer, "SELECT status=$2 AND pod_name=$3")
	write := strings.Index(writer, "UPDATE agent_runtimes r SET status=$1")
	if compare < 0 {
		t.Error("every observation writes, so a runtime nobody can start looks freshly changed to whoever is watching")
	}
	if write >= 0 && compare > write {
		t.Error("the row is written before it is compared, which is the same as not comparing")
	}
	if !strings.Contains(writer, "return nil") {
		t.Error("an unchanged observation still falls through to the write")
	}
	// Everything that decides whether a state is new has to be in the comparison,
	// or a real change is swallowed as a no-op.
	for _, field := range []string{"status=$2", "pod_name=$3", "node_name=$4", "endpoint=$5", "restart_count=$6", "failure_reason=$7"} {
		if !strings.Contains(writer, field) {
			t.Errorf("%s is not compared, so a change to it would be dropped", field)
		}
	}
}
