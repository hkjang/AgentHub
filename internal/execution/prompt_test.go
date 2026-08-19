package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// The prompt has to say what this loop cannot do. Without that sentence models
// report edits they never made, which is the single most expensive failure this
// platform can produce: a task that looks finished and changed nothing.
func TestPromptStatesWhatTheRunCannotDo(t *testing.T) {
	prompt := systemPromptWithEnvironment(
		store.Agent{Name: "결산", RuntimeType: runtimetype.OpenCode},
		store.AgentGoal{Description: "월말 결산 정리", MaxSteps: 5},
		environment{Runtime: runtimetype.Describe(runtimetype.OpenCode), WorkspaceName: "finance", Tools: []string{"jira", "github"}, HandoffAllowed: true, RuntimeReady: true},
	)
	for _, expected := range []string{
		"OpenCode",     // which runtime it is attached to
		"finance",      // the workspace by name
		"/workspace",   // and where it is mounted
		"jira, github", // the tools it has, by name
		"파일을 직접 편집하거나", // and the limit, stated plainly
		"하지 않은 일을 했다고 쓰지 마세요",
		"HANDOFF", // with the way out
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("the prompt does not mention %q:\n%s", expected, prompt)
		}
	}
}

// An agent with no workspace is told so, because a file it writes there is gone
// with the Pod and it should plan differently.
func TestPromptIsHonestAboutAnEphemeralRuntime(t *testing.T) {
	prompt := systemPromptWithEnvironment(
		store.Agent{Name: "조사", RuntimeType: runtimetype.Hermes},
		store.AgentGoal{MaxSteps: 3},
		environment{Runtime: runtimetype.Describe(runtimetype.Hermes)},
	)
	if !strings.Contains(prompt, "영속 작업공간이 연결되어 있지 않습니다") {
		t.Errorf("the prompt must say the files will not survive:\n%s", prompt)
	}
	// Nothing may offer a handover that cannot happen.
	if strings.Contains(prompt, "HANDOFF") {
		t.Errorf("handover was offered without anywhere to hand it to:\n%s", prompt)
	}
}

// The note a person reads has to survive whichever half of the directive the
// model filled in.
func TestHandoffNote(t *testing.T) {
	cases := []struct {
		name      string
		directive Directive
		expect    string
	}{
		{name: "both halves", directive: Directive{Arg: "빌드 스크립트 수정", Body: "npm run build 실패"}, expect: "빌드 스크립트 수정\n\nnpm run build 실패"},
		{name: "summary only", directive: Directive{Arg: "빌드 스크립트 수정"}, expect: "빌드 스크립트 수정"},
		{name: "detail only", directive: Directive{Body: "npm run build 실패"}, expect: "npm run build 실패"},
		{name: "neither, so the platform says something usable", directive: Directive{}, expect: "런타임에서 직접"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := handoffNote(test.directive); !strings.Contains(got, test.expect) {
				t.Fatalf("handoffNote = %q, want it to contain %q", got, test.expect)
			}
		})
	}
}
