package logging

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"
)

func add(ring *Ring, level, message string, fields map[string]any) {
	ring.Add(Entry{Time: time.Now(), Level: level, Message: message, Source: "control-plane", Fields: fields})
}

func messages(entries []Entry) []string {
	list := make([]string, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry.Message)
	}
	return list
}

// An operator opens this screen because something has just gone wrong, so the
// line they need is the last one written. A ring that answered in insertion
// order would put it at the bottom of ten thousand.
func TestTheNewestLineComesBackFirst(t *testing.T) {
	ring := NewRing(0)
	for i := 0; i < 5; i++ {
		add(ring, "INFO", "line "+strconv.Itoa(i), nil)
	}
	got := messages(ring.Entries(slog.LevelDebug, "", 0))
	want := []string{"line 4", "line 3", "line 2", "line 1", "line 0"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The buffer is fixed and the platform outlives it many times over, so the
// interesting case is the one every deployment is permanently in: wrapped. An
// off-by-one here does not fail loudly — it quietly shows an operator somebody
// else's incident.
func TestAWrappedRingForgetsOnlyTheOldest(t *testing.T) {
	ring := NewRing(100)
	for i := 0; i < 250; i++ {
		add(ring, "INFO", "line "+strconv.Itoa(i), nil)
	}
	got := messages(ring.Entries(slog.LevelDebug, "", 0))
	if len(got) != 100 {
		t.Fatalf("got %d entries, want the ring's 100", len(got))
	}
	if got[0] != "line 249" || got[99] != "line 150" {
		t.Fatalf("wrapped ring returned %s … %s, want line 249 … line 150", got[0], got[99])
	}
}

// A limit must take the newest lines, not the first ones that happen to be
// reachable: the console asks for 300 of ten thousand and expects the 300 that
// just happened.
func TestALimitKeepsTheNewestAndDropsTheRest(t *testing.T) {
	ring := NewRing(100)
	for i := 0; i < 120; i++ {
		add(ring, "INFO", "line "+strconv.Itoa(i), nil)
	}
	got := messages(ring.Entries(slog.LevelDebug, "", 3))
	if len(got) != 3 || got[0] != "line 119" || got[2] != "line 117" {
		t.Fatalf("got %v, want the three newest lines", got)
	}
	if all := ring.Entries(slog.LevelDebug, "", 1000); len(all) != 100 {
		t.Fatalf("a limit past the end returned %d entries, want the ring's 100", len(all))
	}
}

func TestTheLevelFilterKeepsWhatIsAtLeastAsSevere(t *testing.T) {
	ring := NewRing(0)
	add(ring, "DEBUG", "debug", nil)
	add(ring, "INFO", "info", nil)
	add(ring, "WARN", "warn", nil)
	add(ring, "ERROR", "error", nil)
	if got := messages(ring.Entries(slog.LevelWarn, "", 0)); len(got) != 2 || got[0] != "error" || got[1] != "warn" {
		t.Fatalf("got %v, want the error and the warning", got)
	}
	if got := ring.Entries(slog.LevelDebug, "", 0); len(got) != 4 {
		t.Fatalf("got %d entries at debug, want all 4", len(got))
	}
}

// A level nobody can parse must not silently disappear from the screen; INFO is
// the honest guess, because the entry is there and an operator asking for INFO
// and above should see it.
func TestAnUnparsableLevelIsReadAsInfo(t *testing.T) {
	ring := NewRing(0)
	add(ring, "TRACE?", "odd", nil)
	if got := ring.Entries(slog.LevelInfo, "", 0); len(got) != 1 {
		t.Fatalf("got %d entries, want the one with the unparsable level", len(got))
	}
	if got := ring.Entries(slog.LevelWarn, "", 0); len(got) != 0 {
		t.Fatalf("got %d entries above warning, want none", len(got))
	}
}

// Nobody types the case of a log line back exactly.
func TestSearchIgnoresCase(t *testing.T) {
	ring := NewRing(0)
	add(ring, "ERROR", "Runtime could not be reached", nil)
	if got := ring.Entries(slog.LevelDebug, "RUNTIME COULD", 0); len(got) != 1 {
		t.Fatalf("got %d entries, want the line the query names", len(got))
	}
	if got := ring.Entries(slog.LevelDebug, "workspace", 0); len(got) != 0 {
		t.Fatalf("got %d entries, want none", len(got))
	}
	// A query longer than anything on the line is not a match, and must not read
	// past the end of one either.
	if got := ring.Entries(slog.LevelDebug, "Runtime could not be reached at all", 0); len(got) != 0 {
		t.Fatalf("got %d entries for a query longer than the message, want none", len(got))
	}
}

// The console prints the structured fields on the line. Searching for what is
// printed there used to return 조건에 맞는 로그가 없습니다 about the line the
// operator was looking at.
func TestSearchFindsWhatTheConsolePrintsBesideTheMessage(t *testing.T) {
	ring := NewRing(0)
	add(ring, "ERROR", "task attempt failed", map[string]any{
		"agent": "nightly-review", "attempt": 3, "error": errors.New("runtime handover"),
	})
	add(ring, "INFO", "task attempt finished", map[string]any{"agent": "daily-digest"})

	for _, query := range []string{"nightly-review", "NIGHTLY", "runtime handover", "error"} {
		got := ring.Entries(slog.LevelDebug, query, 0)
		if len(got) != 1 || got[0].Message != "task attempt failed" {
			t.Errorf("%q: got %v, want the failed attempt", query, messages(got))
		}
	}
	// The field's own key finds it too, which is how an operator narrows to the
	// failures before knowing what any of them say.
	if got := ring.Entries(slog.LevelDebug, "agent", 0); len(got) != 2 {
		t.Errorf("got %d entries for the shared field key, want both", len(got))
	}
	if got := ring.Entries(slog.LevelDebug, "weekly-report", 0); len(got) != 0 {
		t.Errorf("got %d entries for a value nothing carries, want none", len(got))
	}
}

// The attributes a logger was built with belong on every line it writes, and the
// record still has to reach the handler that prints it — the ring is a copy of
// the log, not a replacement for it.
func TestCaptureKeepsTheAttributesAndStillWritesThrough(t *testing.T) {
	ring := NewRing(0)
	next := &countingHandler{}
	logger := slog.New(Capture(next, ring)).With("component", "worker")
	logger.Error("task attempt failed", "agent", "nightly-review")

	entries := ring.Entries(slog.LevelDebug, "", 0)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the one that was logged", len(entries))
	}
	entry := entries[0]
	if entry.Level != "ERROR" || entry.Message != "task attempt failed" || entry.Source != "control-plane" {
		t.Fatalf("captured %+v", entry)
	}
	if entry.Fields["component"] != "worker" || entry.Fields["agent"] != "nightly-review" {
		t.Fatalf("captured fields %+v, want both the logger's and the call's", entry.Fields)
	}
	if next.count != 1 {
		t.Fatalf("the next handler saw %d records, want 1", next.count)
	}
	if got := ring.Entries(slog.LevelDebug, "worker", 0); len(got) != 1 {
		t.Fatalf("the logger's own attribute is not searchable")
	}
}

// A handler must not leak one logger's attributes into a sibling built from the
// same parent, which is what sharing the backing array would do.
func TestSiblingLoggersDoNotShareAttributes(t *testing.T) {
	ring := NewRing(0)
	parent := slog.New(Capture(&countingHandler{}, ring)).With("component", "worker")
	parent.With("queue", "default").Info("first")
	parent.With("runtime", "opencode").Info("second")

	entries := ring.Entries(slog.LevelDebug, "", 0)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	second, first := entries[0], entries[1]
	if _, leaked := second.Fields["queue"]; leaked {
		t.Errorf("the second logger inherited the first's attribute: %+v", second.Fields)
	}
	if _, leaked := first.Fields["runtime"]; leaked {
		t.Errorf("the first logger was given the second's attribute: %+v", first.Fields)
	}
}

// Every request path writes to this ring while the admin screen reads it, so the
// race detector is the point of this one.
func TestReadingAndWritingAtOnceIsSafe(t *testing.T) {
	ring := NewRing(128)
	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				add(ring, "INFO", "line "+strconv.Itoa(i), map[string]any{"writer": writer})
			}
		}(writer)
	}
	for reader := 0; reader < 2; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				ring.Entries(slog.LevelDebug, "writer", 50)
			}
		}()
	}
	wg.Wait()
}

type countingHandler struct {
	count int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(context.Context, slog.Record) error {
	h.count++
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *countingHandler) WithGroup(string) slog.Handler { return h }
