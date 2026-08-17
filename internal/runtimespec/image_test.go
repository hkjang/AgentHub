package runtimespec

import (
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func TestRuntimeImageReferencePrefersDigest(t *testing.T) {
	tests := map[string]struct {
		item store.RuntimeImage
		want string
	}{
		"digest pins the exact build": {
			item: store.RuntimeImage{Image: "registry.corp/agenthub-base", Version: "v0.4.0", Digest: "sha256:abc"},
			want: "registry.corp/agenthub-base@sha256:abc",
		},
		"version becomes the tag when no digest is recorded": {
			item: store.RuntimeImage{Image: "registry.corp/agenthub-base", Version: "v0.4.0"},
			want: "registry.corp/agenthub-base:v0.4.0",
		},
		"an explicit tag is left alone": {
			item: store.RuntimeImage{Image: "registry.corp/agenthub-base:custom", Version: "v0.4.0"},
			want: "registry.corp/agenthub-base:custom",
		},
		"a port in the registry host is not mistaken for a tag": {
			item: store.RuntimeImage{Image: "registry.corp:5000/agenthub-base", Version: "v0.4.0"},
			want: "registry.corp:5000/agenthub-base:v0.4.0",
		},
		"the documentation placeholder is rejected": {
			item: store.RuntimeImage{Image: "registry.local/agent/opencode", Version: "v1"},
			want: "",
		},
		"an empty catalog entry is rejected": {item: store.RuntimeImage{}, want: ""},
	}
	for name, test := range tests {
		if got := runtimeImageReference(test.item); got != test.want {
			t.Errorf("%s: runtimeImageReference() = %q, want %q", name, got, test.want)
		}
	}
}
