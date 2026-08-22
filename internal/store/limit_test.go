package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAskingForTooManyGivesTheMostAllowed(t *testing.T) {
	if got := clampLimit(0, 50, 200); got != 50 {
		t.Errorf("no limit asked for should give the default, got %d", got)
	}
	if got := clampLimit(120, 50, 200); got != 120 {
		t.Errorf("a limit inside the range should be honoured, got %d", got)
	}
	if got := clampLimit(1000, 50, 200); got != 200 {
		t.Errorf("asking for too many should give the ceiling, not the default; got %d", got)
	}
	if clampLimit(1000, 50, 200) <= clampLimit(200, 50, 200)-1 {
		t.Error("asking for more must never return fewer rows than asking for the ceiling")
	}
}

// Every list in this package has a ceiling, and every one of them used to answer
// "out of range" with the default — so `?limit=1000` against a ceiling of 200
// handed back fifty rows, fewer than asking for two hundred, with nothing in the
// answer to say the number had been ignored. There were ten of them, all with
// the same shape, which is why this is a guard and not a fix: the eleventh will
// be written by copying one of the ten.
func TestEveryLimitIsClampedRatherThanReset(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// `if limit > N {` — the shape that resets instead of clamping. The word has
	// to be the limit itself, so that a retention limit in days is not mistaken
	// for a row count.
	shape := regexp.MustCompile(`if [^\n]*\b[lL]imit\s*[<>]=?\s*\d+`)
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "limit.go" {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for number, line := range strings.Split(string(body), "\n") {
			if shape.MatchString(line) {
				t.Errorf("%s:%d clamps a limit by hand: %s\nuse clampLimit, so that asking for too many gives the ceiling rather than the default", file, number+1, strings.TrimSpace(line))
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d file(s) were read; this guard is not looking at the package", checked)
	}
	if !strings.Contains(readFile(t, "execution.go"), "clampLimit(") {
		t.Error("execution.go no longer uses clampLimit; either the lists lost their ceilings or this guard is reading the wrong package")
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
