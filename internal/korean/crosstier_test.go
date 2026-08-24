package korean

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Both tiers write Korean sentences, so both need this rule — and two copies of
// a table is how the console and the server came to disagree about everything
// else in this codebase that has ever been written twice.
//
// The console cannot import Go, so the copy is allowed; what is not allowed is
// for it to drift. This reads the console's table and compares it with this
// package's.
func TestTheConsoleReadsDigitsTheSameWay(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "korean.ts"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	entries := regexp.MustCompile(`"(\d)":\s*(true|false)`).FindAllStringSubmatch(string(body), -1)
	if len(entries) != len(digitEndsInConsonant) {
		t.Fatalf("the console declares %d digits and this package %d", len(entries), len(digitEndsInConsonant))
	}
	for _, entry := range entries {
		digit := rune(entry[1][0])
		console := entry[2] == "true"
		if server, ok := digitEndsInConsonant[digit]; !ok || server != console {
			t.Errorf("%c: the console says ends-in-consonant=%v, this package says %v", digit, console, server)
		}
	}
}
