package runtimetype

import "testing"

func TestRuntimeAgentSettingsDefaultToEveryTypeEnabled(t *testing.T) {
	settings := Settings{}
	for _, runtimeType := range Supported {
		if !settings.Enabled(runtimeType) {
			t.Errorf("%s should remain enabled when the setting is absent", runtimeType)
		}
	}
}

func TestRuntimeAgentSettingsDisableOnlyNamedTypes(t *testing.T) {
	settings := Settings{DisabledTypes: []string{Hermes, Custom}}
	if settings.Enabled(Hermes) || settings.Enabled(Custom) {
		t.Fatal("disabled runtime types were reported as enabled")
	}
	if !settings.Enabled(OpenCode) {
		t.Fatal("an unlisted runtime type was disabled")
	}
}

func TestRuntimeAgentSettingsRejectUnknownAndDuplicateTypes(t *testing.T) {
	for name, settings := range map[string]Settings{
		"unknown":   {DisabledTypes: []string{"not-a-runtime"}},
		"duplicate": {DisabledTypes: []string{OpenCode, OpenCode}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := settings.Validate(); err == nil {
				t.Fatal("invalid runtime Agent settings were accepted")
			}
		})
	}
	if err := (Settings{DisabledTypes: append([]string(nil), Supported...)}).Validate(); err != nil {
		t.Fatalf("supported runtime types were rejected: %v", err)
	}
}
