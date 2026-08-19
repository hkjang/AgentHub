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

// Every kind the platform offers a model has to be a kind the parser accepts.
// The first HANDOFF request was dropped exactly here: the prompt asked for it and
// the parser did not know it, so the model kept asking and nothing happened.
func TestEveryOfferedDirectiveIsParsed(t *testing.T) {
	offered := []string{directiveArtifact, directiveMemory, directiveApproval, directiveDelegate, directiveHandoff}
	for _, kind := range offered {
		output := "설명 문장\n" + directiveOpen + kind + " 인자\n본문 내용" + directiveClose + "\n"
		directives := directivesOfKind(output, kind)
		if len(directives) != 1 {
			t.Fatalf("%s was not parsed from %q", kind, output)
		}
		if directives[0].Arg != "인자" || directives[0].Body != "본문 내용" {
			t.Fatalf("%s parsed as %#v", kind, directives[0])
		}
	}
	// And the prompt must not offer one the parser rejects.
	for _, kind := range offered {
		if !knownDirectives[kind] {
			t.Errorf("%s is offered to the model but the parser does not accept it", kind)
		}
	}
	// Anything else is still ignored: prose that happens to look fenced must not
	// become an approval request.
	if got := parseDirectives(directiveOpen + "SHUTDOWN now\n지금 종료" + directiveClose); len(got) != 0 {
		t.Fatalf("an unknown directive was accepted: %#v", got)
	}
}
