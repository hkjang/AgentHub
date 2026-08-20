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

func flowRuntimeOwner(name string) *unstructured.Unstructured {
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName(name)
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	return owner
}

// Both of these publish an editor that can deploy code, and neither is started
// with an authenticator of its own: the proxy is the only thing between a browser
// and a flow that runs. They also both have to be told the path they are served
// under, because their own UIs address themselves from it.
func TestFlowRuntimesAreBoundToLoopbackUnderTheirOwnPath(t *testing.T) {
	cases := []struct {
		runtimeType string
		container   string
		proxyName   string
		upstream    string
		wantInSpec  string
	}{
		{runtimetype.NodeRED, "agent", "nodered-proxy", "http://127.0.0.1:1880", "--userDir /home/agent/.node-red"},
		{runtimetype.N8N, "agent", "n8n-proxy", "http://127.0.0.1:5678", "n8n start"},
	}
	for _, item := range cases {
		t.Run(item.runtimeType, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			controller := &Controller{client: client}
			owner := flowRuntimeOwner(item.runtimeType + "-runtime")
			var value spec
			value.Owner = "user-1"
			value.Runtime.Type = item.runtimeType
			value.Runtime.Image = "agenthub-" + item.runtimeType + ":v0.1.0"
			value.Runtime.SidecarImage = "agenthub:v0.23.0"
			value.RuntimeRef.ID = "rt-7c1e"
			value.Security.ReadOnlyRootFilesystem = true

			if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", item.runtimeType+"-runtime", "ws", value, owner); err != nil {
				t.Fatal(err)
			}
			statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), item.runtimeType+"-runtime", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var agent, proxy *corev1.Container
			for index, container := range statefulSet.Spec.Template.Spec.Containers {
				switch container.Name {
				case item.container:
					agent = &statefulSet.Spec.Template.Spec.Containers[index]
				case item.proxyName:
					proxy = &statefulSet.Spec.Template.Spec.Containers[index]
				}
			}
			if agent == nil || !strings.Contains(strings.Join(agent.Args, " "), item.wantInSpec) {
				t.Fatalf("the agent container does not start the product: %#v", agent)
			}
			if proxy == nil {
				t.Fatalf("%s must be published through the authenticated proxy", item.runtimeType)
			}
			target := ""
			for _, env := range proxy.Env {
				if env.Name == "AGENTHUB_RUNTIME_PROXY_TARGET" {
					target = env.Value
				}
			}
			if target != item.upstream {
				t.Errorf("the proxy points at %q, not at the product", target)
			}
			// Loopback, so the probe has to be asked from inside the container.
			if agent.ReadinessProbe == nil || agent.ReadinessProbe.Exec == nil {
				t.Fatalf("a loopback runtime needs an in-container readiness probe: %#v", agent.ReadinessProbe)
			}
			// A runtime served under its own path must be probed there, and the
			// gateway has to agree — otherwise it strips a prefix the product still
			// expects, or keeps one it cannot handle.
			probe := strings.Join(agent.ReadinessProbe.Exec.Command, " ")
			if runtimetype.ServesUnderRuntimePath(item.runtimeType) != strings.Contains(probe, "rt-7c1e") {
				t.Errorf("the probe and the gateway disagree about the base path: %q", probe)
			}
		})
	}
}

// Node-RED is served under the runtime's own path and learns it from a settings
// file the platform generates.
//
// n8n is not, and must not be given one: with its base-path setting on, its
// assets and its REST API fall through to the index page and the editor never
// starts. It gets an origin of its own instead, which is a fact the runtime type
// has to agree with or the gateway would publish it where it cannot work.
func TestFlowRuntimesLearnTheirBasePath(t *testing.T) {
	build := adapterBuild{Name: "rt", Value: spec{}}
	build.Value.RuntimeRef.ID = "rt-7c1e"
	build.Value.Runtime.Type = runtimetype.N8N
	env := map[string]string{}
	for _, item := range adapterFor(runtimetype.N8N).Env(build) {
		env[item.Name] = item.Value
	}
	if _, found := env["N8N_PATH"]; found {
		t.Errorf("n8n must not be given a base path: %q", env["N8N_PATH"])
	}
	if runtimetype.ServesUnderRuntimePath(runtimetype.N8N) || !runtimetype.HostSessionOnly(runtimetype.N8N) {
		t.Error("n8n needs an origin of its own, and the runtime type has to say so")
	}
	if env["N8N_LISTEN_ADDRESS"] != "127.0.0.1" {
		t.Errorf("n8n must listen on loopback, got %q", env["N8N_LISTEN_ADDRESS"])
	}

	var value spec
	value.RuntimeRef.ID = "rt-7c1e"
	settings := nodeREDSettings(value)
	body := settings[strings.Index(settings, "{"):]
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(body), "")), &parsed); err != nil {
		t.Fatalf("the generated settings are not readable: %v\n%s", err, settings)
	}
	if parsed["httpAdminRoot"] != "/rt-7c1e/" {
		t.Errorf("httpAdminRoot = %v", parsed["httpAdminRoot"])
	}
	if parsed["uiHost"] != "127.0.0.1" {
		t.Errorf("Node-RED must listen on loopback, got %v", parsed["uiHost"])
	}
	if parsed["userDir"] != nodeREDUserDir {
		t.Errorf("userDir = %v, want the home volume", parsed["userDir"])
	}
}
