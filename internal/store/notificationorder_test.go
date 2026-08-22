package store

import (
	"os"
	"strings"
	"testing"
)

// The bell holds fifty notices. Ordered by time alone, an unread one older than
// fifty newer notices cannot be reached — and the notices this platform sends are
// mostly "something is waiting for you", which is exactly the kind that goes
// unread while more arrive.
//
// So unread leads: the bell is a list of what somebody has not dealt with, and
// read notices give way to anything still waiting.
func TestUnreadNoticesLeadTheBell(t *testing.T) {
	body, err := os.ReadFile("notification.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) Notifications(")
	if at < 0 {
		t.Fatal("Notifications is gone; this guard is reading nothing")
	}
	fn := source[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "read_at IS NULL THEN 0") {
		t.Error("the bell is ordered by time alone; an unread notice older than a page of newer ones cannot be reached")
	}
	if !strings.Contains(fn, "count(*) FROM notifications") {
		t.Error("the unread count is no longer counted in the database; the console can only count its own page")
	}
}
