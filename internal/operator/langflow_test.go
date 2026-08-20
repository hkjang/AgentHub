package operator

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Langflow's editor has no authenticator of its own once automatic login is on,
// and the platform turns automatic login on so that arriving through the proxy
// does not mean a second password. That combination is only safe if the port is
// unreachable any other way, so this pins the two halves together: bound to
// loopback, published through the token-checking proxy.
func TestLangflowIsBoundToLoopbackAndProxied(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("langflow-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = runtimetype.Langflow
	value.Runtime.Image = "agenthub-langflow:v0.1.0"
	value.Runtime.SidecarImage = "agenthub:v0.21.0"
	value.Security.ReadOnlyRootFilesystem = true

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "langflow-runtime", "langflow-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "langflow-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var agent, proxy *corev1.Container
	for index, container := range statefulSet.Spec.Template.Spec.Containers {
		switch container.Name {
		case "agent":
			agent = &statefulSet.Spec.Template.Spec.Containers[index]
		case "langflow-proxy":
			proxy = &statefulSet.Spec.Template.Spec.Containers[index]
		}
	}
	if agent == nil {
		t.Fatal("no agent container was generated")
	}
	if !strings.Contains(strings.Join(agent.Args, " "), "langflow run") {
		t.Fatalf("the agent container does not start Langflow: %#v", agent.Args)
	}
	env := map[string]string{}
	secretKeys := map[string]string{}
	for _, item := range agent.Env {
		env[item.Name] = item.Value
		if item.ValueFrom != nil && item.ValueFrom.SecretKeyRef != nil {
			secretKeys[item.Name] = item.ValueFrom.SecretKeyRef.Key
		}
	}
	if env["LANGFLOW_HOST"] != "127.0.0.1" {
		t.Errorf("Langflow must bind to loopback, got %q", env["LANGFLOW_HOST"])
	}
	if env["LANGFLOW_AUTO_LOGIN"] != "true" || env["LANGFLOW_API_KEY_SOURCE"] != "env" {
		t.Errorf("automatic login with an env-checked API key is what the flow runner authenticates against: %v", env)
	}
	if secretKeys["LANGFLOW_API_KEY"] != "runtime-token" {
		t.Errorf("the Langflow API key must be the runtime's own token, got %q", secretKeys["LANGFLOW_API_KEY"])
	}
	// Everything Langflow owns — flows, its database, the key it generates on
	// first start — has to be on the volume that survives a restart.
	if !strings.HasPrefix(env["LANGFLOW_CONFIG_DIR"], "/home/agent") {
		t.Errorf("Langflow's state is not on the home volume: %q", env["LANGFLOW_CONFIG_DIR"])
	}
	if env["DO_NOT_TRACK"] != "true" {
		t.Error("an offline site must not have Langflow's telemetry on")
	}
	// The model binding reaches flows as global variables; without this a person
	// would retype the endpoint into every flow they draw.
	for _, name := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "AGENTHUB_MODEL_NAME"} {
		if !strings.Contains(env["LANGFLOW_VARIABLES_TO_GET_FROM_ENVIRONMENT"], name) {
			t.Errorf("%s is not offered to flows: %q", name, env["LANGFLOW_VARIABLES_TO_GET_FROM_ENVIRONMENT"])
		}
	}
	if proxy == nil || strings.Join(proxy.Command, " ") != "/usr/local/bin/agenthub-runtime-proxy" {
		t.Fatalf("Langflow must be published through the authenticated proxy: %#v", proxy)
	}
	// The sidecar runs the control plane's image, which is what lets the runtime
	// image be almost-stock Langflow.
	if proxy.Image != "agenthub:v0.21.0" {
		t.Errorf("the proxy sidecar runs %q, not the control plane image", proxy.Image)
	}
	target := ""
	for _, item := range proxy.Env {
		if item.Name == "AGENTHUB_RUNTIME_PROXY_TARGET" {
			target = item.Value
		}
	}
	if target != "http://127.0.0.1:7860" {
		t.Errorf("the proxy points at %q, not at Langflow's port", target)
	}
	// The probe has to be checkable. A TCP probe from the kubelet cannot reach
	// 127.0.0.1 inside the container, so a loopback runtime with the default probe
	// would never become Ready — and a Pod that is never Ready is a runtime no task
	// can acquire and no person can open.
	if agent.ReadinessProbe == nil || agent.ReadinessProbe.Exec == nil {
		t.Fatalf("a loopback runtime needs an in-container readiness probe: %#v", agent.ReadinessProbe)
	}
	if agent.ReadinessProbe.TCPSocket != nil || agent.ReadinessProbe.HTTPGet != nil {
		t.Error("the readiness probe must not be reached from outside the container")
	}
	if !strings.Contains(strings.Join(agent.ReadinessProbe.Exec.Command, " "), "127.0.0.1:7860/health") {
		t.Errorf("the probe does not ask Langflow's health endpoint: %v", agent.ReadinessProbe.Exec.Command)
	}
	if agent.LivenessProbe == nil || agent.LivenessProbe.Exec == nil {
		t.Fatal("the liveness probe must be checked the same way")
	}
	// First start builds the component index and migrates the database; a short
	// grace would restart it in a loop and never finish.
	if grace := agent.ReadinessProbe.InitialDelaySeconds + agent.ReadinessProbe.PeriodSeconds*agent.ReadinessProbe.FailureThreshold; grace < 180 {
		t.Errorf("only %ds of readiness grace for a cold Langflow start", grace)
	}

	// The initialiser exists so that a runtime with no configuration file still
	// reports what it started with.
	found := false
	for _, container := range statefulSet.Spec.Template.Spec.InitContainers {
		if container.Name == "langflow-config-init" {
			found = strings.Contains(strings.Join(container.Args, " "), "agenthub-report-config")
		}
	}
	if !found {
		t.Error("Langflow's initialiser must report the configuration it started with")
	}
}

// The names of the variables an administrator declared travel to the Pod so its
// report can say which of them arrived. The values never do.
func TestDeclaredEnvNamesAreSortedNamesOnly(t *testing.T) {
	variables := []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{
		{Name: "TZ", Value: "Asia/Seoul"},
		{Name: "", Value: "ignored"},
		{Name: "LANGFLOW_LOG_LEVEL", Value: "info"},
	}
	got := declaredEnvNames(variables)
	if got != "LANGFLOW_LOG_LEVEL,TZ" {
		t.Fatalf("declaredEnvNames() = %q", got)
	}
	if strings.Contains(got, "Asia/Seoul") || strings.Contains(got, "info") {
		t.Fatal("a value leaked into the reported names")
	}
}
