package runtime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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

func TestLabelValue(t *testing.T) {
	if got := labelValue("USER_A/Very Long Value"); got != "user-a-very-long-value" {
		t.Fatalf("unexpected label %q", got)
	}
	if got := labelValue(strings.Repeat("a", 100)); len(got) != 63 {
		t.Fatalf("label length is %d, want 63", len(got))
	}
}

func TestSnapshotSupportErrorClassifiesMissingCRD(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}, "snap-1")
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
