package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

// A version bump is not one file, and until now this check only knew about one.
//
// 6059496 added a log line to cmd/runtime-proxy, which the runtime base image is
// built from, and bumped BASE_VERSION for it — precisely what the check above
// asks for. It said nothing about compose.yaml still pinning BASE_VERSION to the
// old number, or about this package still defaulting BaseVersion to it, so the
// author re-read "every image whose inputs changed already has a new version"
// and the release failed twice afterwards on the Go suite instead. The number
// has to reach every place that spends it, and the gate that demands the bump is
// the one place the author is already looking.
//
// The commits are built here rather than named: the one that broke the release
// was never pushed, and a guard that skips whenever the history it needs is
// missing is a guard that stops guarding. commit-tree writes an unreferenced
// commit from a throwaway index, so no branch, ref or file in the checkout moves.
func TestTheImageVersionCheckRefusesABumpThatOnlyReachedTheVersionFile(t *testing.T) {
	root := filepath.Join("..", "..")
	git := func(env []string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), env...)
		out, err := command.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out))
	}
	blob := func(content string) string {
		t.Helper()
		command := exec.Command("git", "-C", root, "hash-object", "-w", "--stdin")
		command.Stdin = strings.NewReader(content)
		out, err := command.Output()
		if err != nil {
			t.Fatalf("write a blob: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	commitWith := func(files map[string]string) string {
		t.Helper()
		env := []string{
			"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index"),
			"GIT_AUTHOR_NAME=version check", "GIT_AUTHOR_EMAIL=version@check",
			"GIT_COMMITTER_NAME=version check", "GIT_COMMITTER_EMAIL=version@check",
		}
		git(env, "read-tree", "HEAD")
		for path, content := range files {
			git(env, "update-index", "--add", "--cacheinfo", "100644,"+blob(content)+","+path)
		}
		return git(env, "commit-tree", git(env, "write-tree"), "-p", "HEAD", "-m", "a synthetic bump")
	}
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	current := strings.TrimSpace(read("BASE_VERSION"))
	parts := strings.Split(current, ".")
	patch, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("BASE_VERSION %q does not end in a number", current)
	}
	parts[len(parts)-1] = strconv.Itoa(patch + 1)
	next := strings.Join(parts, ".")

	compose := strings.Replace(read("compose.yaml"), "BASE_VERSION: "+current, "BASE_VERSION: "+next, 1)
	if strings.Contains(compose, "BASE_VERSION: "+current) || !strings.Contains(compose, "BASE_VERSION: "+next) {
		t.Fatalf("compose.yaml does not pin BASE_VERSION to %s the way this test expects", current)
	}
	defaults := strings.Replace(read(filepath.Join("internal", "buildinfo", "buildinfo.go")),
		`BaseVersion = "`+current+`-dev"`, `BaseVersion = "`+next+`-dev"`, 1)
	if !strings.Contains(defaults, `BaseVersion = "`+next+`-dev"`) {
		t.Fatalf("buildinfo does not default BaseVersion to %s-dev the way this test expects", current)
	}

	run := func(files map[string]string) (string, error) {
		command := exec.Command("bash", "scripts/release-catalog-images.sh", "check-versions")
		command.Dir = root
		command.Env = append(os.Environ(), "GITHUB_SHA="+commitWith(files))
		out, err := command.CombinedOutput()
		return string(out), err
	}

	out, err := run(map[string]string{"BASE_VERSION": next + "\n"})
	if err == nil {
		t.Errorf("a bump that reached only BASE_VERSION was accepted: %s", out)
	}
	for _, named := range []string{"BASE_VERSION", "compose.yaml", "buildinfo"} {
		if !strings.Contains(out, named) {
			t.Errorf("the refusal does not name %s, which still carries the old version: %s", named, out)
		}
	}

	// Each mirror on its own, so that checking one and reporting both cannot pass
	// for checking the two of them.
	out, err = run(map[string]string{"BASE_VERSION": next + "\n", "compose.yaml": compose})
	if err == nil {
		t.Errorf("a bump that skipped the buildinfo default was accepted: %s", out)
	}
	if strings.Contains(out, "compose.yaml") || !strings.Contains(out, "buildinfo") {
		t.Errorf("the refusal does not name the buildinfo default alone: %s", out)
	}
	out, err = run(map[string]string{
		"BASE_VERSION":                    next + "\n",
		"internal/buildinfo/buildinfo.go": defaults,
	})
	if err == nil {
		t.Errorf("a bump that skipped the compose build arg was accepted: %s", out)
	}
	if !strings.Contains(out, "compose.yaml") || strings.Contains(out, "buildinfo") {
		t.Errorf("the refusal does not name the compose build arg alone: %s", out)
	}

	out, err = run(map[string]string{
		"BASE_VERSION":                    next + "\n",
		"compose.yaml":                    compose,
		"internal/buildinfo/buildinfo.go": defaults,
	})
	if err != nil {
		t.Errorf("a bump that reached all three files was refused: %v\n%s", err, out)
	}
}
