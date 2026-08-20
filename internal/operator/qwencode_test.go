package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Qwen Code is a terminal program, so what the platform publishes is the
// terminal. That only works if three things hold together: it is served, it is
// served on loopback, and the only way in is the token-checking proxy. A browser
// terminal reachable without that proxy is a shell anyone in the cluster can use.
func TestQwenCodeServesATerminalBehindTheProxy(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("qwencode-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = runtimetype.QwenCode
	value.Runtime.Image = "agenthub-qwencode:v0.1.0"
	value.Runtime.SidecarImage = "agenthub:v0.22.0"
	value.Security.ReadOnlyRootFilesystem = true
	value.Model.Name = "qwen3-coder"
	value.Model.BaseURL = "http://models.internal/v1"

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "qwencode-runtime", "qwencode-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "qwencode-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var agent, proxy *corev1.Container
	for index, container := range statefulSet.Spec.Template.Spec.Containers {
		switch container.Name {
		case "agent":
			agent = &statefulSet.Spec.Template.Spec.Containers[index]
		case "qwencode-proxy":
			proxy = &statefulSet.Spec.Template.Spec.Containers[index]
		}
	}
	if agent == nil {
		t.Fatal("no agent container was generated")
	}
	args := strings.Join(agent.Args, " ")
	if !strings.Contains(args, "ttyd") || !strings.Contains(args, "agenthub-qwencode-shell") {
		t.Fatalf("the agent container does not serve the terminal: %#v", agent.Args)
	}
	if !strings.Contains(args, "--interface 127.0.0.1") {
		t.Errorf("the terminal must be bound to loopback: %#v", agent.Args)
	}
	if proxy == nil || strings.Join(proxy.Command, " ") != "/usr/local/bin/agenthub-runtime-proxy" {
		t.Fatalf("the terminal must be published through the authenticated proxy: %#v", proxy)
	}
	target := ""
	for _, item := range proxy.Env {
		if item.Name == "AGENTHUB_RUNTIME_PROXY_TARGET" {
			target = item.Value
		}
	}
	if target != "http://127.0.0.1:7681" {
		t.Errorf("the proxy points at %q, not at the terminal", target)
	}
	// Loopback again, so the probe has to be asked from inside the container.
	if agent.ReadinessProbe == nil || agent.ReadinessProbe.Exec == nil {
		t.Fatalf("a loopback runtime needs an in-container readiness probe: %#v", agent.ReadinessProbe)
	}
	// The initialiser prepares the settings, the credentials and the environment
	// `pip install` writes into; without it the agent starts with none of them.
	found := false
	for _, container := range statefulSet.Spec.Template.Spec.InitContainers {
		if container.Name == "qwencode-config-init" {
			found = strings.Contains(strings.Join(container.Command, " "), "agenthub-qwencode-configure")
		}
	}
	if !found {
		t.Error("Qwen Code needs its initialiser")
	}
}

// The settings file is what the agent reads for its tools, and the shape is the
// vendor's: a streamable HTTP server declared under `url` instead of `httpUrl`
// silently never connects, which looks exactly like a tool policy that denied
// everything.
func TestQwenCodeSettingsDeclareMCPServersTheWayTheAgentReadsThem(t *testing.T) {
	var value spec
	value.Runtime.Type = runtimetype.QwenCode
	value.Model.Name = "qwen3-coder"
	value.Model.BaseURL = "http://models.internal/v1"
	value.MCP = []mcpBinding{{Name: "toolbox", Mode: "shared", Endpoint: "http://mcp.internal:8000/mcp"}}

	_, _, _, qwenRaw := runtimeConfigs("agent-runtime-dev", "rt-1", value)
	var settings struct {
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
		MCPServers map[string]struct {
			HTTPURL string `json:"httpUrl"`
			URL     string `json:"url"`
		} `json:"mcpServers"`
		Privacy struct {
			UsageStatisticsEnabled bool `json:"usageStatisticsEnabled"`
		} `json:"privacy"`
		Telemetry struct {
			Enabled bool `json:"enabled"`
		} `json:"telemetry"`
	}
	if err := json.Unmarshal([]byte(qwenRaw), &settings); err != nil {
		t.Fatalf("the generated settings are not valid JSON: %v\n%s", err, qwenRaw)
	}
	if settings.Model.Name != "qwen3-coder" {
		t.Errorf("model = %q", settings.Model.Name)
	}
	server, ok := settings.MCPServers["toolbox"]
	if !ok {
		t.Fatalf("the bound MCP server is missing: %s", qwenRaw)
	}
	if server.HTTPURL != "http://mcp.internal:8000/mcp" || server.URL != "" {
		t.Errorf("a streamable HTTP server must be declared as httpUrl: %#v", server)
	}
	// An offline site must not report outwards, and neither setting is on by
	// default in the product.
	if settings.Privacy.UsageStatisticsEnabled || settings.Telemetry.Enabled {
		t.Error("usage statistics and telemetry must be off unless a site turns them on")
	}
}
