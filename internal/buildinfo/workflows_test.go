package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// A mutable action tag can be moved after review and before the next run. The
// full commit is the boundary the release audit can actually reproduce.
func TestEveryExternalActionIsPinnedToACommit(t *testing.T) {
	files, err := filepath.Glob("../../.github/workflows/*.y*ml")
	if err != nil {
		t.Fatal(err)
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	count := 0
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := yaml.Unmarshal(body, &document); err != nil {
			t.Fatalf("decode %s while scanning actions: %v", filepath.Base(file), err)
		}
		for _, reference := range workflowUses(document) {
			if strings.HasPrefix(reference, "./") {
				continue
			}
			count++
			separator := strings.LastIndexByte(reference, '@')
			if separator < 0 || !commit.MatchString(reference[separator+1:]) {
				t.Errorf("%s uses mutable external action %q; pin its full commit SHA", filepath.Base(file), reference)
			}
		}
	}
	if count < 5 {
		t.Fatalf("found only %d external actions; the workflow scan is probably incomplete", count)
	}
}

func workflowUses(node any) []string {
	var references []string
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "uses" {
					if reference, ok := child.(string); ok {
						references = append(references, reference)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(node)
	return references
}

func TestWorkflowUsesFindsNamedAndUnnamedSteps(t *testing.T) {
	var document any
	if err := yaml.Unmarshal([]byte(`
jobs:
  test:
    steps:
      - uses: owner/first@0123456789012345678901234567890123456789
      - id: provenance
        uses: owner/second@abcdefabcdefabcdefabcdefabcdefabcdefabcd
`), &document); err != nil {
		t.Fatal(err)
	}
	got := workflowUses(document)
	if len(got) != 2 || !strings.Contains(got[1], "owner/second@") {
		t.Fatalf("workflow uses = %v, want both direct and named steps", got)
	}
}

// Independent runtime versions are part of the public build response and feed
// the spawner's default image tags. Omitting one here leaves an operator unable
// to tell which image the running control plane will request.
func TestCurrentIncludesIndependentRuntimeVersions(t *testing.T) {
	got := Current()
	for name, versions := range map[string][2]string{
		"OpenCodeReview": {got.OpenCodeReviewVersion, OpenCodeReviewVersion},
		"Orca":           {got.OrcaVersion, OrcaVersion},
		"OpenHands":      {got.OpenHandsVersion, OpenHandsVersion},
		"Pi":             {got.PiVersion, PiVersion},
	} {
		if versions[0] != versions[1] {
			t.Errorf("Current().%sVersion = %q, want %q", name, versions[0], versions[1])
		}
	}
}

// The release refuses to publish an image whose sources moved under a version
// that says they did not, and that refusal used to arrive only after a tag was
// pushed — v0.223.0 failed on it and cost a second version to repair. The same
// check needs nothing but git and the catalog, so CI runs it on every push.
//
// This fails if that step is dropped, and if the checkout it depends on stops
// fetching the history it compares against.
func TestCIRefusesAStaleImageVersionBeforeATagExists(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	if !strings.Contains(workflow, "release-catalog-images.sh check-versions") {
		t.Error("CI does not check image versions, so a stale one is only found by a failing release")
	}
	if !strings.Contains(workflow, "fetch-depth: 0") {
		t.Error("CI checks out without history, so the version check has no release to compare against")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release-catalog-images.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "check-versions) check_versions ;;") {
		t.Error("the check CI runs is not a command this script answers to")
	}
}

// And the check itself, against the commit it exists because of.
//
// 9a35494 added a function to internal/dlp, which the runtime base image is
// built from, and left BASE_VERSION alone; the release refused it and v0.223.0
// was lost. b742060 is the repair. Wiring a check into CI is not the same as
// the check working, so this runs it on both.
func TestTheImageVersionCheckCatchesTheReleaseItWasWrittenFor(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, commit := range []string{"9a35494", "b742060"} {
		if exec.Command("git", "-C", root, "cat-file", "-e", commit+"^{commit}").Run() != nil {
			t.Skipf("%s is not in this checkout; the history to compare against is missing", commit)
		}
	}
	run := func(commit string) (string, error) {
		command := exec.Command("bash", "scripts/release-catalog-images.sh", "check-versions")
		command.Dir = root
		command.Env = append(os.Environ(), "GITHUB_SHA="+commit)
		out, err := command.CombinedOutput()
		return string(out), err
	}
	out, err := run("9a35494")
	if err == nil {
		t.Errorf("the check passed the commit that broke the release: %s", out)
	}
	if !strings.Contains(out, "BASE_VERSION") {
		t.Errorf("the refusal does not name the file to bump: %s", out)
	}
	if out, err := run("b742060"); err != nil {
		t.Errorf("the check refuses the commit that repaired it: %v\n%s", err, out)
	}
}
