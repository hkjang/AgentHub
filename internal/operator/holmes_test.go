package operator

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

// Cluster read is a privilege, so what it does and does not do is worth pinning
// down. Off by default; and when it is on, the credential arrives the way every
// other credential does rather than by mounting the service account token, which
// stays forbidden.
func TestClusterReadIsOffUntilGrantedAndNeverAutomountsAToken(t *testing.T) {
	build := func(granted bool) spec {
		var value spec
		value.Runtime.Type = runtimetype.Holmes
		value.Security.ClusterRead = granted
		return value
	}

	withoutIt := clusterReadVolume(build(false))
	if len(withoutIt) != 0 || len(clusterReadMounts(build(false))) != 0 || len(clusterReadEnv(build(false))) != 0 {
		t.Error("a runtime nobody granted anything to was given a cluster credential")
	}

	volumes := clusterReadVolume(build(true))
	if len(volumes) != 1 || volumes[0].Projected == nil {
		t.Fatalf("volume = %#v, want a projected token", volumes)
	}
	projected := volumes[0].Projected.Sources[0].ServiceAccountToken
	if projected == nil {
		t.Fatal("the volume is not a service account token projection")
	}
	// The API server's certificate has to travel with the token: the path every
	// example uses exists only when the token is automounted, which it is not
	// here, so a kubeconfig naming it fails before reaching the network.
	var certificate bool
	for _, source := range volumes[0].Projected.Sources {
		if source.ConfigMap != nil && source.ConfigMap.Name == "kube-root-ca.crt" {
			certificate = true
		}
	}
	if !certificate {
		t.Error("the projection carries a token but no certificate authority")
	}
	if !strings.Contains(clusterReadKubeconfig("agent-runtime-dev"), clusterReadMount+"/ca.crt") {
		t.Error("the kubeconfig does not point at the projected certificate")
	}
	// The expiry is the difference between this and automounting: the kubelet
	// replaces the token, so withdrawing the privilege stops working access rather
	// than leaving a usable token behind.
	if projected.ExpirationSeconds == nil || *projected.ExpirationSeconds <= 0 {
		t.Error("the token does not expire")
	}
	// And the audience is deliberately unset, which means the API server's own.
	// Naming one made the API server refuse the token with a message that reads
	// like there was no token at all.
	if projected.Audience != "" {
		t.Errorf("audience = %q, want the API server's own", projected.Audience)
	}
	if mounts := clusterReadMounts(build(true)); len(mounts) != 1 || !mounts[0].ReadOnly {
		t.Errorf("mount = %#v, want one read-only mount", mounts)
	}
	if env := clusterReadEnv(build(true)); len(env) != 1 || env[0].Name != "KUBECONFIG" {
		t.Errorf("env = %#v, want KUBECONFIG", env)
	}
}

// The investigator's toolsets follow the privilege: with no cluster credential
// the Kubernetes toolsets are not offered at all, rather than offered and
// failing their health check on every start.
func TestHolmesToolsetsFollowTheGrant(t *testing.T) {
	toolsets := func(granted bool) string {
		var build adapterBuild
		build.Name = "rt-1"
		build.Value.Runtime.Type = runtimetype.Holmes
		build.Value.Security.ClusterRead = granted
		for _, item := range adapterFor(runtimetype.Holmes).Env(build) {
			if item.Name == "ENABLED_BY_DEFAULT_TOOLSETS" {
				return item.Value
			}
		}
		return ""
	}
	if got := toolsets(false); strings.Contains(got, "kubernetes") {
		t.Errorf("without the grant the toolsets are %q", got)
	}
	if got := toolsets(true); !strings.Contains(got, "kubernetes/core") || !strings.Contains(got, "kubernetes/logs") {
		t.Errorf("with the grant the toolsets are %q", got)
	}
}

// The binding is owned by the runtime, so deleting the runtime withdraws the
// grant. A privilege that outlived the thing it was granted to would be one
// nobody remembers giving.
func TestTheClusterReadBindingBelongsToTheRuntime(t *testing.T) {
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("rt-1")
	owner.SetUID("uid-1")

	binding := clusterReadBinding("agent-runtime-dev", "rt-1", owner)
	// Kubernetes' own read-only role, which cannot read Secrets. Binding anything
	// else would be a different feature with a different conversation behind it.
	if binding.RoleRef.Name != "view" || binding.RoleRef.Kind != "ClusterRole" {
		t.Errorf("binds %s/%s", binding.RoleRef.Kind, binding.RoleRef.Name)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != "rt-1" || binding.Subjects[0].Namespace != "agent-runtime-dev" {
		t.Errorf("subjects = %#v, want only this runtime's own service account", binding.Subjects)
	}
	// The owner reference is provenance, not lifecycle: Kubernetes will not
	// garbage-collect a cluster-scoped object owned by a namespaced one, which is
	// why sweepClusterRead exists. Checked against a real cluster, where deleting
	// the runtime left the binding behind.
	if len(binding.OwnerReferences) != 1 || binding.OwnerReferences[0].UID != "uid-1" {
		t.Errorf("owner references = %#v, want the AgentRuntime named", binding.OwnerReferences)
	}
	// The label the sweep selects on. Without it the sweep sees nothing and the
	// grants accumulate silently.
	if binding.Labels["app.kubernetes.io/managed-by"] != "agenthub-operator" {
		t.Errorf("labels = %v, want one the sweep can select", binding.Labels)
	}
	if !strings.HasPrefix(binding.Name, clusterReadBindingPrefix) {
		t.Errorf("name = %q, want the prefix the sweep matches", binding.Name)
	}
}
