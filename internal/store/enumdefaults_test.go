package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A value the API accepts and the store quietly replaces is the worst kind of
// bug to find: the save succeeds, the screen shows what was asked for until it
// is reloaded, and the agent runs as something else.
//
// It has happened twice — a `review` runner saved as `prose`, and a `trigger`
// review mode saved as `workspace` — because each of these functions carries its
// own copy of the list and one copy was updated. This reads the list the
// database itself enforces and checks the Go against it.
func TestEveryValueTheDatabaseAcceptsSurvivesTheStore(t *testing.T) {
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	// The last migration to constrain a column is the one in force.
	constraints := map[string][]string{}
	pattern := regexp.MustCompile(`CHECK \((runner|review_mode) IN \(([^)]*)\)\)`)
	for _, entry := range migrations {
		body, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			values := []string{}
			for _, raw := range strings.Split(match[2], ",") {
				values = append(values, strings.Trim(strings.TrimSpace(raw), "'"))
			}
			constraints[match[1]] = values
		}
	}
	if len(constraints) != 2 {
		t.Fatalf("read %d constrained column(s) from the migrations; this guard is not looking at them", len(constraints))
	}

	for _, value := range constraints["runner"] {
		if got := runnerOrDefault(value); got != value {
			t.Errorf("the database accepts runner %q and the store saves it as %q", value, got)
		}
	}
	for _, value := range constraints["review_mode"] {
		if got := reviewModeOrDefault(value); got != value {
			t.Errorf("the database accepts review mode %q and the store saves it as %q", value, got)
		}
	}
	// And the other direction: something the database would refuse must not reach
	// it, or the save fails at the driver with a constraint name.
	if got := runnerOrDefault("nonsense"); got != RunnerProse {
		t.Errorf("an unknown runner became %q rather than the default", got)
	}
	if got := reviewModeOrDefault("nonsense"); got != "workspace" {
		t.Errorf("an unknown review mode became %q rather than the default", got)
	}
}
