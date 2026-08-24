package execution

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A review whose files all failed counts them and explains nothing: "1 of 1
// selected item(s) failed." The answer it returns alongside says exactly why —
// the reason it assigned the file, and the class of the model call that failed.
//
// The fixture is a real answer from the engine (v1.9.9) reviewing one file
// against a gateway that refused the credential.
func TestAFailedReviewSaysWhyInTheEnginesTerms(t *testing.T) {
	parsed := refusedReview(t)
	reason := reviewItemFailure(parsed)
	if reason == "" {
		t.Fatal("the engine said why and the platform kept none of it")
	}
	if !strings.Contains(reason, "401") {
		t.Errorf("the refusal's status is missing: %q", reason)
	}
	if !strings.Contains(reason, "authentication") {
		t.Errorf("the class of the failure is missing: %q", reason)
	}
	// And the engine's own word for the file, read through the tag it declares.
	if !strings.Contains(reason, "provider or subtask request failed") {
		t.Errorf("the reason the engine assigned the file is missing: %q", reason)
	}
	if strings.Contains(reason, "\n") {
		t.Errorf("a run's failure line carries a newline: %q", reason)
	}
}

// A review that failed for some reason the engine did not classify invents
// nothing: an empty answer is better than a confident wrong one.
func TestAReviewWithNothingToSayAddsNothing(t *testing.T) {
	if reason := reviewItemFailure(reviewResult{}); reason != "" {
		t.Fatalf("a reason appeared from nowhere: %q", reason)
	}
}

// The file's own reason is carried even when no model call failed — a file the
// engine could not read is a different repair from a refused key.
func TestTheFilesOwnReasonIsCarriedAlone(t *testing.T) {
	var parsed reviewResult
	parsed.Manifest.Coverage.Failed = []reviewItem{{Path: "a.go", Reason: "file could not be read"}}
	reason := reviewItemFailure(parsed)
	if reason != "file could not be read" {
		t.Fatalf("got %q", reason)
	}
}

// And the reason has to reach the run's own message. A helper that finds it and
// a failure path that ignores it is the same silence as before.
func TestTheRunsFailureSaysWhatTheEngineSaid(t *testing.T) {
	body, err := os.ReadFile("review.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, `failure := "리뷰가 끝나지 못했습니다: "`)
	if at < 0 {
		t.Fatal("the review failure path is gone; this guard is reading nothing")
	}
	section := source[at:]
	if end := strings.Index(section, "\n\treturn []string{summary}"); end >= 0 {
		section = section[:end]
	}
	if !strings.Contains(section, "reviewItemFailure(parsed)") {
		t.Error("the run's failure message does not carry the engine's own reason")
	}
}

func refusedReview(t *testing.T) reviewResult {
	t.Helper()
	raw, err := os.ReadFile("testdata/review_refused.json")
	if err != nil {
		t.Skipf("the captured engine answer is not in this checkout: %v", err)
	}
	var parsed reviewResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}
