package buildinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A workflow GitHub cannot parse is a release that does not happen.
//
// The v0.99.0 release failed before a single job started, with the run's name
// showing as the file path rather than the workflow's — GitHub's way of saying
// it could not read the file. The cause was three duplicated `env:` keys, added
// by an edit that inserted a line by pattern.
//
// It passed the check that was run at the time, which is the part worth fixing.
// Python's YAML loader takes the last of a duplicated key without complaining,
// so "it parses" was true and meant nothing. This decodes with duplicate keys
// treated as the error GitHub treats them as, and it reads every workflow rather
// than the one being edited.
func TestEveryWorkflowIsOneGitHubCanRead(t *testing.T) {
	files, err := filepath.Glob("../../.github/workflows/*.y*ml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("found %d workflow(s); this guard is not reading them", len(files))
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// KnownFields is not enough on its own — the failure here was a repeated
		// key, and yaml.v3 reports that as an error while a permissive loader
		// silently keeps the last one.
		var document map[string]any
		decoder := yaml.NewDecoder(strings.NewReader(string(body)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&document); err != nil {
			t.Errorf("%s is not a document GitHub can read: %v", filepath.Base(file), err)
			continue
		}
		// A workflow with no name shows in the runs list as its file path, which
		// is what made the broken release hard to recognise.
		if name, _ := document["name"].(string); strings.TrimSpace(name) == "" {
			t.Errorf("%s has no name; its runs show as a file path", filepath.Base(file))
		}
		if _, ok := document["jobs"]; !ok {
			t.Errorf("%s declares no jobs", filepath.Base(file))
		}
	}
}
