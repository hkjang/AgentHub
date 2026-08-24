package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run's own context is the wrong clock for the record of what the run did.
// The step and the closing event are written after the work ends, and when it
// ended because its deadline passed, that same expired context discards them.
//
// Measured on a cluster: an orca task that reached its time limit stored zero
// steps, and the worker logged "orca step could not be recorded: context
// deadline exceeded" beside "run event could not be recorded". The person was
// shown a failed task with an empty timeline — the one case where what the
// workers did is the whole of what they need to read.
func TestWhatHappenedIsRecordedEvenWhenTheRunIsOutOfTime(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if strings.Contains(source, "AppendRunStep(recordStepContext(ctx)") {
			found = true
		}
		if strings.Contains(source, "AppendRunStep(ctx,") {
			t.Errorf("%s records its step on the run's own context — a run that times out records nothing", file)
		}
	}
	if !found {
		t.Fatal("no backend records a step at all; this guard is reading nothing")
	}
	body, err := os.ReadFile("orchestrator.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) event(")
	if at < 0 {
		t.Fatal("the event writer is gone; this guard is reading nothing")
	}
	writer := source[at:]
	if end := strings.Index(writer, "\n}\n"); end >= 0 {
		writer = writer[:end]
	}
	if !strings.Contains(writer, "recordContext(ctx)") {
		t.Error("the closing event is written on the run's own context, so a run that times out leaves no account of why")
	}
}
