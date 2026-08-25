package runtime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/hkjang/AgentHub/internal/runtimeenv"
	"github.com/hkjang/AgentHub/internal/store"
)

func TestRuntimeObjectReferencesSecretWithoutEmbeddingIt(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime:      store.Runtime{CRDName: "agent-user-agent"},
		Agent:        store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode", Version: 3},
		Profile:      store.RuntimeProfile{CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10},
		ModelBaseURL: "https://model.example/v1",
		ModelName:    "qwen-coder",
		ModelAPIKey:  "must-never-appear-in-crd",
		MCPServers:   []MCPBinding{{Name: "jira", Mode: "shared", Endpoint: "https://mcp.example/mcp"}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "must-never-appear-in-crd") {
		t.Fatal("model API key leaked into AgentRuntime CRD")
	}
	if !strings.Contains(text, `"secretRef":"agent-user-agent"`) {
		t.Fatalf("CRD does not reference its Kubernetes Secret: %s", text)
	}
}

func TestHostNetworkSettingDefaultsOnAndPreservesAnExplicitChoice(t *testing.T) {
	if !(kubernetesSettings{}).hostNetworkEnabled() {
		t.Fatal("an upgraded installation without hostNetwork must default to enabled")
	}
	for _, enabled := range []bool{false, true} {
		value := enabled
		if got := (kubernetesSettings{HostNetwork: &value}).hostNetworkEnabled(); got != enabled {
			t.Fatalf("saved hostNetwork=%v resolved to %v", enabled, got)
		}
		object := (&KubernetesSpawner{}).object(Spec{
			Runtime:     store.Runtime{CRDName: "agent-user-agent"},
			Agent:       store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
			HostNetwork: enabled,
		})
		got, found, err := unstructured.NestedBool(object.Object, "spec", "runtime", "hostNetwork")
		if err != nil || !found || got != enabled {
			t.Fatalf("AgentRuntime hostNetwork = %v, found=%v, err=%v; want %v", got, found, err, enabled)
		}
	}
}

func TestLabelValue(t *testing.T) {
	if got := labelValue("USER_A/Very Long Value"); got != "user-a-very-long-value" {
		t.Fatalf("unexpected label %q", got)
	}
	if got := labelValue(strings.Repeat("a", 100)); len(got) != 63 {
		t.Fatalf("label length is %d, want 63", len(got))
	}
}

func TestSnapshotSupportErrorClassifiesMissingCRD(t *testing.T) {
	// A cluster without the CRD answers about the resource type, with no object to
	// name. This used to be written as NewNotFound(..., "snap-1"), which is how
	// Kubernetes reports one missing object — so the fixture said "this snapshot
	// is gone" while the assertion said "this cluster cannot do snapshots", and
	// the code satisfied both by conflating them. Restoring from a deleted
	// snapshot therefore told an operator to install a CRD that was already there.
	missing := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound,
		Message: "the server could not find the requested resource",
	}}
	if !errors.Is(snapshotSupportError(missing), ErrSnapshotsUnsupported) {
		t.Fatal("a missing VolumeSnapshot CRD must report snapshots as unsupported")
	}
	if snapshotSupportError(nil) != nil {
		t.Fatal("no error must stay no error")
	}
	other := apierrors.NewForbidden(schema.GroupResource{Resource: "volumesnapshots"}, "snap-1", errors.New("denied"))
	if errors.Is(snapshotSupportError(other), ErrSnapshotsUnsupported) {
		t.Fatal("a permission failure must not be reported as unsupported")
	}
}

// The platform-wide runtime environment is copied into the object, which is what
// makes it reach the Pod at all. A regression here looks exactly like the feature
// not existing: the setting saves, and nothing in the cluster changes.
func TestRuntimeObjectCarriesTheProvisionedEnvironment(t *testing.T) {
	spawner := &KubernetesSpawner{}
	object := spawner.object(Spec{
		Runtime: store.Runtime{CRDName: "agent-user-agent"},
		Agent:   store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
		ProvisionedFiles: []runtimeenv.File{{
			Path: "/etc/pip.conf", Content: "[global]\nindex-url = https://nexus.local/simple\n", Mode: "0644",
		}},
		ProvisionedVariables: []runtimeenv.Variable{{Name: "PIP_INDEX_URL", Value: "https://nexus.local/simple"}},
	})
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{`"provisioning"`, `/etc/pip.conf`, `nexus.local/simple`, `PIP_INDEX_URL`, `"mode":"0644"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("the object does not carry %s: %s", expected, text)
		}
	}
	// A deployment that configured nothing must keep producing the object it did
	// before, rather than an empty provisioning section.
	bare, err := json.Marshal(spawner.object(Spec{
		Runtime: store.Runtime{CRDName: "agent-user-agent"},
		Agent:   store.Agent{ID: "agent-id", OwnerID: "user-id", RuntimeType: "opencode"},
	}).Object)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "provisioning") {
		t.Fatalf("an unconfigured deployment must not gain a provisioning section: %s", bare)
	}
}

// Syncing pushes a setting to runtimes that already exist. It must not decide
// whether they run.
func TestSyncKeepsTheDesiredState(t *testing.T) {
	for _, state := range []string{"Running", "Stopped"} {
		existing := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"lifecycle": map[string]any{"desiredState": state, "autoRestart": true},
				"runtime":   map[string]any{"image": "old"},
			},
		}}
		fresh := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"lifecycle":    map[string]any{"desiredState": "Running", "autoRestart": true},
				"runtime":      map[string]any{"image": "new"},
				"provisioning": map[string]any{"files": []any{map[string]any{"path": "/etc/pip.conf"}}},
			},
		}}
		if err := syncSpec(existing, fresh); err != nil {
			t.Fatal(err)
		}
		got, _, _ := unstructured.NestedString(existing.Object, "spec", "lifecycle", "desiredState")
		if got != state {
			t.Fatalf("desired state became %q, want %q", got, state)
		}
		image, _, _ := unstructured.NestedString(existing.Object, "spec", "runtime", "image")
		if image != "new" {
			t.Fatalf("the rest of the spec was not refreshed: image=%q", image)
		}
		if _, found, _ := unstructured.NestedMap(existing.Object, "spec", "provisioning"); !found {
			t.Fatal("the provisioned environment did not reach the object")
		}
	}
	// An object with no lifecycle at all is replaced wholesale rather than
	// refused: it is not a shape this platform writes.
	existing := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	if err := syncSpec(existing, &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"runtime": map[string]any{"image": "new"}}}}); err != nil {
		t.Fatal(err)
	}
}
