package runtime

import (
	"os"
	"path/filepath"
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

// Every runtime with an image of its own has to be named in DefaultRuntimeImage,
// or an agent of that type goes to agenthub-base and looks for a binary that
// image never contained.
//
// The list comes from the repository rather than from a constant here, because a
// constant here is a second list to keep in step. Adding Dockerfile.<name>
// without a case in DefaultRuntimeImage fails this test — which is how Goose,
// HolmesGPT and BrowserCode each shipped unable to start from the catalog.
func TestEveryRuntimeImageHasADefault(t *testing.T) {
	root := repositoryRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the repository: %v", err)
	}
	found := 0
	for _, entry := range entries {
		name, ok := strings.CutPrefix(entry.Name(), "Dockerfile.")
		if !ok || name == "base" {
			// Dockerfile.base is the shared image every other runtime falls back to.
			continue
		}
		if !runtimetype.IsSupported(name) {
			t.Errorf("Dockerfile.%s builds an image for a runtime type nothing supports", name)
			continue
		}
		found++
		image := DefaultRuntimeImage(name)
		if strings.HasPrefix(image, "agenthub-base:") {
			t.Errorf("%s has its own image (Dockerfile.%s) but starts from %s, where its binaries do not exist",
				name, name, image)
			continue
		}
		if !strings.HasPrefix(image, "agenthub-"+name+":v") {
			t.Errorf("%s starts from %q, want the image its Dockerfile builds", name, image)
		}
	}
	// A rename that emptied the loop would otherwise pass silently.
	if found < 5 {
		t.Fatalf("only %d runtime images were found; the naming convention has changed", found)
	}
}

// repositoryRoot walks up from this package to the directory holding go.mod.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for depth := 0; depth < 8; depth++ {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		directory = filepath.Dir(directory)
	}
	t.Fatal("could not find the repository root")
	return ""
}
