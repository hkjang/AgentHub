package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every way of running has to leave a trail that can be attributed to it.
//
// A run does not record which backend produced it — it records the steps that
// backend wrote, and their type is the backend's fingerprint. A backend with no
// step type has no evidence trail at all: it can never be shown to have worked
// here, and it never appears in what this deployment has done. That is a silent
// hole, so it is a guard.
func TestEveryRunnerWritesAStepThatIdentifiesIt(t *testing.T) {
	// The list the database enforces, read from the migration that enforces it,
	// so a runner added to the constraint and forgotten here is caught.
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	runnerPattern := regexp.MustCompile(`CHECK \(runner IN \(([^)]*)\)\)`)
	stepPattern := regexp.MustCompile(`CHECK \(type IN \(([^)]*)\)\)`)
	runners, steps := []string{}, []string{}
	for _, entry := range migrations {
		body, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range runnerPattern.FindAllStringSubmatch(string(body), -1) {
			runners = split(match[1])
		}
		for _, match := range stepPattern.FindAllStringSubmatch(string(body), -1) {
			steps = split(match[1])
		}
	}
	if len(runners) < 6 || len(steps) < 6 {
		t.Fatalf("read %d runner(s) and %d step type(s); this guard is not looking at them", len(runners), len(steps))
	}

	seen := map[string]string{}
	for _, runner := range runners {
		step := RunnerStepType(runner)
		if step == "" {
			t.Errorf("runner %q writes no step type, so nothing it does can be attributed to it — it will read as never used, for ever", runner)
			continue
		}
		if !contains(steps, step) {
			t.Errorf("runner %q claims step type %q, which the database refuses", runner, step)
		}
		// Two backends sharing a step type would credit one with the other's work.
		if other, taken := seen[step]; taken {
			t.Errorf("runners %q and %q both write %q; one would be credited with the other's runs", other, runner, step)
		}
		seen[step] = runner
	}

	// And the list the platform iterates has to be the list the database accepts,
	// or a backend exists that no report ever asks about.
	for _, runner := range runners {
		if !contains(Runners, runner) {
			t.Errorf("runner %q is accepted by the database but absent from Runners; nothing reports on it", runner)
		}
	}
	for _, runner := range Runners {
		if !contains(runners, runner) {
			t.Errorf("runner %q is reported on but the database would refuse it", runner)
		}
	}
}

func split(list string) []string {
	values := []string{}
	for _, raw := range strings.Split(list, ",") {
		values = append(values, strings.Trim(strings.TrimSpace(raw), "'"))
	}
	return values
}
