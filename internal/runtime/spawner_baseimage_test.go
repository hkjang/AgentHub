package runtime

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/buildinfo"
)

// The base image is versioned independently of the control plane, so the default
// must follow BASE_VERSION. Deriving it from the release version would point a
// released control plane at an image tag that was never published.
func TestDefaultBaseImageFollowsTheBaseVersion(t *testing.T) {
	t.Setenv(EnvDefaultBaseImage, "")
	original := buildinfo.BaseVersion
	t.Cleanup(func() { buildinfo.BaseVersion = original })

	buildinfo.BaseVersion = "0.6.0"
	buildinfo.Version = "0.9.9"
	if got := DefaultBaseImage(); got != "agenthub-base:v0.6.0" {
		t.Fatalf("DefaultBaseImage() = %q, want the base version", got)
	}

	buildinfo.BaseVersion = "0.7.0-dev"
	if got := DefaultBaseImage(); got != "agenthub-base:v0.7.0" {
		t.Fatalf("a -dev suffix must be trimmed, got %q", got)
	}
}

func TestDefaultBaseImageHonoursTheOverride(t *testing.T) {
	t.Setenv(EnvDefaultBaseImage, " registry.corp/agenthub-base:pinned ")
	if got := DefaultBaseImage(); got != "registry.corp/agenthub-base:pinned" {
		t.Fatalf("DefaultBaseImage() = %q, want the override", got)
	}
}

// The shipped BASE_VERSION has to be a plain version, since it becomes an image
// tag in the manifests and in every offline install instruction.
func TestShippedBaseVersionIsUsableAsATag(t *testing.T) {
	raw, err := os.ReadFile("../../BASE_VERSION")
	if err != nil {
		t.Fatalf("BASE_VERSION is not readable: %v", err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, " \t/:") {
		t.Fatalf("BASE_VERSION %q is not usable as an image tag", value)
	}
}
