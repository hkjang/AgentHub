package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every step type the platform writes has to be one the database accepts.
//
// It was not, and the way that failed is why this test exists: a rejected step
// is logged and the run carries on, so the flow and CLI runners recorded a step
// count with no steps behind it and nothing surfaced until a task ran on a real
// cluster. Reading the constraint out of the migrations is the cheapest way to
// keep the two lists honest without a database.
func TestEveryRunStepTypeIsAllowedByTheSchema(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	// The last migration that defines the constraint is the one in force.
	pattern := regexp.MustCompile(`(?s)agent_run_steps_type_check[^;]*?CHECK\s*\(\s*type IN \(([^)]*)\)`)
	allowed := map[string]bool{}
	found := false
	for _, name := range names {
		body, readErr := os.ReadFile(filepath.Join("migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			allowed = map[string]bool{}
			for _, value := range strings.Split(match[1], ",") {
				allowed[strings.Trim(strings.TrimSpace(value), "'")] = true
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no migration defines the run step type constraint")
	}
	for _, stepType := range RunStepTypes {
		if !allowed[stepType] {
			t.Errorf("the platform writes %q steps but the schema refuses them", stepType)
		}
	}
}
