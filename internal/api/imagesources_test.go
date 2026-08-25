package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestReleaseAssetLookupRecognizesWholeAndSplitArchives(t *testing.T) {
	root := filepath.Join("..", "..")
	script := filepath.Join(root, "scripts", "release-asset-exists.sh")
	archive := "agenthub-openhands-v1.43.1.tar.gz"
	for name, test := range map[string]struct {
		assets string
		found  bool
	}{
		"whole archive":  {assets: archive + "\n", found: true},
		"split archive":  {assets: archive + ".part-aa\n" + archive + ".part-ab\n", found: true},
		"other version":  {assets: "agenthub-openhands-v1.43.0.tar.gz\n"},
		"similar prefix": {assets: archive + ".backup\n"},
		"empty index":    {},
	} {
		t.Run(name, func(t *testing.T) {
			index := filepath.Join(t.TempDir(), "assets.txt")
			if err := os.WriteFile(index, []byte(test.assets), 0o600); err != nil {
				t.Fatal(err)
			}
			err := exec.Command("bash", script, archive, index).Run()
			if found := err == nil; found != test.found {
				t.Fatalf("release asset lookup found=%v, want %v (error: %v)", found, test.found, err)
			}
		})
	}
}

func TestReleaseDecisionsUsePublishedAssetEvidence(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, required := range []string{
		`gh api --paginate "repos/${GITHUB_REPOSITORY}/releases?per_page=100"`,
		`select(.draft == false and .prerelease == false)`,
		`archive="agenthub-base-v${BASE_VERSION}.tar.gz"`,
		`archive="agenthub-${name}-v${version}.tar.gz"`,
		`OPENHANDS_VERSION: ${{ steps.runtimes.outputs.openhands_version }}`,
		`--build-arg OPENHANDS_VERSION="${OPENHANDS_VERSION}"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow does not use published asset evidence %q", required)
		}
	}
	const lookup = `bash scripts/release-asset-exists.sh "${archive}" "${RUNNER_TEMP}/agenthub-release-assets.txt"`
	if count := strings.Count(workflow, lookup); count != 2 {
		t.Errorf("release asset lookup is wired into %d image decisions, want base and independent runtimes", count)
	}
}

// Every image's package command must be guarded by that image's own decision.
// OpenHands was once nested under ORCA_CHANGED: the runner built it and then
// uploaded nothing, while later releases assumed the missing archive existed.
func TestEveryRuntimeArchiveUsesItsOwnChangeGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	entries := regexp.MustCompile(`"([a-z0-9]+):([A-Z_]+_VERSION):[^"]+"`).FindAllStringSubmatch(workflow, -1)
	for _, entry := range entries {
		name := entry[1]
		prefix := strings.ToUpper(name)
		guard := regexp.QuoteMeta(`if [ "${` + prefix + `_CHANGED}" = "true" ]; then`)
		pack := regexp.QuoteMeta(`package "agenthub-` + name + `:${` + prefix + `_TAG}"`)
		if !regexp.MustCompile(guard + `\s+` + pack).MatchString(workflow) {
			t.Errorf("%s archive is not immediately guarded by %s_CHANGED", name, prefix)
		}
	}
}

func TestEveryReleaseRuntimeCanBePackagedLocally(t *testing.T) {
	root := filepath.Join("..", "..")
	workflowBody, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	makefileBody, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	entries := regexp.MustCompile(`"([a-z0-9]+):([A-Z_]+_VERSION):[^"]+"`).FindAllStringSubmatch(string(workflowBody), -1)
	makefile := string(makefileBody)
	if !strings.Contains(makefile, `--build-arg OPENHANDS_VERSION=$(OPENHANDS_VERSION)`) {
		t.Error("make image-openhands does not pin the packages to OPENHANDS_VERSION")
	}
	for _, entry := range entries {
		name, versionFile := entry[1], entry[2]
		for _, required := range []string{
			versionFile + ` ?= $(shell cat ` + versionFile + `)`,
			"image-" + name + ":",
			"release-archives:",
			`$(call package_image,agenthub-` + name + `:`,
		} {
			if !strings.Contains(makefile, required) {
				t.Errorf("%s cannot be packaged by make release-archives; missing %q", name, required)
			}
		}
		dependency := regexp.MustCompile(`(?m)^release-archives:.*\bimage-` + regexp.QuoteMeta(name) + `\b`)
		if !dependency.MatchString(makefile) {
			t.Errorf("make release-archives does not build image-%s", name)
		}
	}
}

// A runtime image is only rebuilt when one of the files the release names as its
// sources has changed. A file the Dockerfile copies and that list does not
// mention is a change that ships nothing: the release reuses the previous image,
// says so in its log, and nobody looks again.
//
// That is not hypothetical. orca-agents-configure.sh — the script that points
// codex at this deployment's gateway rather than at a vendor — was copied by the
// Dockerfile and missing from the list. An image without it stalls every worker,
// which is exactly what a stale image did on the cluster while this was found.
func TestEveryImageSourceIsNamedInTheRelease(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Skipf("the release workflow is not present in this checkout: %v", err)
	}
	entries := regexp.MustCompile(`"([a-z0-9]+):([A-Z_]+_VERSION):([^"]+)"`).FindAllStringSubmatch(string(workflow), -1)
	if len(entries) < 5 {
		t.Fatalf("found only %d runtime images in the release; the pattern this test reads by has probably changed", len(entries))
	}
	copied := regexp.MustCompile(`COPY (deploy/runtime/[A-Za-z0-9._-]+)`)
	for _, entry := range entries {
		name, sources := entry[1], entry[3]
		body, err := os.ReadFile(filepath.Join(root, "Dockerfile."+name))
		if err != nil {
			t.Errorf("%s is released and has no Dockerfile.%s", name, name)
			continue
		}
		listed := map[string]bool{}
		for _, source := range strings.Fields(sources) {
			listed[source] = true
		}
		var missing []string
		for _, match := range copied.FindAllStringSubmatch(string(body), -1) {
			if !listed[match[1]] {
				missing = append(missing, match[1])
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: the image copies %v, and the release does not watch %v — changing one of them would ship the old image",
				name, "these files", missing)
		}
	}
}
