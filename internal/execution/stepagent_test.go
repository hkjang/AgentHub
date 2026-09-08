package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The planner and the judge call the model as the agent they are working for.
//
// Both send the work itself: the planner is given the task's title and input,
// and the judge is given the whole transcript. Neither said which agent it was
// — they carried "Planner" and "Completion Evaluator" as a name and no id at
// all — so a rule naming an agent matched nothing at the two model boundaries
// that carry the most text, and the global action for the data class decided
// instead. Nothing said so: the console's simulator is given an agent, and went
// on answering 차단 for exactly the calls that were being made.
func TestThePlannerAndTheJudgeCallTheModelAsTheAgent(t *testing.T) {
	agent := store.Agent{ID: "agent-1", Name: "지원 봇"}
	model := resolvedModel{BaseURL: "https://models.example", ModelName: "m", APIKey: "k"}

	plan := planStep(store.AgentTask{OwnerID: "user-1"}, agent, model)
	if plan.AgentID != agent.ID || plan.AgentName != agent.Name {
		t.Errorf("the planner calls the model as %q/%q rather than as the agent it plans for",
			plan.AgentID, plan.AgentName)
	}
	judge := judgeStep(&store.AgentRun{OwnerID: "user-1"}, agent, model)
	if judge.AgentID != agent.ID || judge.AgentName != agent.Name {
		t.Errorf("the judge calls the model as %q/%q rather than as the agent it judges",
			judge.AgentID, judge.AgentName)
	}
	// Which boundary it is has to survive: the audit trail records the step
	// beside the agent, and both of these are now the same agent as the run's
	// own turns.
	if plan.ID != "plan" || judge.ID != "judge" {
		t.Errorf("the boundaries no longer name themselves: %q, %q", plan.ID, judge.ID)
	}
	// Everything else the step carries still has to be there, or the call fails
	// for a reason that has nothing to do with policy.
	if plan.ModelBaseURL != model.BaseURL || plan.SystemPrompt == "" || plan.OwnerID != "user-1" {
		t.Errorf("the planner's step lost something on the way: %#v", plan)
	}
	if judge.ModelBaseURL != model.BaseURL || judge.SystemPrompt == "" || judge.OwnerID != "user-1" {
		t.Errorf("the judge's step lost something on the way: %#v", judge)
	}
}

// Every step handed to the inspector has to say which agent it is for.
//
// This is the companion of the owner sweep next to it, and for the same reason:
// the omission is silent. A step with no agent is still scanned, the run still
// finishes, and only a rule that names an agent quietly stops applying.
func TestEveryStepSaysWhichAgentItIsFor(t *testing.T) {
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
			if !strings.Contains(literal, "AgentID") {
				t.Errorf("%s: a step reaches the content inspector without an agent, so no rule about an agent applies to it:\n%s",
					filepath.Base(file), literal)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no steps found; this sweep is reading nothing")
	}
}
