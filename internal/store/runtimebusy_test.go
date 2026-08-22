package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stopping a runtime after a task is a decision about somebody else's work.
//
// The rule used to be "did this task start it", which answers who it belonged to
// when the task began and nothing about who is in it now. An agent allowed
// concurrent runs reuses the runtime the first task started, so the first task to
// finish stopped the Pod under the others. A person who opened a terminal after
// the task started it lost their window when the task ended — under a comment
// saying the user may be working in it.
func TestATaskAsksWhoIsInTheRuntimeBeforeStoppingIt(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "execution", "runtime.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) releaseRuntime(")
	if at < 0 {
		t.Fatal("releaseRuntime is gone; this guard is reading nothing")
	}
	release := source[at:]
	if end := strings.Index(release, "\n}\n"); end >= 0 {
		release = release[:end]
	}
	if !strings.Contains(release, "RuntimeBusy(") {
		t.Error("a task stops its runtime without asking whether anybody else is in it")
	}
	// The check has to come before the stop, not beside it.
	if busy, stop := strings.Index(release, "RuntimeBusy("), strings.Index(release, "spawner.Stop("); busy < 0 || stop < 0 || busy > stop {
		t.Error("the check happens after the Pod is stopped, which is not a check")
	}
	// And an unanswerable question must not read as "nobody".
	if !strings.Contains(release, "leaving it running") {
		t.Error("a failed check no longer leaves the runtime up; an error would stop a runtime somebody is working in")
	}

	busy, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := string(busy)[strings.Index(string(busy), "func (s *Store) RuntimeBusy("):]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	for _, half := range []struct{ what, evidence string }{
		{"another task running in it", "agent_tasks"},
		{"a person with a session open in it", "runtime_sessions"},
		{"the asking task itself, which does not count", "id<>$2"},
	} {
		if !strings.Contains(fn, half.evidence) {
			t.Errorf("RuntimeBusy does not consider %s", half.what)
		}
	}
}
