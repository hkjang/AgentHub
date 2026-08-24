// Package korean picks the particle that follows a word.
//
// Korean chooses 이/가, 을/를 and 은/는 by whether the preceding word ends in a
// consonant, so a message that hardcodes one is wrong for half the words it is
// given. That is not a nicety: these sentences are what somebody reads when
// something has gone wrong, and "리뷰이 최대 실행 시간에 도달해" — which this
// platform shipped — reads as though the tool were written by somebody who does
// not speak the language it is speaking.
//
// Numbers are included because a version or a count is interpolated as often as
// a word, and a digit's particle follows how the digit is said: v3 is 삼, which
// ends in ㅁ, so it takes 은 rather than 는.
package korean

// digitEndsInConsonant is how each digit is pronounced, and whether that reading
// ends in a consonant. A number ending in zero is read 영, 십, 백, 천 or 만 —
// every one of which does — so the last digit is enough to decide.
var digitEndsInConsonant = map[rune]bool{
	'0': true,  // 영
	'1': true,  // 일
	'2': false, // 이
	'3': true,  // 삼
	'4': false, // 사
	'5': false, // 오
	'6': true,  // 육
	'7': true,  // 칠
	'8': true,  // 팔
	'9': false, // 구
}

// EndsInConsonant reports whether the last character of word is pronounced with
// a final consonant.
//
// Anything that is neither Hangul nor a digit — a Latin identifier, a path, a
// hostname — is answered false. Latin words genuinely have no rule here (a
// reader says "github.com이" and "AWS는" by sound), and guessing wrong in both
// directions is worse than being consistent; sentences that interpolate those
// are written without a particle instead.
func EndsInConsonant(word string) bool {
	runes := []rune(word)
	if len(runes) == 0 {
		return false
	}
	last := runes[len(runes)-1]
	if ending, ok := digitEndsInConsonant[last]; ok {
		return ending
	}
	if last < 0xAC00 || last > 0xD7A3 {
		return false
	}
	// A Hangul syllable is composed as (initial × 21 + medial) × 28 + final, so
	// a remainder of zero means there is no final consonant.
	return (last-0xAC00)%28 != 0
}

// Subject is 이 or 가.
func Subject(word string) string {
	if EndsInConsonant(word) {
		return "이"
	}
	return "가"
}

// Object is 을 or 를.
func Object(word string) string {
	if EndsInConsonant(word) {
		return "을"
	}
	return "를"
}

// Topic is 은 or 는.
func Topic(word string) string {
	if EndsInConsonant(word) {
		return "은"
	}
	return "는"
}
