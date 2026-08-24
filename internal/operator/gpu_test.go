package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// A runtime profile can ask for GPUs. Asking was all it did: the number was
// stored, carried in the CRD and parsed by this controller, and the Pod was
// scheduled without one — so an agent that needed a GPU ran on the CPU and
// looked like it was working.
func TestAProfileThatAsksForGPUsGetsThem(t *testing.T) {
	resources := func(count int64) corev1.ResourceRequirements {
		client := fake.NewSimpleClientset()
		controller := &Controller{client: client}
		owner := &unstructured.Unstructured{}
		owner.SetAPIVersion("agenthub.io/v1alpha1")
		owner.SetKind("AgentRuntime")
		owner.SetName("gpu-runtime")
		owner.SetNamespace("agent-runtime-dev")
		owner.SetUID(types.UID("test-owner"))
		var value spec
		value.Owner = "user-1"
		value.Runtime.Type = "opencode"
		value.Runtime.Image = "agenthub-base:v0.3.1"
		value.Profile.CPUMillis, value.Profile.MemoryMB = 2000, 4096
		value.Profile.GPUCount = count
		if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "gpu-runtime", "gpu-workspace", value, owner); err != nil {
			t.Fatal(err)
		}
		set, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "gpu-runtime", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return set.Spec.Template.Spec.Containers[0].Resources
	}

	asked := resources(2)
	quantity, ok := asked.Limits[gpuResource]
	if !ok {
		t.Fatalf("a profile asking for 2 GPUs produced a Pod with none: %v", asked.Limits)
	}
	if quantity.Value() != 2 {
		t.Errorf("the Pod asks for %d GPUs", quantity.Value())
	}

	// And a profile that asks for none must not carry the resource at all: an
	// empty GPU limit makes a Pod unschedulable on a cluster that has no GPUs.
	none := resources(0)
	if _, ok := none.Limits[gpuResource]; ok {
		t.Errorf("a profile with no GPUs still asked for the resource: %v", none.Limits)
	}
	// CPU and memory are unchanged by any of this.
	if none.Limits.Cpu().MilliValue() != 2000 || none.Limits.Memory().Value() != 4096*1024*1024 {
		t.Errorf("the other limits moved: %v", none.Limits)
	}
}
