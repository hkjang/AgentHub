package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func goalWith(criteria ...string) store.AgentGoal {
	goal := store.DefaultAgentGoal("agent-1")
	goal.SuccessCriteria = criteria
	return goal
}

func TestRuleVerdictRequiresEveryCriterionToBeEvidenced(t *testing.T) {
	goal := goalWith("신규 장애 조회 완료", "관련 로그 분석 완료", "보고서 저장 완료")
	transcript := []string{
		"ITSM에서 신규 장애를 조회 완료했습니다.",
		"관련 로그를 분석 완료했습니다.",
	}
	verdict := ruleVerdict(goal, transcript)
	if verdict.Passed {
		t.Fatal("a task must not pass while a success criterion is unevidenced")
	}
	if len(verdict.Unmet) != 1 || !strings.Contains(verdict.Unmet[0], "보고서") {
		t.Fatalf("the unmet criterion was not identified: %#v", verdict.Unmet)
	}
	if len(verdict.Met) != 2 {
		t.Fatalf("the met criteria were not recorded: %#v", verdict.Met)
	}
}

func TestRuleVerdictPassesWhenEveryCriterionAppears(t *testing.T) {
	goal := goalWith("코드 수정", "테스트 통과")
	verdict := ruleVerdict(goal, []string{"코드 수정을 마쳤고 테스트 통과를 확인했습니다."})
	if !verdict.Passed || len(verdict.Unmet) != 0 {
		t.Fatalf("expected a pass, got %#v", verdict)
	}
}

func TestRuleVerdictAcceptsAParaphrase(t *testing.T) {
	// An agent restates a requirement in its own words; demanding the exact
	// phrase would reject every real completion.
	goal := goalWith("원인 분석 결과 생성")
	verdict := ruleVerdict(goal, []string{"장애의 원인 분석을 마치고 결과를 정리해 생성했습니다."})
	if !verdict.Passed {
		t.Fatalf("a paraphrased criterion must still count: %#v", verdict)
	}
}

func TestAgentClaimAloneDoesNotSatisfyTheRuleStrategy(t *testing.T) {
	// This is the whole point of the evaluator: "완료했습니다" is not completion.
	goal := goalWith("go test PASS", "go build PASS")
	verdict := ruleVerdict(goal, []string{"수정했습니다. TASK_COMPLETE"})
	if verdict.Passed {
		t.Fatal("an unsupported completion claim must not pass the rule strategy")
	}
	if len(verdict.Unmet) != 2 {
		t.Fatalf("both criteria should be unmet: %#v", verdict.Unmet)
	}
}

func TestDeclaresCompletionNeedsTheMarkerOnItsOwnLine(t *testing.T) {
	if !declaresCompletion("작업을 마쳤습니다.\nTASK_COMPLETE") {
		t.Fatal("a marker on its own line must be recognised")
	}
	if !declaresCompletion("TASK_COMPLETE\n") {
		t.Fatal("a trailing newline must not hide the marker")
	}
	// Mentioning the token inside a sentence is not a declaration, otherwise an
	// agent explaining the protocol would end its own run.
	if declaresCompletion("완료되면 TASK_COMPLETE 를 출력하라고 하셨습니다.") {
		t.Fatal("an inline mention must not end the run")
	}
	if declaresCompletion("아직 진행 중입니다.") {
		t.Fatal("ordinary output must not end the run")
	}
}

func TestExtractArtifactsPullsFencedDocuments(t *testing.T) {
	output := `분석을 마쳤습니다.

<<<ARTIFACT incident-report.md
# 장애 보고서

원인은 커넥션 풀 고갈입니다.
>>>

추가로 패치도 만들었습니다.

<<<ARTIFACT fix.patch
--- a/main.go
+++ b/main.go
>>>
TASK_COMPLETE`
	artifacts := extractArtifacts(output)
	if len(artifacts) != 2 {
		t.Fatalf("expected two artifacts, got %d", len(artifacts))
	}
	if artifacts[0].Name != "incident-report.md" || artifacts[0].Type != "report" {
		t.Fatalf("first artifact is wrong: %#v", artifacts[0])
	}
	if !strings.Contains(artifacts[0].Content, "커넥션 풀") {
		t.Fatalf("artifact content was not captured: %q", artifacts[0].Content)
	}
	if artifacts[1].Type != "patch" {
		t.Fatalf("a .patch file must be typed as a patch: %#v", artifacts[1])
	}
}

func TestExtractArtifactsIgnoresAnUnclosedFence(t *testing.T) {
	// Truncated output must not produce a half-written artifact.
	if got := extractArtifacts("<<<ARTIFACT report.md\n내용이 잘렸습니다"); len(got) != 0 {
		t.Fatalf("an unterminated fence must yield nothing, got %#v", got)
	}
}

func TestArtifactNameCannotEscapeIntoAPath(t *testing.T) {
	// The name comes from the model, so it is untrusted input.
	for _, name := range []string{"../../etc/passwd", "a/b/c.md", `..\\windows`} {
		if got := sanitiseArtifactName(name); strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") {
			t.Errorf("sanitiseArtifactName(%q) = %q, which is still path-like", name, got)
		}
	}
	if sanitiseArtifactName("   ") != "artifact" {
		t.Error("a blank name must fall back to a usable default")
	}
}

func TestExtractJSONFindsTheObjectInAWrappedReply(t *testing.T) {
	wrapped := "판정 결과입니다:\n```json\n{\"passed\": false, \"reason\": \"근거 없음\"}\n```"
	if got := extractJSON(wrapped); got != `{"passed": false, "reason": "근거 없음"}` {
		t.Fatalf("extractJSON = %q", got)
	}
}

func TestSystemPromptCarriesGoalAndCriteria(t *testing.T) {
	goal := goalWith("보고서 저장 완료")
	goal.Description = "신규 장애를 분석한다."
	goal.FailureCriteria = []string{"MCP 조회 3회 연속 실패"}
	goal.Constraints = "운영 DB에 쓰기 금지"
	prompt := systemPrompt(store.Agent{Name: "Incident Analyst"}, goal)
	for _, expected := range []string{"신규 장애를 분석한다.", "보고서 저장 완료", "MCP 조회 3회 연속 실패", "운영 DB에 쓰기 금지", completionMarker} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt is missing %q", expected)
		}
	}
}

func TestStepPromptCarriesTheTaskAndPriorTurns(t *testing.T) {
	task := store.AgentTask{Title: "INC-1023 분석", Input: "장애 ID: INC-1023"}
	first := stepPrompt(task, goalWith(), nil)
	if !strings.Contains(first, "INC-1023 분석") || !strings.Contains(first, "장애 ID") {
		t.Fatalf("the first prompt lost the task: %q", first)
	}
	second := stepPrompt(task, goalWith(), []string{"로그를 조회했습니다."})
	if !strings.Contains(second, "로그를 조회했습니다.") {
		t.Fatalf("the follow-up prompt lost the earlier turn: %q", second)
	}
}
