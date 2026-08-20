package runtimecfg

import (
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// A runtime configured only through the environment must not be offered a
// configuration-file setting: the API refuses it, so the offer would be a
// promise the platform breaks.
func TestConfiglessRuntimesAreOnlyOfferedEnvironmentSettings(t *testing.T) {
	for runtimeType := range configless {
		for _, item := range Suggestions(runtimeType) {
			if item.Target == TargetConfig {
				t.Errorf("%s was offered the config setting %q although it has no configuration file", runtimeType, item.Label)
			}
		}
		profile := Profile{RuntimeType: runtimeType, Config: map[string]any{"theme": "dark"}}
		if err := (Settings{Profiles: []Profile{profile}}).Validate(); err == nil {
			t.Errorf("%s accepted a config overlay it cannot apply", runtimeType)
		}
	}
	// And the runtimes that do have one keep being offered both kinds.
	config := false
	for _, item := range Suggestions(runtimetype.OpenCode) {
		config = config || item.Target == TargetConfig
	}
	if !config {
		t.Error("opencode lost its configuration suggestions")
	}
}

// Every Langflow setting the platform decides itself has to be refused rather
// than accepted and then overridden by the adapter: a setting that saves and
// does nothing is the failure this package exists to remove.
func TestPlatformOwnedLangflowVariablesAreRefused(t *testing.T) {
	for _, name := range []string{"LANGFLOW_HOST", "LANGFLOW_PORT", "LANGFLOW_AUTO_LOGIN", "LANGFLOW_API_KEY", "LANGFLOW_API_KEY_SOURCE", "LANGFLOW_CONFIG_DIR", "LANGFLOW_SKIP_AUTH_AUTO_LOGIN", "DO_NOT_TRACK"} {
		profile := Profile{RuntimeType: runtimetype.Langflow, Env: map[string]string{name: "x"}}
		if err := (Settings{Profiles: []Profile{profile}}).Validate(); err == nil {
			t.Errorf("%s was accepted although the platform sets it", name)
		}
	}
	// The ones a site legitimately tunes still go through.
	profile := Profile{RuntimeType: runtimetype.Langflow, Env: map[string]string{"LANGFLOW_LOG_LEVEL": "info", "LANGFLOW_WORKERS": "2", "TZ": "Asia/Seoul"}}
	if err := (Settings{Profiles: []Profile{profile}}).Validate(); err != nil {
		t.Errorf("a legitimate Langflow overlay was refused: %v", err)
	}
}
