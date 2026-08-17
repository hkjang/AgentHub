package workflow

import (
	"context"
	"strings"
	"testing"
)

// scriptedCompletion answers each step from a table, so a consensus can be set
// up exactly — unanimous, split, tied — without a model.
type scriptedCompletion struct {
	byAgent map[string]string
	prompts map[string]string
	systems map[string]string
}

func (s *scriptedCompletion) Complete(_ context.Context, step Step, prompt string) (string, error) {
	if s.prompts == nil {
		s.prompts = map[string]string{}
		s.systems = map[string]string{}
	}
	s.prompts[step.ID] = prompt
	s.systems[step.ID] = step.SystemPrompt
	return s.byAgent[step.ID], nil
}

func consensusSteps(ids ...string) []Step {
	steps := make([]Step, 0, len(ids))
	for i, id := range ids {
		step := Step{ID: id, AgentID: "agent-" + id, AgentName: strings.ToUpper(id), SystemPrompt: "당신은 검토자입니다."}
		// Wired as a chain, which is how the console saved consensus workflows
		// before the mode did anything.
		if i > 0 {
			step.DependsOn = []string{ids[i-1]}
		}
		steps = append(steps, step)
	}
	return steps
}

func TestConsensusAsksEveryoneIndependently(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{
		"a": "근거...\nVOTE: 롤백",
		"b": "근거...\nVOTE: 롤백",
		"c": "근거...\nVOTE: 롤백",
	}}
	result, err := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b", "c"), Guardrails{}, "지금 배포를 롤백해야 하는가?")
	if err != nil {
		t.Fatal(err)
	}
	// A chain would run in three levels and let each agent read the previous
	// answer, which is not an independent vote.
	if len(result.Levels) != 1 || len(result.Levels[0]) != 3 {
		t.Fatalf("participants must run in one independent level, got %#v", result.Levels)
	}
	for id, prompt := range script.prompts {
		if strings.Contains(prompt, "이전 단계 결과") {
			t.Fatalf("participant %s saw another agent's answer:\n%s", id, prompt)
		}
		if !strings.Contains(prompt, "롤백해야 하는가") {
			t.Fatalf("participant %s did not get the original question:\n%s", id, prompt)
		}
	}
	for id, system := range script.systems {
		if !strings.Contains(system, voteMarker) {
			t.Fatalf("participant %s was not told how to vote: %q", id, system)
		}
		if !strings.Contains(system, "당신은 검토자입니다.") {
			t.Fatalf("participant %s lost its own system prompt: %q", id, system)
		}
	}
	if result.Consensus == nil || !result.Consensus.Unanimous || result.Consensus.Agreed != 3 {
		t.Fatalf("tally = %#v, want unanimous 3/3", result.Consensus)
	}
	if !strings.Contains(result.Output, "만장일치") || !strings.Contains(result.Output, "롤백") {
		t.Fatalf("output should lead with the verdict:\n%s", result.Output)
	}
}

func TestConsensusReportsAMajorityRatherThanTheLastAnswer(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{
		"a": "VOTE: 롤백",
		"b": "VOTE: 롤백",
		"c": "VOTE: 유지",
	}}
	result, _ := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b", "c"), Guardrails{}, "질문")
	tally := result.Consensus
	if tally.Winner != "롤백" || tally.Agreed != 2 || tally.Total != 3 || tally.Unanimous || tally.Tie {
		t.Fatalf("tally = %#v, want a 2/3 majority for 롤백", tally)
	}
	if !strings.Contains(result.Output, "다수결") {
		t.Fatalf("a split vote must be reported as a majority:\n%s", result.Output)
	}
	// The dissent has to survive into the record, or the decision cannot be
	// reviewed later.
	if !strings.Contains(result.Output, "유지") {
		t.Fatalf("the minority position is missing:\n%s", result.Output)
	}
}

// A tie is not a decision, and reporting the alphabetically first answer as if
// it won would hide that.
func TestConsensusCallsOutATie(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{"a": "VOTE: 롤백", "b": "VOTE: 유지"}}
	result, _ := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b"), Guardrails{}, "질문")
	if !result.Consensus.Tie || result.Consensus.Unanimous {
		t.Fatalf("tally = %#v, want a tie", result.Consensus)
	}
	if !strings.Contains(result.Output, "동률") {
		t.Fatalf("a tie must say so:\n%s", result.Output)
	}
}

func TestConsensusRecordsAbstentionRatherThanGuessing(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{
		"a": "VOTE: 롤백",
		"b": "판단하기 어렵습니다.",
	}}
	result, _ := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b"), Guardrails{}, "질문")
	tally := result.Consensus
	if tally.Total != 1 || tally.Agreed != 1 || !tally.Unanimous {
		t.Fatalf("an abstention must not count as a vote: %#v", tally)
	}
	if len(tally.Votes) != 2 || !tally.Votes[1].Abstained {
		t.Fatalf("the abstention must still be recorded: %#v", tally.Votes)
	}
	if !strings.Contains(result.Output, "기권") {
		t.Fatalf("the abstention should be visible:\n%s", result.Output)
	}
}

func TestConsensusWithNoVotesDoesNotInventOne(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{"a": "모르겠습니다", "b": "역시 모르겠습니다"}}
	result, _ := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b"), Guardrails{}, "질문")
	if result.Consensus.Winner != "" || result.Consensus.Total != 0 {
		t.Fatalf("tally = %#v, want no winner", result.Consensus)
	}
	if !strings.Contains(result.Output, "판정하지 못했습니다") {
		t.Fatalf("the run must say no consensus was reached:\n%s", result.Output)
	}
}

func TestVoteExtractionToleratesHowModelsWrite(t *testing.T) {
	cases := map[string]string{
		"VOTE: 롤백":       "롤백",
		"**VOTE: 롤백**":   "롤백",
		"결론\n`VOTE: 롤백`": "롤백",
		"vote: rollback": "rollback",
		"VOTE: 유지\n다시 생각하니\nVOTE: 롤백": "롤백",
		"  VOTE:   롤백   ":             "롤백",
	}
	for output, want := range cases {
		got, ok := extractVote(output)
		if !ok || got != want {
			t.Errorf("extractVote(%q) = %q,%v, want %q", output, got, ok, want)
		}
	}
	for _, output := range []string{"", "VOTE:", "VOTE:   ", "그 문제는 VOTE: 로 정합시다 라고 설명했다"} {
		if got, ok := extractVote(output); ok && strings.TrimSpace(got) == "" {
			t.Errorf("extractVote(%q) returned an empty vote", output)
		}
	}
}

// Two agents that mean the same thing rarely type it the same way.
func TestVotesAreComparedAfterNormalising(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{
		"a": "VOTE: 즉시 롤백",
		"b": "VOTE: 즉시  롤백.",
		"c": "VOTE: 즉시 롤백!",
	}}
	result, _ := New(script).Run(context.Background(), "consensus", consensusSteps("a", "b", "c"), Guardrails{}, "질문")
	if !result.Consensus.Unanimous || result.Consensus.Agreed != 3 {
		t.Fatalf("spacing and punctuation must not split a vote: %#v", result.Consensus)
	}
	// The winner keeps the first participant's wording rather than the
	// normalised form, which would read as machine output.
	if result.Consensus.Winner != "즉시 롤백" {
		t.Fatalf("winner = %q, want the agent's own wording", result.Consensus.Winner)
	}
}

// Every other mode must be untouched by this.
func TestOtherModesStillFollowTheirGraph(t *testing.T) {
	script := &scriptedCompletion{byAgent: map[string]string{"a": "first", "b": "second"}}
	result, err := New(script).Run(context.Background(), "sequential", consensusSteps("a", "b"), Guardrails{}, "질문")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Levels) != 2 {
		t.Fatalf("a sequential run must keep its chain, got %#v", result.Levels)
	}
	if result.Consensus != nil {
		t.Fatalf("only a consensus run carries a tally: %#v", result.Consensus)
	}
	if !strings.Contains(script.systems["a"], "당신은 검토자입니다.") || strings.Contains(script.systems["a"], voteMarker) {
		t.Fatalf("a non-consensus run must not be told to vote: %q", script.systems["a"])
	}
	if result.Output != "second" {
		t.Fatalf("output = %q, want the last step's answer", result.Output)
	}
}
