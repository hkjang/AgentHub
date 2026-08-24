package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
)

// A scanner that is on and inspecting nothing.
//
// A class that is not listed is not scanned — the rule that keeps a newly added
// detector from blocking anybody's traffic unasked. With no class listed at all,
// the same rule means the scanner reports itself as enabled while every payload
// goes through untouched. That is worse than being off, because the screen says
// it is on.
func TestAScannerWithNoClassesSaysItIsInspectingNothing(t *testing.T) {
	empty := dlp.Settings{Enabled: true}
	if got := dlpSaved(empty, syncResult{}); !strings.Contains(got, "아무것도 검사하지 않습니다") {
		t.Errorf("a scanner with no classes was saved with %q", got)
	}
	// Every class switched off is the same state by another route.
	allOff := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Off, "card": dlp.Off}}
	if got := dlpSaved(allOff, syncResult{}); !strings.Contains(got, "아무것도 검사하지 않습니다") {
		t.Errorf("a scanner whose classes are all off was saved with %q", got)
	}
	// And one that does something is not nagged.
	working := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}
	if got := dlpSaved(working, syncResult{}); strings.Contains(got, "아무것도 검사하지 않습니다") {
		t.Errorf("a working scanner was told it inspects nothing: %q", got)
	}
	// Off is off: it says so plainly and does not pretend to be a fault.
	if got := dlpSaved(dlp.Settings{}, syncResult{}); !strings.Contains(got, "껐습니다") {
		t.Errorf("a disabled scanner was saved with %q", got)
	}
}

func TestScannedClassesCountsOnlyWhatActuallyRuns(t *testing.T) {
	settings := dlp.Settings{Enabled: true, Classes: map[string]string{
		"rrn": dlp.Block, "card": dlp.Off, "email": dlp.Redact, "phone": "",
	}}
	scanned := scannedClasses(settings)
	if len(scanned) != 2 {
		t.Fatalf("expected two classes actually scanned, got %v", scanned)
	}
	// Sorted, so the sentence an operator reads does not shuffle between saves.
	if scanned[0] != "email" || scanned[1] != "rrn" {
		t.Errorf("the classes are not in a stable order: %v", scanned)
	}
}
