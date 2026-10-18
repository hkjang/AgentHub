package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseVersionIsConsistentAcrossDeploymentSurfaces(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	stable := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	body, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(body))
	if !stable.MatchString(version) {
		t.Fatalf("VERSION = %q, want a stable semantic version", version)
	}

	// The runtime base image carries its own number, and the two move apart on
	// purpose — DefaultBaseImage in internal/runtime/spawner.go explains why: the
	// base image is several GB and is only rebuilt when something it is built
	// from changes, so most control plane releases keep running on an older base
	// tag. That is exactly what makes the deployment artifacts below easy to
	// forget. A release commit hand-edits the VERSION strings and the base tag
	// stays where it was, which is right until the turn BASE_VERSION does move;
	// then the manifests ask the cluster for a tag nobody published and the
	// bridge Pod sits in ImagePullBackOff. The file read here is the released
	// number, so it carries no "-dev" suffix the way the buildinfo default does;
	// hold it to the same shape as VERSION so a suffix that leaks in is named
	// here rather than silently failing to match any manifest.
	body, err = os.ReadFile(filepath.Join(root, "BASE_VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	baseVersion := strings.TrimSpace(string(body))
	if !stable.MatchString(baseVersion) {
		t.Fatalf("BASE_VERSION = %q, want a stable semantic version", baseVersion)
	}

	// Only the two deployment paths below are checked against BASE_VERSION, and
	// the narrowness is the point: "every agenthub-base:v* in the repository is
	// current" is a false rule. Fixtures across internal/operator, internal/runtime,
	// internal/runtimespec and web/scripts pin invented tags (agenthub-base:v0.4.0
	// and friends) precisely because they must not follow the release, so a repo
	// wide scan would fail on files that are correct. These two are the installation
	// instructions an operator actually runs.
	checks := []struct {
		path string
		want []string
	}{
		{"compose.yaml", []string{"image: agenthub:v" + version, "VERSION: " + version}},
		{"README.md", []string{"minikube image load agenthub:v" + version, "minikube image load agenthub-base:v" + baseVersion}},
		{"deploy/kubernetes/agenthub.yaml", []string{"image: agenthub:v" + version, `value: "agenthub:v` + version + `"`}},
		{"deploy/kubernetes/operator.yaml", []string{"image: agenthub:v" + version}},
		{"deploy/kubernetes/worker.yaml", []string{"image: agenthub:v" + version}},
		{"deploy/kubernetes/ollama-bridge.yaml", []string{"image: agenthub-base:v" + baseVersion}},
		{"deploy/offline/.env.example", []string{"AGENTHUB_VERSION=v" + version}},
		{"docs/index.html", []string{`"softwareVersion": "v` + version + `"`}},
	}
	for _, check := range checks {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(check.path)))
		if err != nil {
			t.Errorf("read %s: %v", check.path, err)
			continue
		}
		for _, want := range check.want {
			if !strings.Contains(string(content), want) {
				t.Errorf("%s does not reference release %s through %q", check.path, version, want)
			}
		}
	}
}
