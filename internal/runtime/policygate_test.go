package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A binding whose only restriction is the platform's "every tool needs a person"
// still has to be routed through the gateway and still has to carry the rule
// there. Before this, the flag was computed, dropped, and the Pod was provisioned
// as if no policy existed.
func TestServerWideGateReachesTheCRD(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime:    store.Runtime{CRDName: "agent-user-agent"},
		Agent:      store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
		Profile:    store.RuntimeProfile{CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10},
		MCPServers: []MCPBinding{{Name: "github", Mode: "shared", Endpoint: "https://mcp.example/mcp", PolicyGateAll: true}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Spec struct {
			MCP []struct {
				ToolPolicy *struct {
					PolicyGateAll bool `json:"policyGateAll"`
				} `json:"toolPolicy"`
			} `json:"mcp"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Spec.MCP) != 1 || decoded.Spec.MCP[0].ToolPolicy == nil {
		t.Fatalf("a gated binding must be policied: %s", raw)
	}
	if !decoded.Spec.MCP[0].ToolPolicy.PolicyGateAll {
		t.Fatalf("the server-wide gate did not reach the CRD: %s", raw)
	}
}

// An exception has to travel with the restriction it is an exception to. If only
// the deny arrives, the Pod enforces a rule the operator never wrote — and the
// console, which evaluates the document, keeps saying the tool is allowed.
func TestAnExceptionReachesTheCRDBesideItsRestriction(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime:    store.Runtime{CRDName: "agent-user-agent"},
		Agent:      store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
		Profile:    store.RuntimeProfile{CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10},
		MCPServers: []MCPBinding{{Name: "github", Mode: "shared", Endpoint: "https://mcp.example/mcp", PolicyDenyAll: true, PolicyAllowed: []string{"read_file"}}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Spec struct {
			MCP []struct {
				ToolPolicy *struct {
					PolicyAllowed []string `json:"policyAllowed"`
				} `json:"toolPolicy"`
			} `json:"mcp"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Spec.MCP) != 1 || decoded.Spec.MCP[0].ToolPolicy == nil {
		t.Fatalf("a policied binding must be policied: %s", raw)
	}
	if strings.Join(decoded.Spec.MCP[0].ToolPolicy.PolicyAllowed, ",") != "read_file" {
		t.Fatalf("the exception did not reach the CRD: %s", raw)
	}
}

// The rules reach the CRD in the order they were written, with the default
// beside them. Summarised into lists, the order — which is what decides — was
// left behind in the control plane.
func TestTheOrderedRulesReachTheCRD(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime: store.Runtime{CRDName: "agent-user-agent"},
		Agent:   store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
		Profile: store.RuntimeProfile{CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10},
		MCPServers: []MCPBinding{{Name: "github", Mode: "shared", Endpoint: "https://mcp.example/mcp",
			PolicyRules: []PolicyRule{
				{Effect: "require_approval", Tools: []string{"delete_*"}},
				{Effect: "deny"},
			},
			PolicyDefault: "deny"}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Spec struct {
			MCP []struct {
				ToolPolicy *struct {
					PolicyRules []struct {
						Effect string   `json:"effect"`
						Tools  []string `json:"tools"`
					} `json:"policyRules"`
					PolicyDefault string `json:"policyDefault"`
				} `json:"toolPolicy"`
			} `json:"mcp"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Spec.MCP) != 1 || decoded.Spec.MCP[0].ToolPolicy == nil {
		t.Fatalf("a binding the policy decides has to be policied: %s", raw)
	}
	rules := decoded.Spec.MCP[0].ToolPolicy.PolicyRules
	if len(rules) != 2 || rules[0].Effect != "require_approval" || strings.Join(rules[0].Tools, ",") != "delete_*" {
		t.Fatalf("the rules did not reach the CRD in order: %s", raw)
	}
	if decoded.Spec.MCP[0].ToolPolicy.PolicyDefault != "deny" {
		t.Fatalf("the document default did not reach the CRD: %s", raw)
	}
}

// The CRD schema prunes anything it does not declare, so a field the spawner
// writes and the schema omits never arrives — silently, and only in a cluster.
func TestCRDDeclaresEveryToolPolicyFieldTheSpawnerWrites(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kubernetes", "crd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	schema := string(body)
	for _, field := range []string{"policyRules", "policyDefault", "policyDenied", "policyGated", "policyAllowed", "policyDenyAll", "policyGateAll", "approvalTools", "approvalRequired"} {
		if !strings.Contains(schema, field+":") {
			t.Errorf("the CRD does not declare toolPolicy.%s, so the API server drops it", field)
		}
	}
}
