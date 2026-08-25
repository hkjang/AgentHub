package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func TestRuntimeTypeDisabledIsClassifiedAndActionable(t *testing.T) {
	err := RuntimeTypeDisabled{RuntimeType: runtimetype.Hermes}
	if !errors.Is(err, ErrRuntimeTypeDisabled) {
		t.Fatal("the disabled runtime error cannot be classified by the API")
	}
	for _, text := range []string{"Hermes", "관리자", "Runtime Agents"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("the disabled runtime error %q does not mention %q", err.Error(), text)
		}
	}
}
