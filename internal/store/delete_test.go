package store

import "testing"

// The delete paths build SQL from these names, so a typo only surfaces as a
// runtime 500. Every kind the admin API registers must resolve to a real table.
func TestAdminResourceTableCoversEveryRegisteredKind(t *testing.T) {
	expected := map[string]string{
		"runtime-profiles":  "runtime_profiles",
		"runtime-images":    "runtime_images",
		"models":            "model_endpoints",
		"mcp-servers":       "mcp_servers",
		"mcp-bundles":       "mcp_bundles",
		"security-profiles": "security_profiles",
		"network-profiles":  "network_profiles",
	}
	for kind, table := range expected {
		got, err := adminResourceTable(kind)
		if err != nil || got != table {
			t.Errorf("adminResourceTable(%q) = (%q, %v), want %q", kind, got, err, table)
		}
	}
	for _, unknown := range []string{"", "users", "agents", "profiles", "audit-profiles"} {
		if _, err := adminResourceTable(unknown); err == nil {
			t.Errorf("adminResourceTable(%q) must be rejected", unknown)
		}
	}
}
