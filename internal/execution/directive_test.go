package execution

import (
	"strings"
	"testing"
)

func TestParseDirectivesReadsEveryKind(t *testing.T) {
	output := `분석을 마쳤습니다.

<<<ARTIFACT report.md
# 보고서
원인은 커넥션 풀 고갈입니다.
>>>

<<<MEMORY 담당팀
결제 시스템은 코어뱅킹팀이 담당합니다.
>>>

<<<APPROVAL 결제 서비스 재시작
payment-api Deployment를 롤링 재시작해야 합니다.
>>>

<<<DELEGATE Infra Agent
노드 메모리 사용량을 점검해 주세요.
>>>`
	directives := parseDirectives(output)
	if len(directives) != 4 {
		t.Fatalf("expected four directives, got %d: %#v", len(directives), directives)
	}
	kinds := map[string]Directive{}
	for _, directive := range directives {
		kinds[directive.Kind] = directive
	}
	if kinds[directiveArtifact].Arg != "report.md" {
		t.Errorf("artifact arg = %q", kinds[directiveArtifact].Arg)
	}
	if kinds[directiveMemory].Arg != "담당팀" || !strings.Contains(kinds[directiveMemory].Body, "코어뱅킹팀") {
		t.Errorf("memory directive is wrong: %#v", kinds[directiveMemory])
	}
	if !strings.Contains(kinds[directiveApproval].Arg, "재시작") {
		t.Errorf("approval arg = %q", kinds[directiveApproval].Arg)
	}
	if kinds[directiveDelegate].Arg != "Infra Agent" {
		t.Errorf("delegate target = %q", kinds[directiveDelegate].Arg)
	}
}

// Describing a directive must not trigger it. An agent explaining the protocol,
// or a report that quotes it, would otherwise request approvals it never meant.
func TestUnknownFencesAreIgnored(t *testing.T) {
	for _, output := range []string{
		"<<<SOMETHING else\nbody\n>>>",
		"approval 을 요청하려면 APPROVAL 지시자를 쓰라고 합니다.",
		"<<<APPROVAL 잘린 요청\n본문이 끝나지 않음",
		"<<<\n헤더가 없음\n>>>",
	} {
		if got := parseDirectives(output); len(got) != 0 {
			t.Errorf("input %q must yield no directives, got %#v", output, got)
		}
	}
}

func TestDirectivesOfKindFiltersOthersOut(t *testing.T) {
	output := "<<<MEMORY a\n1\n>>>\n<<<ARTIFACT b.md\n2\n>>>\n<<<MEMORY c\n3\n>>>"
	if got := directivesOfKind(output, directiveMemory); len(got) != 2 {
		t.Fatalf("expected two memory directives, got %d", len(got))
	}
	if got := directivesOfKind(output, directiveDelegate); len(got) != 0 {
		t.Fatalf("expected no delegate directives, got %d", len(got))
	}
}

func TestDirectiveKindIsCaseInsensitiveButBodyIsVerbatim(t *testing.T) {
	got := parseDirectives("<<<memory 키\n  값에 공백과 <<< 같은 기호가 있어도 된다  \n>>>")
	if len(got) != 1 || got[0].Kind != directiveMemory {
		t.Fatalf("a lowercase kind must still parse: %#v", got)
	}
	if !strings.Contains(got[0].Body, "<<<") {
		t.Fatalf("the body must be preserved verbatim: %q", got[0].Body)
	}
}

// The artifact extractor is now built on the shared parser, so its previous
// behaviour has to survive.
func TestArtifactExtractionStillWorksThroughTheSharedParser(t *testing.T) {
	artifacts := extractArtifacts("<<<ARTIFACT fix.patch\n--- a\n+++ b\n>>>")
	if len(artifacts) != 1 || artifacts[0].Type != "patch" || artifacts[0].Name != "fix.patch" {
		t.Fatalf("artifact extraction regressed: %#v", artifacts)
	}
	if got := extractArtifacts("<<<ARTIFACT \n이름이 없음\n>>>"); len(got) != 0 {
		t.Fatalf("an unnamed artifact must be skipped, got %#v", got)
	}
}
