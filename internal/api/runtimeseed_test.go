package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The console carries its own list of runtime types, to draw something before
// the platform answers and if it never does. That list is a copy, and it had
// drifted: three runtimes this build supports — Open Code Review, Orca and Pi —
// were missing from it, so nobody could register an image for them, filter
// agents by them, or name one as an evaluation condition. Nothing looked broken.
// The lists were simply different, and only one of them was true.
func TestTheConsoleKnowsEveryRuntimeThisBuildSupports(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "runtime.ts"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "const SEED")
	end := strings.Index(text, "const FALLBACK")
	if start < 0 || end < 0 || end < start {
		t.Fatal("the console's seeded descriptions are gone; this guard is reading nothing")
	}
	seeded := map[string]bool{}
	for _, match := range regexp.MustCompile(`\n  ([a-z0-9]+): \{`).FindAllStringSubmatch(text[start:end], -1) {
		seeded[match[1]] = true
	}
	if len(seeded) < 10 {
		t.Fatalf("found only %d seeded runtimes; the pattern this test reads by has probably changed", len(seeded))
	}

	var missing []string
	for _, supported := range runtimetype.Supported {
		if !seeded[supported] {
			missing = append(missing, supported)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the console cannot offer %v — this build supports them and its list does not say so", missing)
	}

	// And the other way: a console offering a runtime the platform cannot spawn
	// is a choice that fails after it is made.
	for name := range seeded {
		if !runtimetype.IsSupported(name) {
			t.Errorf("the console offers %q, which this build cannot run", name)
		}
	}
}
