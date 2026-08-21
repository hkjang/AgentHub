package execution

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// An artifact has to be announced twice, and only one of the two is obvious.
//
// The run event puts it in this run's timeline, where whoever opens the run will
// see it. The platform event is what a trigger subscribes to, and it is the whole
// of "산출물 생성" as an automation: without it the console offers a subscription
// that never fires. The pictures an ACP agent takes were stored, shown in the run,
// and silent to every trigger — so this reads the code rather than the intent.
func TestEveryStoredArtifactIsAnnouncedToTheWholePlatform(t *testing.T) {
	saver := regexp.MustCompile(`CreateArtifact\(`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, at := range saver.FindAllStringIndex(source, -1) {
			callers++
			// The announcement follows the store call closely; a caller that stores
			// an artifact and says nothing about it for 40 lines is the bug.
			end := at[1] + 1200
			if end > len(source) {
				end = len(source)
			}
			if !strings.Contains(source[at[1]:end], "artifactSaved(") {
				t.Errorf("%s stores an artifact without going through artifactSaved; a trigger subscribed to 산출물 생성 will never hear about it", name)
			}
		}
	}
	if callers < 2 {
		t.Fatalf("only %d artifact writer(s) found; this guard is not reading the package", callers)
	}
}

// And the announcement itself must reach both places. Reading one function is
// enough only because everything else goes through it.
func TestTheArtifactAnnouncementReachesBothPlaces(t *testing.T) {
	body, err := os.ReadFile("evaluate.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) artifactSaved(")
	if at < 0 {
		t.Fatal("artifactSaved is gone; the guard above is now checking for nothing")
	}
	body2 := source[at:]
	if end := strings.Index(body2, "\n}\n"); end >= 0 {
		body2 = body2[:end]
	}
	for _, half := range []string{`o.event(`, "EventArtifactCreated"} {
		if !strings.Contains(body2, half) {
			t.Errorf("artifactSaved no longer does %s; an artifact is announced in the run or to the platform, and it has to be both", half)
		}
	}
}
