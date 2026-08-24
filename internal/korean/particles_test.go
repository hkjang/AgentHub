package korean

import "testing"

// The platform shipped "리뷰이 최대 실행 시간에 도달해", which reads as though the
// tool were written by somebody who does not speak the language it is speaking —
// in the sentence somebody reads when something has gone wrong.
func TestAWordTakesTheParticleItsEndingAsksFor(t *testing.T) {
	for word, want := range map[string][3]string{
		"리뷰":   {"가", "를", "는"}, // no final consonant
		"에이전트": {"가", "를", "는"},
		"조사":   {"가", "를", "는"},
		"실행":   {"이", "을", "은"}, // ㅇ
		"작업":   {"이", "을", "은"}, // ㅂ
		"런타임":  {"이", "을", "은"}, // ㅁ
	} {
		got := [3]string{Subject(word), Object(word), Topic(word)}
		if got != want {
			t.Errorf("%s: got %v, want %v", word, got, want)
		}
	}
}

// A version or a count is interpolated as often as a word, and a digit's
// particle follows how the digit is said.
func TestANumberTakesTheParticleItsReadingAsksFor(t *testing.T) {
	for number, want := range map[string]string{
		"v1": "은", // 일
		"v2": "는", // 이
		"v3": "은", // 삼
		"v4": "는", // 사
		"v5": "는", // 오
		"v6": "은", // 육
		"v7": "은", // 칠
		"v8": "은", // 팔
		"v9": "는", // 구
		"10": "은", // 십
		"20": "은", // 이십
	} {
		if got := Topic(number); got != want {
			t.Errorf("%s%s — want %s", number, got, want)
		}
	}
}

// Latin has no rule here, and guessing wrong in both directions is worse than
// being consistent: sentences interpolating those are written without a
// particle instead.
func TestLatinIsAnsweredConsistently(t *testing.T) {
	for _, word := range []string{"github.com", "AWS", "/etc/agenthub", "OPENAI_API_KEY"} {
		if EndsInConsonant(word) {
			t.Errorf("%s was given a rule it does not have", word)
		}
	}
}

func TestAnEmptyWordDoesNotPanic(t *testing.T) {
	if Subject("") != "가" || Object("") != "를" || Topic("") != "는" {
		t.Fatal("an empty word must still answer")
	}
}
