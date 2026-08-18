package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func TestCheckpointCarriesCompletedWorkOldestFirst(t *testing.T) {
	resume := trimCheckpoint(store.Checkpoint{Outputs: []string{"1단계", "2단계", "3단계"}, Steps: 3, LastRunID: "run-2"})
	if resume.steps != 3 || resume.lastRunID != "run-2" {
		t.Fatalf("unexpected checkpoint: %+v", resume)
	}
	if got := strings.Join(resume.transcript, "|"); got != "1단계|2단계|3단계" {
		t.Fatalf("transcript out of order: %q", got)
	}
	if resume.dropped != 0 {
		t.Fatalf("nothing should have been dropped: %d", resume.dropped)
	}
}

func TestEmptyCheckpointIsTheFirstAttempt(t *testing.T) {
	resume := trimCheckpoint(store.Checkpoint{})
	if len(resume.transcript) != 0 || resume.steps != 0 {
		t.Fatalf("a first attempt must resume from nothing: %+v", resume)
	}
}

func TestCheckpointKeepsTheNewestWorkWithinBudget(t *testing.T) {
	// Ten entries, each a third of the budget: only the newest few can be carried,
	// and the ones left out have to be declared rather than silently missing.
	entry := strings.Repeat("가", maxResumeChars/3)
	outputs := make([]string, 10)
	for index := range outputs {
		outputs[index] = entry + string(rune('a'+index))
	}
	resume := trimCheckpoint(store.Checkpoint{Outputs: outputs, Steps: len(outputs)})
	if resume.steps != 10 {
		t.Fatalf("the step count is the task's, not the transcript's: %d", resume.steps)
	}
	if resume.dropped == 0 {
		t.Fatal("oversized history was carried whole")
	}
	if !strings.Contains(resume.transcript[0], "생략") {
		t.Fatalf("the notice about dropped steps is missing: %q", resume.transcript[0])
	}
	// The newest entry must be the last one carried.
	if !strings.HasSuffix(resume.transcript[len(resume.transcript)-1], "j") {
		t.Fatal("the newest step was dropped instead of the oldest")
	}
	carried := 0
	for _, item := range resume.transcript[1:] {
		carried += len(item)
	}
	if carried > maxResumeChars {
		t.Fatalf("carried %d bytes, over the %d budget", carried, maxResumeChars)
	}
}

func TestASingleOversizedStepIsStillCarried(t *testing.T) {
	// Resuming with nothing would repeat the work, which is worse than one long
	// prompt.
	huge := strings.Repeat("나", maxResumeChars*2)
	resume := trimCheckpoint(store.Checkpoint{Outputs: []string{huge}, Steps: 1})
	if len(resume.transcript) != 1 || resume.dropped != 0 {
		t.Fatalf("the only completed step was dropped: %+v", len(resume.transcript))
	}
}

func TestCheckpointRespectsTheEntryCap(t *testing.T) {
	outputs := make([]string, maxResumeEntries+5)
	for index := range outputs {
		outputs[index] = "짧은 단계"
	}
	resume := trimCheckpoint(store.Checkpoint{Outputs: outputs, Steps: len(outputs)})
	if resume.dropped != 5 {
		t.Fatalf("dropped %d, want 5", resume.dropped)
	}
	// One notice plus the cap.
	if len(resume.transcript) != maxResumeEntries+1 {
		t.Fatalf("carried %d entries", len(resume.transcript))
	}
}
