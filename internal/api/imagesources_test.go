package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/imagecatalog"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"gopkg.in/yaml.v3"
)

func repositoryCatalog(t *testing.T) (string, *imagecatalog.Catalog) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := imagecatalog.LoadRepository(root, runtimetype.Supported)
	if err != nil {
		t.Fatal(err)
	}
	return root, catalog
}

func readRepositoryFile(t *testing.T, root, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestReleasePlanIncludesEveryCatalogImageInDependencyOrder(t *testing.T) {
	root, catalog := repositoryCatalog(t)
	index := filepath.Join(t.TempDir(), "releases.json")
	if err := os.WriteFile(index, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	command := exec.Command("bash", filepath.Join(root, "scripts", "release-catalog-images.sh"), "plan")
	command.Dir = root
	command.Env = append(os.Environ(),
		"AGENTHUB_IMAGE_CATALOG="+filepath.Join(root, "runtime-images.json"),
		"AGENTHUB_RELEASE_INDEX="+index,
		"AGENTHUB_IMAGE_PLAN="+planPath,
		"AGENTHUB_PREVIOUS_TAG=",
		"GITHUB_REF_NAME=v99.0.0",
		"GITHUB_SHA=HEAD",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create catalog release plan: %v\n%s", err, output)
	}
	var plan struct {
		SchemaVersion int `json:"schemaVersion"`
		Images        []struct {
			ID                 string   `json:"id"`
			Publish            bool     `json:"publish"`
			Build              bool     `json:"build"`
			BuildDependencies  []string `json:"buildDependencies"`
			BundleDependencies []string `json:"bundleDependencies"`
		} `json:"images"`
	}
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("release plan is invalid JSON: %v\n%s", err, body)
	}
	if plan.SchemaVersion != 1 || len(plan.Images) != len(catalog.Images) {
		t.Fatalf("plan schema/images = %d/%d, want 1/%d", plan.SchemaVersion, len(plan.Images), len(catalog.Images))
	}
	position := map[string]int{}
	for index, image := range plan.Images {
		position[image.ID] = index
		if !image.Publish || !image.Build {
			t.Errorf("first release plan for %s has publish=%v build=%v, want both true", image.ID, image.Publish, image.Build)
		}
	}
	for _, image := range catalog.Images {
		imagePosition, exists := position[image.ID]
		if !exists {
			t.Errorf("catalog image %s is absent from release plan", image.ID)
			continue
		}
		for _, dependency := range image.BuildDependencies {
			if position[dependency] >= imagePosition {
				t.Errorf("dependency %s is planned after %s", dependency, image.ID)
			}
		}
		planned := plan.Images[imagePosition]
		if strings.Join(planned.BuildDependencies, ",") != strings.Join(image.BuildDependencies, ",") ||
			strings.Join(planned.BundleDependencies, ",") != strings.Join(image.BundleDependencies, ",") {
			t.Errorf("plan dependency metadata for %s drifted from catalog", image.ID)
		}
	}
}

func TestPublishedArchiveEvidenceIsCompleteChecksummedAndNotFromTheFuture(t *testing.T) {
	root, _ := repositoryCatalog(t)
	hexDigest := strings.Repeat("a", 64)
	asset := func(name string) map[string]any {
		return map[string]any{
			"name": name, "size": 123, "digest": "sha256:" + hexDigest,
			"browser_download_url": "https://github.example/" + name,
		}
	}
	release := func(tag, published string, assets ...map[string]any) map[string]any {
		return map[string]any{"tag_name": tag, "draft": false, "prerelease": false, "published_at": published, "assets": assets}
	}
	archive := "agenthub-openhands-v1.43.1.tar.gz"
	run := func(t *testing.T, current string, releases []map[string]any) (string, bool) {
		t.Helper()
		body, err := json.Marshal(releases)
		if err != nil {
			t.Fatal(err)
		}
		index := filepath.Join(t.TempDir(), "index.json")
		if err := os.WriteFile(index, body, 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("bash", filepath.Join(root, "scripts", "release-catalog-images.sh"), "archive-source", archive)
		command.Dir = root
		command.Env = append(os.Environ(), "AGENTHUB_RELEASE_INDEX="+index, "GITHUB_REF_NAME="+current)
		output, err := command.CombinedOutput()
		return strings.TrimSpace(string(output)), err == nil
	}

	t.Run("newest stable version wins, not publication timestamp", func(t *testing.T) {
		releases := []map[string]any{
			release("v1.8.0", "2030-01-01T00:00:00Z", asset(archive)),
			release("v1.9.0", "2029-01-01T00:00:00Z", asset(archive)),
			release("v2.1.0", "2028-01-01T00:00:00Z", asset(archive)),
		}
		if got, ok := run(t, "v2.0.0", releases); !ok || got != "v1.9.0" {
			t.Fatalf("archive source = %q, ok=%v, want v1.9.0", got, ok)
		}
	})

	t.Run("contiguous split archive", func(t *testing.T) {
		releases := []map[string]any{release("v1.7.0", "2029-01-01T00:00:00Z",
			asset(archive+".part-aa"), asset(archive+".part-ab"))}
		if got, ok := run(t, "v2.0.0", releases); !ok || got != "v1.7.0" {
			t.Fatalf("archive source = %q, ok=%v, want v1.7.0", got, ok)
		}
	})

	for name, releases := range map[string][]map[string]any{
		"missing digest": {
			release("v1.0.0", "2029-01-01T00:00:00Z", map[string]any{
				"name": archive, "size": 123, "browser_download_url": "https://github.example/archive",
			}),
		},
		"zero size": {
			release("v1.0.0", "2029-01-01T00:00:00Z", map[string]any{
				"name": archive, "size": 0, "digest": "sha256:" + hexDigest,
				"browser_download_url": "https://github.example/archive",
			}),
		},
		"split gap": {
			release("v1.0.0", "2029-01-01T00:00:00Z", asset(archive+".part-aa"), asset(archive+".part-ac")),
		},
		"parts from different releases": {
			release("v1.1.0", "2029-01-01T00:00:00Z", asset(archive+".part-aa")),
			release("v1.0.0", "2028-01-01T00:00:00Z", asset(archive+".part-ab")),
		},
		"single and split conflict": {
			release("v1.0.0", "2029-01-01T00:00:00Z", asset(archive), asset(archive+".part-aa")),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := run(t, "v2.0.0", releases); ok {
				t.Fatalf("invalid published evidence was accepted from %q", got)
			}
		})
	}
}

func TestPreviousReleaseIsHighestStableTagBelowCurrent(t *testing.T) {
	root, _ := repositoryCatalog(t)
	command := exec.Command("bash", filepath.Join(root, "scripts", "release-catalog-images.sh"), "previous-tag")
	command.Dir = root
	command.Env = append(os.Environ(), "GITHUB_REF_NAME=v2.0.0")
	command.Stdin = strings.NewReader(strings.Join([]string{
		"v1.9.0",
		"v999.0.0",
		"v2.0.1",
		"v1.10.0",
		"v2.0.0",
		"v1.11.0-rc.1",
		"v01.99.0",
	}, "\n") + "\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("select previous release: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "v1.10.0" {
		t.Fatalf("previous release = %q, want v1.10.0", got)
	}
}

func TestReleaseWorkflowIsCatalogDrivenAndSupplyChainChecked(t *testing.T) {
	root, catalog := repositoryCatalog(t)
	workflow := readRepositoryFile(t, root, filepath.Join(".github", "workflows", "release.yaml"))
	helper := readRepositoryFile(t, root, filepath.Join("scripts", "release-catalog-images.sh"))
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatalf("release workflow is invalid YAML: %v", err)
	}

	for _, required := range []string{
		"runtime-images.json",
		"release-catalog-images.sh plan",
		"release-catalog-images.sh build",
		"release-catalog-images.sh package",
		"release-catalog-images.sh verify",
		"release-catalog-images.sh notes",
		"agenthub-release-index.json",
		"tag_name",
		"browser_download_url",
		"digest",
		`docker run --rm "agenthub:${release_tag}" version --json`,
		`git show -s --format=%ct "$commit"`,
		"gzip -n -9",
		`${RELEASE_CHUNK:-1900M}`,
		".sourcePaths[]",
		".controlBuildArg",
		".buildArgs | to_entries[]",
		".buildDependencies[]",
		".bundleDependencies",
		".health.kind",
	} {
		combined := workflow + "\n" + helper
		if !strings.Contains(combined, required) {
			t.Errorf("catalog release wiring is missing %q", required)
		}
	}
	for _, image := range catalog.Images {
		for _, forbidden := range []string{
			"steps.runtimes.outputs." + image.ID,
			"Dockerfile." + image.ID + " -t",
			image.ID + "_changed",
		} {
			if strings.Contains(workflow, forbidden) {
				t.Errorf("workflow hard-codes catalog image %s through %q", image.ID, forbidden)
			}
		}
	}

	for _, required := range []string{
		"id-token: write",
		"attestations: write",
		"artifact-metadata: write",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		"actions/setup-node@820762786026740c76f36085b0efc47a31fe5020",
		"docker/setup-buildx-action@37fe631027851001ddb9b187196cc803df7f5f0e",
		"anchore/sbom-action/download-syft@e22c389904149dbc22b58101806040fa8d37a610",
		"syft-version: v1.51.0",
		`.images[] | select(.publish) | .image + ":" + .tag`,
		"actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6",
		"subject-checksums: release/SHA256SUMS",
		"softprops/action-gh-release@3d0d9888cb7fd7b750713d6e236d1fcb99157228",
		"go test -race ./cmd/... ./internal/...",
		"kubectl kustomize deploy/kubernetes",
		"env -u AGENTHUB_POSTGRES_DSN",
		"docker compose -f deploy/offline/compose.yaml config --format json",
		`(.services | keys | sort) == ["agenthub", "agenthub-worker"]`,
		`.image == ("agenthub:" + env.GITHUB_REF_NAME)`,
		`contains("postgres") | not`,
		"agenthub-offline-linux-amd64 manifest",
		"--release-index \"$AGENTHUB_RELEASE_INDEX\"",
		"--output release/offline-bundle.json",
		"-iname '*postgres*'",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("hardened release workflow is missing %q", required)
		}
	}
	for _, required := range []string{
		"PostgreSQL 17은 Docker 이미지/오프라인 번들에 **포함되지 않습니다**",
		"runtime-images.json` 카탈로그",
		"base/n8n/orca 핀을 갱신했습니다",
		"version --json",
	} {
		if !strings.Contains(helper, required) {
			t.Errorf("catalog release notes are missing %q", required)
		}
	}
}

func TestRuntimeProxyLocalDependenciesFollowTheDockerBuild(t *testing.T) {
	root, _ := repositoryCatalog(t)
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
	root, _ := repositoryCatalog(t)
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

	if err := os.WriteFile(fakeGo, []byte("#!/usr/bin/env bash\nexit 23\n"), 0o700); err != nil {
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

func TestAllCatalogVersionsReachControlAndLocalBuildPaths(t *testing.T) {
	root, catalog := repositoryCatalog(t)
	dockerfile := readRepositoryFile(t, root, "Dockerfile")
	makefile := readRepositoryFile(t, root, "Makefile")
	compose := readRepositoryFile(t, root, "compose.yaml")
	helper := readRepositoryFile(t, root, filepath.Join("scripts", "release-catalog-images.sh"))
	if !strings.Contains(helper, `arg="$(jq -r '.controlBuildArg'`) || !strings.Contains(helper, `args+=(--build-arg "${arg}=${version}")`) {
		t.Fatal("release control build does not derive version build args from the catalog")
	}
	for _, required := range []string{
		`.images[] | .controlBuildArg`,
		`.versionFile`,
		`build_command+=(--build-arg "$$build_arg=$$image_version")`,
	} {
		if !strings.Contains(makefile, required) {
			t.Errorf("catalog-driven make control build is missing %q", required)
		}
	}
	for _, image := range catalog.Images {
		if !strings.Contains(dockerfile, "ARG "+image.ControlBuildArg+"=") {
			t.Errorf("control Dockerfile does not declare %s", image.ControlBuildArg)
		}
		ldflag := "buildinfo." + image.BuildInfoSymbol + "=${" + image.ControlBuildArg + "}"
		if count := strings.Count(dockerfile, ldflag); count != 3 {
			t.Errorf("control Dockerfile injects %s into %d binaries, want 3", image.ControlBuildArg, count)
		}
		version := strings.TrimSpace(readRepositoryFile(t, root, image.VersionFile))
		if !strings.Contains(compose, image.ControlBuildArg+": "+version) {
			t.Errorf("compose build args do not pin %s to %s", image.ControlBuildArg, version)
		}
	}
}

func TestEveryCatalogRuntimeCanStillBePackagedLocally(t *testing.T) {
	root, catalog := repositoryCatalog(t)
	makefile := readRepositoryFile(t, root, "Makefile")
	for _, required := range []string{
		"image-%:",
		"image-runtimes:",
		"release-archives: image image-runtimes",
		`.images[] | .image, "\u0000", .versionFile`,
		`package_image "$$image_name:v$$image_version"`,
		"gzip -n -9",
	} {
		if !strings.Contains(makefile, required) {
			t.Errorf("catalog-driven local packaging is missing %q", required)
		}
	}
	command := exec.Command("make", "-s", "catalog-image-ids")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list catalog make targets: %v\n%s", err, output)
	}
	got := strings.Fields(string(output))
	want := make([]string, 0, len(catalog.Images))
	for _, image := range catalog.Images {
		want = append(want, image.ID)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("make catalog ids = %v, want %v", got, want)
	}
}
