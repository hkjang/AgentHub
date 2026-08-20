package operator

import (
	"encoding/json"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The configuration file is what the investigator reads for its model and its
// data sources, and every shape in it was checked against the agent rather than
// guessed. Two of them refuse the obvious spelling by name: the address lives
// under `config`, and the transport is "streamable-http" with a hyphen. Get
// either wrong and the server is asked for /sse, answers 405, and the runtime
// comes up with a toolset that quietly is not there.
func TestHolmesConfigDeclaresItsModelAndMCPServersTheWayTheAgentReadsThem(t *testing.T) {
	var value spec
	value.Runtime.Type = runtimetype.Holmes
	value.Model.Name = "gpt-5"
	value.Model.BaseURL = "http://models.internal/v1"
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp"}}

	raw := runtimeConfigs("agent-runtime-dev", "rt-1", value)[configHolmes]
	var config struct {
		Model      string `json:"model"`
		APIBase    string `json:"api_base"`
		MCPServers map[string]struct {
			Description string `json:"description"`
			Config      struct {
				Mode    string            `json:"mode"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"config"`
		} `json:"mcp_servers"`
	}
	// Written as JSON under a .yaml name: the agent parses YAML, YAML is a
	// superset of JSON, and the platform keeps one way of building configuration.
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("the generated configuration is not valid JSON: %v\n%s", err, raw)
	}
	// The provider prefix tells the agent's model client which protocol to speak;
	// without it the model name alone is not routable.
	if config.Model != "openai/gpt-5" {
		t.Errorf("model = %q, want it prefixed with the protocol", config.Model)
	}
	if config.APIBase != "http://models.internal/v1" {
		t.Errorf("api_base = %q", config.APIBase)
	}
	server, ok := config.MCPServers["toolbox"]
	if !ok {
		t.Fatalf("the bound MCP server is missing: %s", raw)
	}
	if server.Config.URL != "http://mcp.internal:8000/mcp" {
		t.Errorf("url = %q, and it has to be under config rather than beside it", server.Config.URL)
	}
	if server.Config.Mode != "streamable-http" {
		t.Errorf("mode = %q, want streamable-http — the agent's default is SSE and refuses the underscore spelling", server.Config.Mode)
	}
}

// A policied binding is authenticated by the in-Pod gateway, so its credential
// must not reach the agent process; an unpolicied one carries its header.
func TestHolmesCredentialsFollowTheSameRuleAsEveryOtherRuntime(t *testing.T) {
	var value spec
	value.Runtime.Type = runtimetype.Holmes
	value.MCP = []mcpBinding{
		{Name: "open", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp",
			AuthType: "bearer", CredentialKey: "mcp-credential-open"},
		{Name: "policied", Mode: "shared", Endpoint: "http://mcp.internal:8001/mcp",
			AuthType: "bearer", CredentialKey: "mcp-credential-policied",
			ToolPolicy: &mcpToolPolicy{Mode: "allow", Tools: []string{"read"}}},
	}
	raw := runtimeConfigs("agent-runtime-dev", "rt-1", value)[configHolmes]
	var config struct {
		MCPServers map[string]struct {
			Config struct {
				Headers map[string]string `json:"headers"`
			} `json:"config"`
		} `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("invalid configuration: %v", err)
	}
	if len(config.MCPServers["open"].Config.Headers) == 0 {
		t.Error("an unpolicied binding lost its credential")
	}
	if len(config.MCPServers["policied"].Config.Headers) != 0 {
		t.Errorf("a policied binding handed its credential to the agent: %v", config.MCPServers["policied"].Config.Headers)
	}
}

// The investigator's own defaults enable toolsets that cannot work in a runtime
// Pod — the Kubernetes ones need a service account token the platform does not
// mount, and Robusta needs an account. Left on, they fail their health check
// every start and then tell the model they could not look.
func TestHolmesIsNotStartedWithToolsetsThatCannotWork(t *testing.T) {
	adapter := adapterFor(runtimetype.Holmes)
	if adapter.Env == nil {
		t.Fatal("HolmesGPT has no adapter")
	}
	var build adapterBuild
	build.Name = "rt-1"
	build.Value.Runtime.Type = runtimetype.Holmes
	found := ""
	for _, item := range adapter.Env(build) {
		if item.Name == "ENABLED_BY_DEFAULT_TOOLSETS" {
			found = item.Value
		}
	}
	if found == "" {
		t.Fatal("the runtime is started with the agent's own toolset defaults")
	}
	if found != "internet" {
		t.Errorf("default toolsets = %q", found)
	}
}
