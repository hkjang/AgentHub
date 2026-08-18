package api

import (
	"net/http/httptest"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimeenv"
)

// The runtime environment setting is the one whose shape the platform has to
// know exactly, so the handler has to reject a malformed document rather than
// storing something the operator will silently drop.
func TestRuntimeEnvironmentSettingIsValidatedOnTheWayIn(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest("PUT", "/api/v1/admin/settings/"+runtimeenv.SettingKey, nil)
	valid := map[string]any{"files": []any{map[string]any{"path": "/etc/pip.conf", "content": "[global]\n", "mode": "0644"}}, "variables": []any{map[string]any{"name": "PIP_INDEX_URL", "value": "https://nexus.local/simple"}}}
	if err := server.validateSetting(request, runtimeenv.SettingKey, valid, nil); err != nil {
		t.Fatalf("a valid runtime environment was rejected: %v", err)
	}
	rejected := map[string]map[string]any{
		"relative path":     {"files": []any{map[string]any{"path": "etc/pip.conf"}}},
		"platform path":     {"files": []any{map[string]any{"path": "/etc/agenthub/runtime.json"}}},
		"platform variable": {"variables": []any{map[string]any{"name": "OPENAI_API_KEY", "value": "x"}}},
		"bad mode":          {"files": []any{map[string]any{"path": "/etc/pip.conf", "mode": "rw-r--r--"}}},
		"unknown field":     {"file": []any{map[string]any{"path": "/etc/pip.conf"}}},
		"wrong type":        {"files": map[string]any{"path": "/etc/pip.conf"}},
	}
	for name, value := range rejected {
		if err := server.validateSetting(request, runtimeenv.SettingKey, value, nil); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
