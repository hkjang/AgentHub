package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "Idle" has to mean idle. The timestamp it is measured from was written by one
// thing only — a person's browser session through the runtime proxy — so a
// runtime doing an agent's work was idle by definition from the moment it
// started. With a profile timeout shorter than a run, which is exactly what an
// operator sets to save cluster money, the culler stopped the Pod under a running
// task and the task failed with a runtime error that explained nothing.
//
// Two halves, and both have to hold: the execution plane says the runtime is
// busy, and the query refuses to call a runtime with work in it idle regardless.
func TestARuntimeDoingWorkIsNotIdle(t *testing.T) {
	body, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) IdleRuntimeCandidates(")
	if at < 0 {
		t.Fatal("IdleRuntimeCandidates is gone; this guard is reading nothing")
	}
	query := source[at:]
	if end := strings.Index(query, "\n}\n"); end >= 0 {
		query = query[:end]
	}
	if !strings.Contains(query, "NOT EXISTS") || !strings.Contains(query, "agent_tasks") {
		t.Error("the idle query does not exclude runtimes with a task in them; a stopped Pod under a running task is a crash with no reason in it")
	}
	for _, status := range []string{"'running'", "'handoff'"} {
		if !strings.Contains(query[strings.Index(query, "NOT EXISTS"):], status) {
			t.Errorf("a task in status %s no longer keeps its runtime alive", status)
		}
	}

	// And the other half: the execution plane has to mark the runtime as active,
	// or the timestamp goes on measuring the wrong thing.
	for _, file := range []string{
		filepath.Join("..", "execution", "runtime.go"),
		filepath.Join("..", "execution", "orchestrator.go"),
	} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "TouchRuntime(") {
			t.Errorf("%s never marks the runtime as active; last_activity_at is written only by a person's browser session again", filepath.Base(file))
		}
	}
}
