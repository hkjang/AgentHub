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
		for index, line := range strings.Split(string(body), "\n") {
			if guessing.MatchString(line) {
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
