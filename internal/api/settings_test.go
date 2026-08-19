package api

import (
	"net/http/httptest"
	"strings"
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

// Saving the runtime environment used to answer "saved" and change nothing an
// administrator could see, because each runtime object carries its own copy. The
// message is the part they read, so it has to say what actually happened.
func TestRuntimeEnvironmentApplied(t *testing.T) {
	cases := []struct {
		name                     string
		applied, skipped, failed int
		expect                   string
	}{
		{name: "nothing running says so rather than claiming success", expect: "다음에 시작할 때"},
		{name: "a push that worked names the count", applied: 3, expect: "3개에 적용"},
		{name: "a push that worked says the Pods restart", applied: 1, expect: "재시작"},
		{name: "a partial push reports both halves", applied: 2, failed: 1, expect: "실패"},
		{name: "a push that failed does not read as success", failed: 2, expect: "적용하지 못했습니다"},
		{name: "no cluster is not a failure", skipped: 4, expect: "Kubernetes"},
	}
	// An outdated CRD is the one case that looks like the feature is broken, so
	// it says what to do instead of reporting a count.
	pruned := runtimeEnvironmentApplied(syncResult{pruned: true})
	if message, _ := pruned["message"].(string); !strings.Contains(message, "crd.yaml") {
		t.Fatalf("a pruned environment must name the fix; got %q", message)
	}
	if pruned["crdOutdated"] != true {
		t.Fatalf("a pruned environment must be reported as such: %#v", pruned)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := runtimeEnvironmentApplied(syncResult{applied: test.applied, skipped: test.skipped, failed: test.failed})
			message, _ := result["message"].(string)
			if !strings.Contains(message, test.expect) {
				t.Fatalf("message %q does not mention %q", message, test.expect)
			}
			if result["applied"] != test.applied || result["failed"] != test.failed {
				t.Fatalf("counts are not reported: %#v", result)
			}
		})
	}
}
