package runtimetype

import (
	"fmt"
	"slices"
)

// SettingKey is the system_settings row that controls which runtime types may
// be used for new Agent definitions.
//
// DisabledTypes is stored instead of EnabledTypes so an installation upgraded
// from a version without this setting keeps every adapter available. An empty
// document therefore has the same safe, backwards-compatible meaning as no row.
const SettingKey = "runtimeAgents"

// Settings controls which runtime adapters a site offers for new Agents.
// Existing Agents deliberately keep working when their type is disabled: this
// is a catalogue and creation policy, not an emergency stop for running work.
type Settings struct {
	DisabledTypes []string `json:"disabledTypes"`
}

// Enabled reports whether a new Agent may use runtimeType.
func (s Settings) Enabled(runtimeType string) bool {
	return !slices.Contains(s.DisabledTypes, runtimeType)
}

// Validate rejects values the control plane cannot enforce. Duplicate entries
// are rejected too, so the saved document remains an unambiguous set rather
// than silently normalising input from an API client.
func (s Settings) Validate() error {
	seen := map[string]bool{}
	for _, runtimeType := range s.DisabledTypes {
		if !IsSupported(runtimeType) {
			return fmt.Errorf("지원하지 않는 Runtime 유형입니다: %s", runtimeType)
		}
		if seen[runtimeType] {
			return fmt.Errorf("Runtime 유형이 중복되었습니다: %s", runtimeType)
		}
		seen[runtimeType] = true
	}
	return nil
}
