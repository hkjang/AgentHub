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
