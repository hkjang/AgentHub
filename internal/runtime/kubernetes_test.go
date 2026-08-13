package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func TestRuntimeObjectReferencesSecretWithoutEmbeddingIt(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime:      store.Runtime{CRDName: "agent-user-agent"},
		Agent:        store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode", Version: 3},
		Profile:      store.RuntimeProfile{CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10},
		ModelBaseURL: "https://model.example/v1",
		ModelName:    "qwen-coder",
		ModelAPIKey:  "must-never-appear-in-crd",
		MCPServers:   []MCPBinding{{Name: "jira", Mode: "shared", Endpoint: "https://mcp.example/mcp"}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "must-never-appear-in-crd") {
		t.Fatal("model API key leaked into AgentRuntime CRD")
	}
	if !strings.Contains(text, `"secretRef":"agent-user-agent"`) {
		t.Fatalf("CRD does not reference its Kubernetes Secret: %s", text)
	}
}

func TestLabelValue(t *testing.T) {
	if got := labelValue("USER_A/Very Long Value"); got != "user-a-very-long-value" {
		t.Fatalf("unexpected label %q", got)
	}
	if got := labelValue(strings.Repeat("a", 100)); len(got) != 63 {
		t.Fatalf("label length is %d, want 63", len(got))
	}
}
