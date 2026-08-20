package runtime

import (
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/buildinfo"
	"github.com/hkjang/AgentHub/internal/runtimetype"
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

// Langflow does not boot from the shared base image, so the default has to
// follow LANGFLOW_VERSION. Sending a Langflow agent to agenthub-base would start
// a Pod whose command does not exist in it.
func TestDefaultRuntimeImageIsPerRuntimeType(t *testing.T) {
	t.Setenv(EnvDefaultBaseImage, "")
	t.Setenv(EnvDefaultLangflowImage, "")
	original, originalLangflow := buildinfo.BaseVersion, buildinfo.LangflowVersion
	t.Cleanup(func() { buildinfo.BaseVersion, buildinfo.LangflowVersion = original, originalLangflow })

	buildinfo.BaseVersion = "0.10.0"
	buildinfo.LangflowVersion = "0.1.0"
	if got := DefaultRuntimeImage(runtimetype.OpenCode); got != "agenthub-base:v0.10.0" {
		t.Errorf("opencode default image = %q", got)
	}
	if got := DefaultRuntimeImage(runtimetype.Langflow); got != "agenthub-langflow:v0.1.0" {
		t.Errorf("langflow default image = %q", got)
	}
	t.Setenv(EnvDefaultLangflowImage, " registry.corp/agenthub-langflow:pinned ")
	if got := DefaultRuntimeImage(runtimetype.Langflow); got != "registry.corp/agenthub-langflow:pinned" {
		t.Errorf("langflow override = %q", got)
	}
	// The Langflow override must not leak into the runtimes that do boot from
	// the shared image.
	if got := DefaultRuntimeImage(runtimetype.Hermes); got != "agenthub-base:v0.10.0" {
		t.Errorf("hermes default image = %q", got)
	}
}

func TestDefaultBaseImageHonoursTheOverride(t *testing.T) {
	t.Setenv(EnvDefaultBaseImage, " registry.corp/agenthub-base:pinned ")
	if got := DefaultBaseImage(); got != "registry.corp/agenthub-base:pinned" {
		t.Fatalf("DefaultBaseImage() = %q, want the override", got)
	}
}

// The shipped versions have to be plain versions, since each becomes an image
// tag in the manifests and in every offline install instruction.
func TestShippedImageVersionsAreUsableAsTags(t *testing.T) {
	for _, name := range []string{"BASE_VERSION", "LANGFLOW_VERSION"} {
		raw, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatalf("%s is not readable: %v", name, err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || strings.ContainsAny(value, " \t/:") {
			t.Fatalf("%s %q is not usable as an image tag", name, value)
		}
	}
}
