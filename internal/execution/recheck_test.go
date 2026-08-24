package execution

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The last step of the review loop.
//
// A finding handed to a coding agent stays open until a later review stops
// reporting it — and nothing ever started that later review. So a fix that
// worked and a fix that changed nothing looked identical for ever, and the loop
// the platform documents ended one step early.
//
// These are the properties that keep the automatic half from being worse than
// the manual one it replaces.
func TestARecheckOnlyFollowsAFix(t *testing.T) {
	body, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (w *Worker) recheckFixedFindings(")
	if at < 0 {
		t.Fatal("the re-review is gone; this guard is reading nothing")
	}
	block := source[at:]
	if end := strings.Index(block[1:], "\nfunc "); end >= 0 {
		block = block[:end]
	}

	// Only a task that was itself a fix leads here. Without this the re-review's
	// own completion would queue another, for ever.
	if !strings.Contains(block, `task.Source != "review"`) {
		t.Error("any completed task can start a re-review, so a re-review starts another and the loop never ends")
	}
	// The re-review is a different source, so it can never be mistaken for a fix.
	if !strings.Contains(block, `"review-recheck"`) {
		t.Error("the re-review is queued with the same source as a fix, which makes it able to trigger itself")
	}
	if !strings.Contains(block, "oncePerReviewer(") {
		t.Error("a fix covering four findings queues four identical reviews")
	}
	// The finding's own agent and owner, not the fixing agent's: the review that
	// found the problem is the one that can say whether it is gone, and the work
	// stays charged to whoever owns it.
	if !strings.Contains(block, "finding.ReviewAgentID") || !strings.Contains(block, "finding.OwnerID") {
		t.Error("the re-review is queued against the fixing agent rather than the reviewer")
	}
}

// TestOnlyOpenFindingsAreRechecked — a finding somebody dismissed has had its
// answer from a person, and re-reviewing it argues with them.
func TestOnlyOpenFindingsAreRechecked(t *testing.T) {
	body, err := os.ReadFile("../store/review.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) FindingsFixedBy(")
	if at < 0 {
		t.Fatal("FindingsFixedBy is gone; this guard is reading nothing")
	}
	query := source[at:]
	if end := strings.Index(query[1:], "\nfunc "); end >= 0 {
		query = query[:end]
	}
	if !strings.Contains(query, "status = 'open'") {
		t.Error("a finding a person has already decided is re-reviewed anyway")
	}
	if !strings.Contains(query, "resolved_at IS NULL") {
		t.Error("a finding a later review already cleared is re-reviewed again")
	}
	if !strings.Contains(query, "fix_task_id = $1") {
		t.Error("the re-review is not tied to the fix task that finished")
	}
}

// TestOneRecheckPerReviewer is the behaviour rather than the shape: a fix that
// closed four findings from one review needs one re-review, and two reviewers
// are two opinions and both are asked.
func TestOneRecheckPerReviewer(t *testing.T) {
	fixed := []store.FindingFixed{
		{FindingID: "1", ReviewAgentID: "reviewer-a", FilePath: "a.go"},
		{FindingID: "2", ReviewAgentID: "reviewer-a", FilePath: "b.go"},
		{FindingID: "3", ReviewAgentID: "reviewer-b", FilePath: "c.go"},
		{FindingID: "4", ReviewAgentID: "reviewer-a", FilePath: "d.go"},
	}
	once := oncePerReviewer(fixed)
	if len(once) != 2 {
		t.Fatalf("four findings from two reviewers produced %d re-reviews", len(once))
	}
	if once[0].ReviewAgentID != "reviewer-a" || once[1].ReviewAgentID != "reviewer-b" {
		t.Errorf("the re-reviews are not one per reviewer, in the order they were found: %v", once)
	}
	if len(oncePerReviewer(nil)) != 0 {
		t.Error("no findings produced a re-review")
	}
}
