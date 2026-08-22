package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// What the review queue reports about itself has to come from the table.
//
// The live check in internal/api can see the order and the arithmetic, but not
// where the numbers came from: a count taken over the two hundred rows that were
// returned satisfies "hidden = pending - shown" perfectly and is exactly the bug
// — the bell had the same one, counting the unread among the fifty it fetched
// and printing it as the unread count. So this reads the query.
func TestTheWaitingCountIsOfTheTable(t *testing.T) {
	body, err := os.ReadFile("approval.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) Approvals(")
	if at < 0 {
		t.Fatal("Approvals is gone; this guard is reading nothing")
	}
	block := source[at:]
	if end := strings.Index(block, "\nfunc "); end >= 0 {
		block = block[:end]
	}
	counts := regexp.MustCompile(`count\(\*\) FROM approvals`)
	if !counts.MatchString(block) {
		t.Error("the waiting count is not a count of the approvals table; a count of the rows that fit reports zero hidden no matter how many are waiting")
	}
	if !strings.Contains(block, `a.status='pending'`) {
		t.Error("the count does not restrict to waiting requests")
	}
	// Waiting requests ascending: the longest wait is the one that falls off the
	// end of a capped list, so it has to be at the top of it.
	if !regexp.MustCompile(`CASE WHEN a\.status='pending' THEN a\.created_at END ASC`).MatchString(block) {
		t.Error("waiting requests are no longer ordered oldest-first; the request that has waited longest is the one a reviewer will never see")
	}
	if !strings.Contains(block, "list.Hidden = max(0, list.Pending-waiting)") {
		t.Error("the queue no longer works out how many waiting requests did not fit, so the console cannot say so")
	}
}
