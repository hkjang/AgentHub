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

func TestRuntimeProxyLocalDependenciesFollowTheDockerBuild(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "runtime-proxy-local-go-deps.sh")
	command := exec.Command("bash", script)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve runtime-proxy dependencies: %v\n%s", err, output)
	}
	dependencies := map[string]bool{}
	for _, dependency := range strings.Fields(string(output)) {
		dependencies[filepath.ToSlash(dependency)] = true
		if filepath.IsAbs(dependency) || strings.HasPrefix(dependency, "../") {
			t.Errorf("dependency %q is not relative to the Docker build context", dependency)
		}
	}
	for _, expected := range []string{"cmd/runtime-proxy", "internal/dlp", "internal/policy"} {
		if !dependencies[expected] {
			t.Errorf("runtime-proxy dependency list does not include %s; got %q", expected, output)
		}
	}
	if dependencies["internal/api"] {
		t.Error("runtime-proxy dependency list includes unrelated control-plane package internal/api")
	}
}

func TestRuntimeProxyDependencyLookupUsesLinuxWithoutCgoAndFailsClosed(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "runtime-proxy-local-go-deps.sh")
	fakeBin := t.TempDir()
	fakeGo := filepath.Join(fakeBin, "go")
	good := `#!/usr/bin/env bash
set -euo pipefail
test "${CGO_ENABLED:-}" = 0
test "${GOOS:-}" = linux
test "${GOARCH:-}" = amd64
test "${GOWORK:-}" = off
case " $* " in *" -mod=readonly "*) ;; *) exit 1 ;; esac
printf '%s\n' \
  'github.com/hkjang/AgentHub github.com/hkjang/AgentHub/cmd/runtime-proxy' \
  'github.com/hkjang/AgentHub github.com/hkjang/AgentHub/internal/dlp'
`
	if err := os.WriteFile(fakeGo, []byte(good), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script)
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency helper did not use Docker's Go environment: %v\n%s", err, output)
	}
	if got := strings.Fields(string(output)); strings.Join(got, " ") != "cmd/runtime-proxy internal/dlp" {
		t.Fatalf("dependency helper output = %q", output)
	}

	failing := "#!/usr/bin/env bash\nexit 23\n"
	if err := os.WriteFile(fakeGo, []byte(failing), 0o700); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("bash", script)
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("dependency helper ignored go list failure; output %q", output)
	}
	if !strings.Contains(string(output), "failed to resolve runtime-proxy dependencies") {
		t.Fatalf("dependency helper failure has no useful diagnostic: %q", output)
	}
}

func TestReleaseWatchesRuntimeProxyLocalDependenciesFailClosed(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, required := range []string{
		`uses: actions/setup-go@v7`,
		`go-version-file: go.mod`,
		`if ! local_go_source_lines="$(bash scripts/runtime-proxy-local-go-deps.sh)"; then`,
		`sources+=("${local_go_sources[@]}")`,
		`GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go list -mod=readonly -deps`,
		`echo "::error::could not resolve runtime-proxy's module graph"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow does not fail-closed over base Go dependencies; missing %q", required)
		}
	}
}

// The control plane chooses these runtime images from buildinfo. Keeping the
// wrapper version only in the runtime image build is not enough: when the
// wrapper is bumped, a control image built without its ARG/ldflag keeps asking
// Kubernetes for the previous tag.
func TestIndependentRuntimeVersionsReachTheControlPlaneBuild(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	dockerfile := read("Dockerfile")
	makefile := read("Makefile")
	compose := read("compose.yaml")
	workflow := read(filepath.Join(".github", "workflows", "release.yaml"))

	for _, runtime := range []struct {
		variable string
		field    string
		output   string
	}{
		{"OPENCODEREVIEW_VERSION", "OpenCodeReviewVersion", "opencodereview_version"},
		{"ORCA_VERSION", "OrcaVersion", "orca_version"},
		{"OPENHANDS_VERSION", "OpenHandsVersion", "openhands_version"},
		{"PI_VERSION", "PiVersion", "pi_version"},
	} {
		if !strings.Contains(dockerfile, "ARG "+runtime.variable+"=") {
			t.Errorf("Dockerfile does not declare %s", runtime.variable)
		}
		ldflag := "buildinfo." + runtime.field + "=${" + runtime.variable + "}"
		if count := strings.Count(dockerfile, ldflag); count != 3 {
			t.Errorf("Dockerfile injects %s into %d control binaries, want 3", runtime.variable, count)
		}
		makeArg := "--build-arg " + runtime.variable + "=$(" + runtime.variable + ")"
		if !strings.Contains(makefile, makeArg) {
			t.Errorf("make image does not pass %s", runtime.variable)
		}
		version := strings.TrimSpace(read(runtime.variable))
		if !strings.Contains(compose, runtime.variable+": "+version) {
			t.Errorf("compose build args do not pin %s to its version file", runtime.variable)
		}
		workflowEnv := runtime.variable + ": ${{ steps.runtimes.outputs." + runtime.output + " }}"
		if !strings.Contains(workflow, workflowEnv) {
			t.Errorf("release control build has no %s environment value", runtime.variable)
		}
		workflowArg := `--build-arg ` + runtime.variable + `="${` + runtime.variable + `}"`
		if !strings.Contains(workflow, workflowArg) {
			t.Errorf("release control build does not pass %s", runtime.variable)
		}
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
