package execution

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A run's timeline is what somebody opens to find out what happened, and it used
// to print the identifier the code uses — runtime.acquiring, acp.permission.asked.
// The screen now says these in Korean, which only stays true if adding an event
// type to the platform is not a silent way to put an identifier back on it.
//
// So this reads what the execution package actually emits and requires each one
// to have a label. It is deliberately the Go side that fails: the event is added
// here, and this is where the author is standing.

// The trailing comma matters: an ending is emitted as "task."+status, and the
// literal prefix of a concatenation is not an event type. Requiring the argument
// to end there reads the ones that are.
var emittedEvent = regexp.MustCompile(`\.event\((?:[^,]+), (?:[^,]+), "([a-z_.]+)",`)

func TestEveryEventTheTimelineCanShowHasAName(t *testing.T) {
	emitted := emittedEventTypes(t)
	if len(emitted) < 20 {
		t.Fatalf("the extractor found only %d event types; it is reading the wrong thing", len(emitted))
	}
	labels := labelledEventTypes(t)
	var missing []string
	for _, eventType := range emitted {
		if !labels[eventType] {
			missing = append(missing, eventType)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the timeline would print these as identifiers; add them to web/src/runEvents.ts: %s",
			strings.Join(missing, ", "))
	}
}

// A run ends with one of the statuses the run row can hold, and the event is
// typed with it. An ending nobody labelled reads as "task.dead_letter" on the
// last line of the timeline, which is the line people read first.
func TestEveryEndingARunCanHaveHasAName(t *testing.T) {
	labels := labelledEventTypes(t)
	for _, status := range []string{store.TaskCompleted, store.TaskFailed, store.TaskCancelled, store.TaskDeadLetter} {
		if !labels["task."+status] {
			t.Errorf("a run that ends %q has no name on the timeline", status)
		}
	}
}

// TestARunWithNoTaskStatusIsStillTypedAsAnEnding fails if the ending is typed
// with a value that can be empty. It produced "task." — a dot with nothing after
// it — on every run parked for an approval or stopped by a quota, and a live
// database had fourteen of them.
func TestARunWithNoTaskStatusIsStillTypedAsAnEnding(t *testing.T) {
	source := readSource(t, "orchestrator.go")
	if strings.Contains(source, `o.event(finish, run, "task."+outcome.Status`) {
		t.Error("the ending is typed with a status that is empty on a parked run")
	}
	at := strings.Index(source, `o.event(finish, run, "task."+`)
	if at < 0 {
		t.Fatal("the ending event is gone; this guard is reading nothing")
	}
	before := source[:at]
	if !strings.Contains(before, "ending = run.Status") {
		t.Error("nothing falls back to the status the run row actually recorded")
	}
	if !strings.Contains(before, "note = outcome.Note") {
		t.Error("a parked run's ending carries no message, so the line is blank")
	}
}

func emittedEventTypes(t *testing.T) []string {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range emittedEvent.FindAllStringSubmatch(string(raw), -1) {
			seen[match[1]] = true
		}
	}
	types := make([]string, 0, len(seen))
	for eventType := range seen {
		types = append(types, eventType)
	}
	sort.Strings(types)
	return types
}

func labelledEventTypes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "runEvents.ts"))
	if err != nil {
		t.Fatalf("the console's label table cannot be read: %v", err)
	}
	labels := map[string]bool{}
	for _, match := range regexp.MustCompile(`'([a-z_.]+)':\s*'([^']+)'`).FindAllStringSubmatch(string(raw), -1) {
		labels[match[1]] = true
	}
	if len(labels) == 0 {
		t.Fatal("the label table parsed to nothing; this guard is reading the wrong file")
	}
	return labels
}
