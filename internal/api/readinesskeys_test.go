package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One thing can be wrong in two ways at once: a runtime that keeps dying is also
// a runtime that never became ready, and this screen says both. Drawn from a
// list keyed on the area and the name alone, those two rows collide — React is
// handed two children with the same key and may drop or reorder one.
//
// Seen on a real deployment: two runtime rows, one distinct key between them.
func TestTheReadinessListKeepsItsRowsApart(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "AdminInsights.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	source := string(body)
	at := strings.Index(source, `className="readiness-list"`)
	if at < 0 {
		t.Fatal("the readiness list is gone; this guard is reading nothing")
	}
	row := source[at:]
	if end := strings.Index(row, "</ul>"); end >= 0 {
		row = row[:end]
	}
	// The whole line: a template literal is full of closing braces, so cutting at
	// the first one reads `key={${item.area}` and concludes nothing.
	key := ""
	for _, line := range strings.Split(row, "\n") {
		if strings.Contains(line, "key={") {
			key = line
			break
		}
	}
	if key == "" {
		t.Fatal("the rows are drawn without a key at all")
	}
	// Only the key attribute. The rest of the line styles the row by its verdict,
	// and reading that as part of the key concluded the rows were distinct when
	// they were not — the first version of this guard passed against the very
	// code it was written for.
	key = key[strings.Index(key, "key={"):]
	if end := strings.Index(key, " className="); end >= 0 {
		key = key[:end]
	}
	// Two rows about the same thing differ by their verdict, so the key has to
	// carry it — or the index, which is unique whatever the rows say.
	if !strings.Contains(key, "verdict") && !strings.Contains(key, "index") {
		t.Errorf("two findings about one runtime would share the key %q", key)
	}
}
