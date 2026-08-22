package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The number beside a status has to mean what it says.
//
// The console counted the rows it had been handed, and the list is capped at the
// hundred most recent tasks. So "승인 대기 2" meant two were waiting among the most
// recent hundred — and was read, correctly, as two waiting altogether. On a
// deployment that finishes a hundred tasks a day, work waiting for a person read
// as zero while it sat there: the longer it waited, the less it existed.
//
// The counts come from the database now, and this reads the console to make sure
// the local tally has not come back.
func TestTheConsoleDoesNotCountThePageItWasGiven(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "pages", "Tasks.tsx")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	source := string(body)
	if strings.Contains(source, "tasks.reduce<Record<string, number>>") {
		t.Error("Tasks.tsx counts statuses over the tasks it was given; the list is capped, so the number is only about that page")
	}
	if !strings.Contains(source, "counts?: Record<string, number>") {
		t.Error("Tasks.tsx no longer reads the counts the server sends; the numbers beside each status are page-local again")
	}
	// The waiting states are the ones this is for. A status list that quietly drops
	// them puts the work back out of sight.
	for _, status := range []string{"waiting_approval", "handoff", "blocked"} {
		if !strings.Contains(source, "'"+status+"'") {
			t.Errorf("Tasks.tsx no longer mentions %s; work waiting on a person has to be somewhere a person looks", status)
		}
	}
}

// And the server has to send them. A handler that stops counting turns the console
// numbers into zeroes rather than into an error, which is the failure this whole
// change is about.
func TestTheTaskListSendsItsCounts(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("execution.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) tasks(")
	if at < 0 {
		t.Fatal("the task list handler is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\n}\n"); end >= 0 {
		handler = handler[:end]
	}
	if !strings.Contains(handler, "AgentTaskCounts(") || !strings.Contains(handler, `"counts"`) {
		t.Error("the task list no longer sends counts from the database; the console will show the size of its own page instead")
	}
}
