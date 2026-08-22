package execution

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// These fixtures are what the engine actually wrote.
//
// They were captured by running the published open-code-review binary in the
// image this platform ships, against a real git repository, with the model
// endpoint pointed at a stub — the same path a review takes in production, minus
// the model's opinions. A fixture somebody typed by hand would only prove that
// the parser can read what the person who wrote the parser expected.
func fixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// accepted mirrors what the database's check constraint will do with the value,
// which is the question worth asking of an engine's vocabulary.
func accepted(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func TestAReviewThatFoundSomethingBecomesFindings(t *testing.T) {
	parsed, err := parseReview(fixture(t, "review-complete.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "complete" {
		t.Fatalf("status is %q; this fixture is meant to be a finished review", parsed.Status)
	}
	if len(parsed.Comments) != 1 {
		t.Fatalf("read %d comment(s) from a document with one", len(parsed.Comments))
	}
	comment := parsed.Comments[0]
	// The line number is the engine's own work — it matches the code the model
	// quoted against the diff — and it is the whole reason a finding is worth
	// more than a paragraph. A parser that dropped it would leave the console
	// pointing at line zero of every file.
	if comment.StartLine <= 0 {
		t.Errorf("the finding has no line: %+v", comment)
	}
	if comment.Path == "" || comment.Severity == "" || comment.Category == "" {
		t.Errorf("the finding is missing what makes it actionable: %+v", comment)
	}
	if !accepted(store.ReviewSeverities, comment.Severity) {
		t.Errorf("severity %q is not one the database will accept", comment.Severity)
	}
	if !accepted(store.ReviewCategories, comment.Category) {
		t.Errorf("category %q is not one the database will accept", comment.Category)
	}
	if parsed.Summary.TotalTokens <= 0 {
		t.Error("the review reports no tokens, so it would be recorded as free")
	}
	if parsed.Manifest.Input.ResolvedBase == "" || parsed.Manifest.Input.ResolvedHead == "" {
		t.Error("the manifest carries no resolved commits, so nothing says what was compared")
	}
}

// A review that read nothing and a review that found nothing both produce an
// empty list. Telling them apart is the point of keeping the coverage, and the
// document that says so is the one the engine writes as it exits non-zero — with
// its session id and an error line printed after the JSON.
func TestAReviewThatReadNothingIsNotACleanReview(t *testing.T) {
	parsed, err := parseReview(fixture(t, "review-all-failed.json"), fixture(t, "review-all-failed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Status == "complete" {
		t.Fatal("this fixture is a review where every file failed; it must not read as complete")
	}
	if len(parsed.Comments) != 0 {
		t.Fatalf("a review that read nothing reported %d finding(s)", len(parsed.Comments))
	}
	if len(parsed.Manifest.Coverage.Failed) == 0 {
		t.Fatal("nothing in the coverage says a file failed, so an empty review looks clean")
	}
	if parsed.Manifest.Coverage.Failed[0].Path == "" {
		t.Error("the coverage does not name the file it could not review")
	}
}

// The trailing text is the part that catches a parser out: json.Unmarshal
// refuses a document with anything after it, and that document is the one that
// matters most.
func TestTrailingOutputAfterTheDocumentIsIgnored(t *testing.T) {
	document := fixture(t, "review-complete.json") +
		"\n[ocr] Session: 4169a013 (retry with: --resume 4169a013)\nError: review failed\n"
	parsed, err := parseReview(document, "")
	if err != nil {
		t.Fatalf("a document with the engine's own trailing lines was refused: %v", err)
	}
	if len(parsed.Comments) != 1 {
		t.Fatalf("read %d comment(s) after the trailing lines", len(parsed.Comments))
	}
}

// No document at all has to be an error. An empty review reads as "nothing wrong
// with this code", which is the most expensive thing this runner could say by
// accident.
func TestNoDocumentIsAnErrorRatherThanACleanReview(t *testing.T) {
	if _, err := parseReview("", "ocr: command not found"); err == nil {
		t.Fatal("a run that produced no document was read as a review")
	}
	if _, err := parseReview("not json at all", ""); err == nil {
		t.Fatal("output that is not a document was read as a review")
	}
}

func TestTheCommandSaysWhatToCompare(t *testing.T) {
	base := []string{"/usr/local/bin/agenthub-ocr-run"}
	for _, one := range []struct {
		name string
		goal store.AgentGoal
		want []string
	}{
		{"workspace", store.AgentGoal{ReviewMode: "workspace"}, []string{"review"}},
		{"a branch against a base", store.AgentGoal{ReviewMode: "range", ReviewBaseRef: "main", ReviewHeadRef: "feature"}, []string{"review", "--from", "main", "--to", "feature"}},
		{"one commit", store.AgentGoal{ReviewMode: "commit", ReviewHeadRef: "9f2c1ab"}, []string{"review", "--commit", "9f2c1ab"}},
		{"the whole repository", store.AgentGoal{ReviewMode: "scan"}, []string{"scan"}},
		{"one directory", store.AgentGoal{ReviewMode: "scan", ReviewPath: "internal/auth"}, []string{"scan", "--path", "internal/auth"}},
	} {
		command := strings.Join(reviewCommand(base, one.goal), " ")
		for _, want := range one.want {
			if !strings.Contains(command, want) {
				t.Errorf("%s: the command does not carry %q: %s", one.name, want, command)
			}
		}
		// Without these the engine writes for a human terminal, and nothing the
		// platform stores can be read out of it.
		if !strings.Contains(command, "--format json") || !strings.Contains(command, "--audience agent") {
			t.Errorf("%s: the command does not ask for a document a program can read: %s", one.name, command)
		}
	}
}

// The Goal's guardrails have to reach the engine, or a budget somebody set in
// the console is a number the console keeps to itself.
func TestTheGoalsBudgetsReachTheEngine(t *testing.T) {
	command := strings.Join(reviewCommand([]string{"ocr"}, store.AgentGoal{
		ReviewMode: "workspace", TokenBudget: 40000, MaxDurationSeconds: 600, ReviewExclude: "**/testdata/*",
	}), " ")
	for _, want := range []string{"--max-tokens-budget 40000", "--timeout 10", "--exclude **/testdata/*"} {
		if !strings.Contains(command, want) {
			t.Errorf("the command does not carry %q: %s", want, command)
		}
	}
}

// The gate is the reason a review can fail a task, and an empty floor is the
// deployment saying it has not asked for one.
func TestTheGateBlocksOnlyWhatItWasAskedTo(t *testing.T) {
	findings := []store.ReviewFinding{
		{Severity: "critical", FilePath: "a.go", StartLine: 1},
		{Severity: "medium", FilePath: "b.go", StartLine: 2},
		{Severity: "low", FilePath: "c.go", StartLine: 3},
	}
	if blocking := blockingFindings(findings, ""); len(blocking) != 0 {
		t.Errorf("a Goal with no gate blocked %d finding(s); a gate nobody chose must not start blocking work", len(blocking))
	}
	if blocking := blockingFindings(findings, "high"); len(blocking) != 1 {
		t.Errorf("a gate at high blocked %d finding(s), want the one critical", len(blocking))
	}
	if blocking := blockingFindings(findings, "low"); len(blocking) != 3 {
		t.Errorf("a gate at low blocked %d finding(s), want all three", len(blocking))
	}
}
