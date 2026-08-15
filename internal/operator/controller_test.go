package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRuntimeConfigsCompileModelAndMCPBindings(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.1.0"
	value.Model.BaseURL = "http://model-gateway.ai.svc:8000/v1"
	value.Model.Name = "qwen-coder"
	value.MCP = append(value.MCP,
		struct {
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			Endpoint string `json:"endpoint"`
			Image    string `json:"image"`
			Port     int32  `json:"port"`
		}{Name: "jira", Mode: "shared", Endpoint: "https://mcp.example.test/mcp"},
		struct {
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			Endpoint string `json:"endpoint"`
			Image    string `json:"image"`
			Port     int32  `json:"port"`
		}{Name: "filesystem", Mode: "sidecar", Image: "mcp/filesystem:1", Port: 8100},
		struct {
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			Endpoint string `json:"endpoint"`
			Image    string `json:"image"`
			Port     int32  `json:"port"`
		}{Name: "database", Mode: "dedicated", Image: "mcp/database:1", Port: 8200},
	)

	runtimeRaw, openRaw, hermesRaw := runtimeConfigs("agent-runtime-dev", "agent-alice-1234", value)
	for _, expected := range []string{"http://127.0.0.1:8100/mcp", "agent-alice-1234-mcp-database.agent-runtime-dev.svc:8200", "https://mcp.example.test/mcp"} {
		if !strings.Contains(runtimeRaw, expected) || !strings.Contains(openRaw, expected) || !strings.Contains(hermesRaw, expected) {
			t.Fatalf("compiled configurations do not contain %q", expected)
		}
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(openRaw), &config); err != nil {
		t.Fatalf("OpenCode config is not JSON: %v", err)
	}
	if config["model"] != "ollama/qwen-coder" && config["model"] != "agenthub/qwen-coder" {
		t.Fatalf("unexpected OpenCode model: %#v", config["model"])
	}
}

func TestHermesStatefulSetUsesLoopbackDashboardAndAuthenticatedProxy(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("hermes-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "hermes"
	value.Runtime.Image = "agenthub-base:v0.1.0"
	value.Security.ReadOnlyRootFilesystem = true

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "hermes-runtime", "hermes-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "hermes-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	containers := map[string]struct {
		command   []string
		args      []string
		readiness string
		liveness  string
	}{}
	for _, container := range statefulSet.Spec.Template.Spec.Containers {
		readiness, liveness := "", ""
		if container.ReadinessProbe != nil && container.ReadinessProbe.HTTPGet != nil {
			readiness = container.ReadinessProbe.HTTPGet.Path
		}
		if container.LivenessProbe != nil && container.LivenessProbe.HTTPGet != nil {
			liveness = container.LivenessProbe.HTTPGet.Path
		}
		containers[container.Name] = struct {
			command   []string
			args      []string
			readiness string
			liveness  string
		}{container.Command, container.Args, readiness, liveness}
	}
	dashboard, ok := containers["hermes-dashboard"]
	if !ok || !strings.Contains(strings.Join(dashboard.args, " "), "--host 127.0.0.1 --port 9120") {
		t.Fatalf("Hermes Dashboard is not loopback-only: %#v", dashboard)
	}
	proxy, ok := containers["hermes-dashboard-proxy"]
	if !ok || strings.Join(proxy.command, " ") != "/usr/local/bin/agenthub-runtime-proxy" || proxy.readiness != "/healthz" || proxy.liveness != "/livez" {
		t.Fatalf("authenticated Dashboard proxy is incomplete: %#v", proxy)
	}
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("Runtime Pod must not receive a Kubernetes service account token")
	}
}

func TestEndpointPort(t *testing.T) {
	tests := map[string]int32{
		"https://gateway.example/v1":      443,
		"http://gateway.example/v1":       80,
		"http://gateway.example:11434/v1": 11434,
		"not a url":                       0,
	}
	for input, expected := range tests {
		if got := endpointPort(input); got != expected {
			t.Errorf("endpointPort(%q)=%d, want %d", input, got, expected)
		}
	}
}

func TestSidecarsUseRestrictedSecurityContext(t *testing.T) {
	var value spec
	value.MCP = append(value.MCP, struct {
		Name     string `json:"name"`
		Mode     string `json:"mode"`
		Endpoint string `json:"endpoint"`
		Image    string `json:"image"`
		Port     int32  `json:"port"`
	}{Name: "git", Mode: "sidecar", Image: "mcp/git:1", Port: 8000})
	containers := sidecarContainers(value)
	if len(containers) != 1 {
		t.Fatalf("got %d sidecars, want 1", len(containers))
	}
	security := containers[0].SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.RunAsNonRoot == nil || !*security.RunAsNonRoot {
		t.Fatal("sidecar security context is not restricted")
	}
	if len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("sidecar must drop all capabilities: %#v", security.Capabilities)
	}
}
