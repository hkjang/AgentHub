package korean

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A particle written straight after an interpolated value is wrong for half the
// values it will be given. The platform shipped several — "리뷰이", "v3는",
// "환경변수 %s가 중복되었습니다" — and each of them is a sentence somebody reads
// when something has gone wrong, in a product whose users speak Korean.
//
// This is the sweep that found them, kept so the next one fails the build
// instead of shipping. Rewriting the sentence without a particle is as good an
// answer as using the helpers here: what is not allowed is guessing.
func TestNoSentenceGuessesAParticle(t *testing.T) {
	root := filepath.Join("..", "..")
	// Go interpolates with %s and the console with ${…}; both end in a value
	// followed, in these mistakes, by a particle that cannot know what came
	// before it.
	guessing := regexp.MustCompile(`(%[sdv]|\})(이|가|을|를|은|는)[\s"` + "`" + `.,)]`)
	// The same mistake with a different join. A sentence built by concatenation
	// puts the particle at the start of the next literal, where %s never appears
	// — and that is how "결정 기록에 rrn 가 포함되어" and "런타임 2개은
	// 실패했습니다" shipped past the sweep above. The value is on the other side
	// of the +, so the particle is guessing just the same.
	concatenating := regexp.MustCompile(`\+\s*[` + "`" + `"']\s*(이|가|을|를|은|는)[\s"'` + "`" + `.,)]`)
	// And once more, for the join that wraps. A long sentence puts the + at the
	// end of one line and the particle at the start of the next, which reads as
	// clean to a rule that looks at one line at a time — this sweep found the
	// mistake and then let the fix for it through in that shape.
	//
	// Only when the line before it ends in the +. Plenty of sentences open with
	// the demonstrative 이 — "이 Agent에는 진행 중인 작업이 있습니다" — and those
	// are not particles at all; what makes one a particle is the value it was
	// just glued to.
	continuing := regexp.MustCompile(`^\s*[` + "`" + `"']\s*(이|가|을|를|은|는)[\s"'` + "`" + `.,)]`)
	offenders := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "release", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		// Both tiers write these sentences, and the console shipped the same
		// version-number mistake as the server: "현재 정의 v3는 승격되지 않아".
		// One sweep covers both, because a rule enforced on one side only is how
		// the two came to disagree.
		switch {
		case strings.HasSuffix(path, "_test.go"):
			return nil
		case strings.HasSuffix(path, ".go"), strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"):
		default:
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		lines := strings.Split(string(body), "\n")
		for index, line := range lines {
			glued := index > 0 && strings.HasSuffix(strings.TrimSpace(lines[index-1]), "+")
			if guessing.MatchString(line) || concatenating.MatchString(line) ||
				(glued && continuing.MatchString(line)) {
				offenders = append(offenders, filepath.ToSlash(path)+":"+itoa(index+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("these sentences put a fixed particle after an interpolated value, which is wrong for half the values they will be given — use korean.Subject/Object/Topic, or write the sentence without a particle:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
