package execution

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// What a review would do, read from what the engine actually printed.
//
// Both fixtures came out of the review engine in the image this platform ships,
// rather than from an idea of its output. That is not ceremony: the first version
// of this parser read the exclusion reason as "reason" and the engine calls it
// "exclude_reason", so the answer to "why was my file not reviewed" — the whole
// point of the excluded list — was being dropped in silence.
func TestAReviewPlanIsReadFromWhatTheEnginePrints(t *testing.T) {
	preview, err := os.ReadFile("testdata/reviewplan_preview.json")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile("testdata/reviewplan_rules.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := parseReviewPlan(string(preview), string(rules))
	if err != nil {
		t.Fatalf("the engine's own output could not be read: %v", err)
	}
	if plan.Mode != "workspace" {
		t.Errorf("the plan is for mode %q", plan.Mode)
	}
	if len(plan.Reviewable) != 1 || plan.Reviewable[0].Path != "a.go" {
		t.Errorf("the reviewable files are %v", plan.Reviewable)
	}
	if len(plan.Excluded) != 1 {
		t.Fatalf("the excluded files are %v", plan.Excluded)
	}
	// The reason is the point of the list: without it, "your file was skipped"
	// is a statement somebody can do nothing with.
	if plan.Excluded[0].Reason != "unsupported_ext" {
		t.Errorf("the excluded file gives no reason: %+v", plan.Excluded[0])
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("expected two rule groups, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Pattern != "**/*.go" || plan.Groups[0].Files[0] != "a.go" {
		t.Errorf("the first group is %+v", plan.Groups[0])
	}
	// The standard itself travels, because a name is not something a person can
	// check their code against.
	if !strings.Contains(plan.Groups[0].Rule, "Go Review") {
		t.Errorf("the group carries no rule text: %q", plan.Groups[0].Rule)
	}
}

// TestAPlanWithNothingToReviewIsStillAnAnswer — no reviewable file means no rule
// groups, and failing there would turn "nothing changed" into an error.
func TestAPlanWithNothingToReviewIsStillAnAnswer(t *testing.T) {
	// The keys are absent rather than empty, which is what a document without any
	// change looks like — and what turns into nulls a console renders as broken.
	empty := `{"schema_version":"1","mode":"workspace","repository":"/x"}`
	plan, err := parseReviewPlan(empty, "")
	if err != nil {
		t.Fatalf("an empty plan was reported as a failure: %v", err)
	}
	if plan.Reviewable == nil || plan.Excluded == nil || plan.Groups == nil {
		t.Error("an empty plan carries nulls rather than empty lists, which a console renders as broken")
	}
	// And output that is not a plan at all is a failure rather than an empty one.
	if _, err := parseReviewPlan("command not found", ""); err == nil {
		t.Error("output that is not a plan was accepted as one")
	}
	if _, err := parseReviewPlan(`{"hello":"world"}`, ""); err == nil {
		t.Error("a JSON document that is not a plan was accepted as one")
	}
}

// TestThePlanAsksAboutTheReviewThatWouldRun — a plan for a different comparison
// than the Goal describes answers a question nobody asked.
func TestThePlanAsksAboutTheReviewThatWouldRun(t *testing.T) {
	ranged := store.AgentGoal{ReviewMode: "range", ReviewBaseRef: "main", ReviewHeadRef: "feature/login"}
	argv := reviewPlanCommand(ranged, "preview", nil)
	if !strings.Contains(strings.Join(argv, " "), "--from main --to feature/login") {
		t.Errorf("a branch comparison is not asked about: %v", argv)
	}
	// A ref carrying shell punctuation is left out rather than passed on, the same
	// rule the review runner follows.
	dangerous := store.AgentGoal{ReviewMode: "range", ReviewBaseRef: "main; rm -rf /", ReviewHeadRef: "x"}
	if joined := strings.Join(reviewPlanCommand(dangerous, "preview", nil), " "); strings.Contains(joined, "rm -rf") {
		t.Errorf("a ref with shell punctuation was passed to the engine: %v", joined)
	}
	// The files come last, so the rules call is about the files the preview found.
	withFiles := reviewPlanCommand(store.AgentGoal{}, "rule", []string{"a.go", "b.py"})
	if withFiles[len(withFiles)-2] != "a.go" || withFiles[len(withFiles)-1] != "b.py" {
		t.Errorf("the files are not passed to the rules call: %v", withFiles)
	}
	if !strings.Contains(strings.Join(withFiles, " "), "--format json") {
		t.Error("the plan asks for text rather than something a program can read")
	}
}
