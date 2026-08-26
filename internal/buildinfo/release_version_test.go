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
	body, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(body))
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(version) {
		t.Fatalf("VERSION = %q, want a stable semantic version", version)
	}

	checks := []struct {
		path string
		want []string
	}{
		{"compose.yaml", []string{"image: agenthub:v" + version, "VERSION: " + version}},
		{"README.md", []string{"minikube image load agenthub:v" + version}},
		{"deploy/kubernetes/agenthub.yaml", []string{"image: agenthub:v" + version, `value: "agenthub:v` + version + `"`}},
		{"deploy/kubernetes/operator.yaml", []string{"image: agenthub:v" + version}},
		{"deploy/kubernetes/worker.yaml", []string{"image: agenthub:v" + version}},
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
