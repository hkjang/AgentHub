package operator

import (
	"encoding/json"
	"testing"
)

// A platform rule that gates every tool on a server is the only restriction some
// bindings carry. It has to produce a gateway, reach that gateway's configuration,
// and open the egress the gateway needs to ask a person — a gate the control
// plane cannot be reached from is a call that fails rather than one that waits.
func TestServerWideGateReachesTheGatewayAndItsEgress(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.7.0"
	value.MCP = append(value.MCP, mcpBinding{
		Name: "github", Mode: "shared", Endpoint: "https://mcp.github.test/mcp",
		ToolPolicy: &mcpToolPolicy{PolicyGateAll: true},
	})

	bindings := effectiveMCP("agent-runtime-dev", "rt-1", value)
	gateway, ok := mcpGatewayContainer(value.Runtime.Image, "rt-1", "runtime-1", bindings, value.MCP, value)
	if !ok {
		t.Fatal("a server-wide gate must produce a gateway container")
	}
	var config string
	approvalURL := false
	for _, env := range gateway.Env {
		switch env.Name {
		case "AGENTHUB_MCP_GATEWAY":
			config = env.Value
		case "AGENTHUB_APPROVAL_URL":
			approvalURL = env.Value != ""
		}
	}
	if !approvalURL {
		t.Fatal("the gateway was not told where to ask for approval")
	}
	var upstreams []struct {
		Name          string `json:"name"`
		PolicyGateAll bool   `json:"policyGateAll"`
	}
	if err := json.Unmarshal([]byte(config), &upstreams); err != nil {
		t.Fatalf("gateway config is not valid JSON: %v (%s)", err, config)
	}
	if len(upstreams) != 1 || !upstreams[0].PolicyGateAll {
		t.Fatalf("the server-wide gate did not reach the gateway: %s", config)
	}
	if !gatesApproval(value.MCP) {
		t.Fatal("the egress to the control plane stays closed, so every gated call would fail instead of waiting")
	}
}

// The exception the restriction was written with has to arrive with it. The
// operator reads the CRD and writes the gateway's configuration, so a field it
// does not copy is a rule that was compiled, stored, delivered, and then dropped
// one hop short of the process that enforces it.
func TestAnExceptionReachesTheGatewayWithItsRestriction(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.7.0"
	value.MCP = append(value.MCP, mcpBinding{
		Name: "github", Mode: "shared", Endpoint: "https://mcp.github.test/mcp",
		ToolPolicy: &mcpToolPolicy{PolicyDenyAll: true, PolicyAllowed: []string{"read_file"}},
	})

	bindings := effectiveMCP("agent-runtime-dev", "rt-1", value)
	gateway, ok := mcpGatewayContainer(value.Runtime.Image, "rt-1", "runtime-1", bindings, value.MCP, value)
	if !ok {
		t.Fatal("a server-wide deny must produce a gateway container")
	}
	var config string
	for _, env := range gateway.Env {
		if env.Name == "AGENTHUB_MCP_GATEWAY" {
			config = env.Value
		}
	}
	var upstreams []struct {
		PolicyDenyAll bool     `json:"policyDenyAll"`
		PolicyAllowed []string `json:"policyAllowed"`
	}
	if err := json.Unmarshal([]byte(config), &upstreams); err != nil {
		t.Fatalf("gateway config is not valid JSON: %v (%s)", err, config)
	}
	if len(upstreams) != 1 || !upstreams[0].PolicyDenyAll {
		t.Fatalf("the deny did not reach the gateway: %s", config)
	}
	if len(upstreams[0].PolicyAllowed) != 1 || upstreams[0].PolicyAllowed[0] != "read_file" {
		t.Fatalf("the exception did not reach the gateway, so the deny arrives alone: %s", config)
	}
}

// The rules the control plane compiled have to reach the gateway in the order it
// compiled them, and a gate that only exists among them has to open the egress
// the gateway asks for approval through. A gate the operator cannot see is a
// call that fails rather than one that waits.
func TestTheOrderedRulesReachTheGatewayAndOpenItsEgress(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.7.0"
	value.MCP = append(value.MCP, mcpBinding{
		Name: "github", Mode: "shared", Endpoint: "https://mcp.github.test/mcp",
		ToolPolicy: &mcpToolPolicy{PolicyRules: []mcpPolicyRule{
			{Effect: "require_approval", Tools: []string{"delete_*"}},
			{Effect: "deny"},
		}, PolicyDefault: "deny"},
	})

	bindings := effectiveMCP("agent-runtime-dev", "rt-1", value)
	gateway, ok := mcpGatewayContainer(value.Runtime.Image, "rt-1", "runtime-1", bindings, value.MCP, value)
	if !ok {
		t.Fatal("a policied binding must produce a gateway container")
	}
	var config string
	approvalURL := false
	for _, env := range gateway.Env {
		switch env.Name {
		case "AGENTHUB_MCP_GATEWAY":
			config = env.Value
		case "AGENTHUB_APPROVAL_URL":
			approvalURL = env.Value != ""
		}
	}
	if !approvalURL {
		t.Fatal("the gateway was not told where to ask for approval")
	}
	var upstreams []struct {
		PolicyRules []struct {
			Effect string   `json:"effect"`
			Tools  []string `json:"tools"`
		} `json:"policyRules"`
		PolicyDefault string `json:"policyDefault"`
	}
	if err := json.Unmarshal([]byte(config), &upstreams); err != nil {
		t.Fatalf("gateway config is not valid JSON: %v (%s)", err, config)
	}
	if len(upstreams) != 1 || len(upstreams[0].PolicyRules) != 2 {
		t.Fatalf("the rules did not reach the gateway: %s", config)
	}
	if upstreams[0].PolicyRules[0].Effect != "require_approval" || upstreams[0].PolicyDefault != "deny" {
		t.Fatalf("the order or the default was lost on the way: %s", config)
	}
	if !gatesApproval(value.MCP) {
		t.Fatal("the egress to the control plane stays closed, so the gated call would fail instead of waiting")
	}
}

// The same for a default of approval, which is a gate over every tool on every
// server the document says nothing else about.
func TestADefaultOfApprovalOpensTheEgress(t *testing.T) {
	var value spec
	value.MCP = append(value.MCP, mcpBinding{Name: "github", ToolPolicy: &mcpToolPolicy{PolicyDefault: "require_approval"}})
	if !gatesApproval(value.MCP) {
		t.Fatal("every call would fail for want of a route to ask along")
	}
}
