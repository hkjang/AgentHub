package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every step handed to the inspector has to say who it belongs to.
//
// The bug this file is about was an omission, not a mistake in logic: the field
// existed nowhere, so nothing could carry it. Now that it exists, a backend
// added later that forgets it is the same bug again — silently, because the
// text is still scanned and the run still finishes. The execution plane knows
// the owner at every one of these points, so the check is that it says so.
func TestEveryStepSaysWhoItIsFor(t *testing.T) {
	// The control plane builds steps too — a workflow run resolves one per graph
	// node — and its steps go to the same inspector, so the sweep reads both
	// places rather than the one it lives in.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	control, err := filepath.Glob("../api/*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, control...)
	seen := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for at := 0; ; {
			start := strings.Index(source[at:], "workflow.Step{")
			if start < 0 {
				break
			}
			start += at
			literal := source[start:closingBrace(source, start+len("workflow.Step"))]
			at = start + len("workflow.Step{")
			seen++
			if !strings.Contains(literal, "OwnerID") {
				t.Errorf("%s: a step reaches the content inspector without an owner, so no rule about a person applies to it:\n%s",
					filepath.Base(file), literal)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no steps found; this sweep is reading nothing")
	}
}

// closingBrace returns the offset just past the brace that closes the one at
// open, so a composite literal is read whole however it is wrapped.
func closingBrace(source string, open int) int {
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(source)
}
