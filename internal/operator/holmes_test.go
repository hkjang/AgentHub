package operator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimecfg"
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

// BrowserCode reads the configuration shape it inherited from OpenCode, so the
// platform builds it the same way — the provider block, and MCP servers written
// exactly as OpenCode's are. Checked against the real agent too, which offers
// the server's tools namespaced under its name.
func TestBrowserCodeConfigCarriesTheModelTheBrowserNoteAndItsMCPServers(t *testing.T) {
	var value spec
	value.Runtime.Type = runtimetype.BrowserCode
	value.Model.Name = "gpt-5"
	value.Model.BaseURL = "http://models.internal/v1"
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp"}}

	raw := runtimeConfigs("agent-runtime-dev", "rt-1", value)[configBcode]
	var config struct {
		Model        string   `json:"model"`
		Autoupdate   bool     `json:"autoupdate"`
		Instructions []string `json:"instructions"`
		Provider     map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
		MCP map[string]struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Enabled bool   `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("the generated configuration is not valid JSON: %v\n%s", err, raw)
	}
	if config.Model != openCodeProvider+"/gpt-5" {
		t.Errorf("model = %q", config.Model)
	}
	if config.Provider[openCodeProvider].Options.BaseURL != "http://models.internal/v1" {
		t.Errorf("the model endpoint did not reach the agent: %s", raw)
	}
	// An agent that updates itself is an agent whose version nobody pinned.
	if config.Autoupdate {
		t.Error("the agent is configured to update itself")
	}
	// Without this the agent cannot find the browser running beside it: it looks
	// for a DevTools port file current Chromium does not write, so it has to be
	// told the websocket URL, and this file is where that is written down.
	if len(config.Instructions) != 1 || !strings.HasSuffix(config.Instructions[0], "browsercode-browser.md") {
		t.Errorf("instructions = %v, want the browser note the image ships", config.Instructions)
	}
	server, ok := config.MCP["toolbox"]
	if !ok || server.URL != "http://mcp.internal:8000/mcp" || server.Type != "remote" || !server.Enabled {
		t.Errorf("the bound MCP server is missing or misdeclared: %s", raw)
	}
}

// An administrator's overlay must not be able to stop the operator.
//
// Every runtime whose generated configuration holds a map the builder writes
// into afterwards is one bad overlay away from a type assertion — and a panic in
// Reconcile takes the operator down and brings it back into the same panic on the
// next retry. The keys below are reserved from the overlay now, and the builder
// reads them back defensively, so this is checked from both ends.
func TestABadOverlayCannotBringDownTheOperator(t *testing.T) {
	for _, runtime := range []struct {
		runtimeType string
		key         string
	}{
		{runtimetype.BrowserCode, "mcp"},
		{runtimetype.Goose, "extensions"},
		{runtimetype.Holmes, "mcp_servers"},
		{runtimetype.OpenCode, "mcp"},
		{runtimetype.QwenCode, "mcpServers"},
		{runtimetype.Hermes, "mcp_servers"},
	} {
		t.Run(runtime.runtimeType, func(t *testing.T) {
			var value spec
			value.Runtime.Type = runtime.runtimeType
			value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp"}}
			// A string where the platform builds a map: accepted by the settings
			// validator, because it does not know the shape the builder expects.
			value.RuntimeSettings.Config = map[string]any{runtime.key: "not a map"}

			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("an overlay setting %q crashed the operator: %v", runtime.key, recovered)
				}
			}()
			configs := runtimeConfigs("agent-runtime-dev", "rt-1", value)
			if len(configs) == 0 {
				t.Fatal("no configuration was generated")
			}
		})
	}
}

// And the same keys are refused at the edge, so the overlay above is rejected
// when somebody saves it rather than silently ignored when it is used.
func TestThePlatformsOwnKeysAreRefusedInAnOverlay(t *testing.T) {
	for _, item := range []struct {
		runtimeType string
		key         string
	}{
		{runtimetype.BrowserCode, "mcp"},
		{runtimetype.BrowserCode, "model"},
		{runtimetype.BrowserCode, "provider"},
		// The file that tells the agent how to reach its browser. Replacing it
		// leaves a runtime that starts cleanly and fails every browser task.
		{runtimetype.BrowserCode, "instructions"},
		{runtimetype.Goose, "extensions"},
		{runtimetype.Holmes, "mcp_servers"},
		{runtimetype.Holmes, "api_base"},
	} {
		settings := runtimecfg.Settings{Profiles: []runtimecfg.Profile{{
			RuntimeType: item.runtimeType,
			Config:      map[string]any{item.key: map[string]any{"anything": true}},
		}}}
		if err := settings.Validate(); err == nil {
			t.Errorf("%s: an overlay setting %q was accepted", item.runtimeType, item.key)
		}
	}
}

// BrowserCode's provider block is built from OpenCode's, because it is that
// agent's fork. Built from, not shared: the merge writes through nested maps, so
// a shared one would let an overlay on either runtime reach into the other
// document in the same ConfigMap.
func TestTwoRuntimesDoNotShareOneGeneratedDocument(t *testing.T) {
	var value spec
	value.Runtime.Type = runtimetype.BrowserCode
	value.Model.Name = "gpt-5"
	value.Model.BaseURL = "http://models.internal/v1"
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp"}}
	// An overlay on a key the platform does not reserve, nested inside one it
	// builds, is what would travel between them.
	value.RuntimeSettings.Config = map[string]any{"agent": map[string]any{"build": map[string]any{"model": "other"}}}

	configs := runtimeConfigs("agent-runtime-dev", "rt-1", value)
	var opencode, bcode struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
		MCP map[string]struct {
			URL string `json:"url"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(configs[configOpenCode]), &opencode); err != nil {
		t.Fatalf("opencode: %v", err)
	}
	if err := json.Unmarshal([]byte(configs[configBcode]), &bcode); err != nil {
		t.Fatalf("bcode: %v", err)
	}
	if opencode.Provider[openCodeProvider].Options.BaseURL != bcode.Provider[openCodeProvider].Options.BaseURL {
		t.Error("the two documents disagree about the model endpoint")
	}
	if opencode.MCP["toolbox"].URL != bcode.MCP["toolbox"].URL {
		t.Error("the two documents disagree about the MCP binding")
	}
	// The proof they are separate: mutating one leaves the other alone.
	generated := runtimeConfigs("agent-runtime-dev", "rt-1", value)
	if generated[configOpenCode] != configs[configOpenCode] {
		t.Error("generating twice produced different OpenCode configuration")
	}
}
