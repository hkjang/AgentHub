package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The execution fabric runs beside the terminal rather than inside it, so a
// person closing their shell does not take the workers with it. It had every
// mount except the repository, and refused every task with "Not a valid git
// repository: /workspace" — its own words, correctly reported, about a directory
// that really was empty, one mount away from the one holding the work.
//
// Found by running an orca task on a real cluster.
func TestTheFabricSidecarCanSeeTheWorkspace(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("orca-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))

	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = runtimetype.Orca
	value.Runtime.Image = "agenthub-orca:v0.3.0"
	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "orca-runtime", "orca-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	set, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "orca-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, container := range set.Spec.Template.Spec.Containers {
		if container.Name != "orca-runtime" {
			continue
		}
		found = true
		if !mountsWorkspace(container) {
			t.Errorf("the fabric runs without the repository it is meant to work in: %v", container.VolumeMounts)
		}
	}
	if !found {
		t.Fatal("the fabric sidecar is gone; this guard is reading nothing")
	}

	// The proxy is a different job: it publishes a port and has no business in
	// somebody's repository.
	for _, container := range set.Spec.Template.Spec.Containers {
		if container.Name == "orca-proxy" && mountsWorkspace(container) {
			t.Error("the proxy sidecar was given the workspace it does not need")
		}
	}
}

func mountsWorkspace(container corev1.Container) bool {
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == "/workspace" {
			return true
		}
	}
	return false
}
