package store

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Pinning is what keeps a definition reproducible, and it only works if the pin
// is an image that runtime can actually boot. Every runtime used to share one
// base image, so a mismatched pin was harmless; with Langflow and Qwen Code
// carrying images of their own it starts a Pod whose command does not exist,
// which surfaces as a crash loop with nothing explaining it.
//
// The message has to name both sides, because the person reading it is looking
// at a form with two dropdowns and needs to know which one to change.
func TestRuntimeImagePinMismatchIsExplained(t *testing.T) {
	err := runtimeImagePinMismatch(RuntimeImage{RuntimeType: runtimetype.QwenCode}, runtimetype.Langflow)
	if err == nil {
		t.Fatal("a mismatched pin must be refused")
	}
	for _, want := range []string{runtimetype.QwenCode, runtimetype.Langflow} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if runtimeImagePinMismatch(RuntimeImage{RuntimeType: runtimetype.Langflow}, runtimetype.Langflow) != nil {
		t.Error("an image of the agent's own runtime must be accepted")
	}
}
