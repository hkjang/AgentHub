package runtime

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// A runtime's object is written by two people and only one of them can lose.
//
// The control plane owns the spec — start, stop, restart, the platform-wide
// environment push — and the operator owns the status, which it rewrites every
// time a Pod changes phase. Kubernetes counts a status write as a change to the
// object, so a spec write carrying a version read a moment earlier is refused
// with "the object has been modified". The cost was the whole action: a setting
// that reported as failed for that runtime and never reached the Pod, or a start
// button that returned an error to the person who pressed it.
//
// This asks a real API server, because a fake client hands out its own versions
// and would agree with whatever the code does:
//
//	kubectl proxy --port=8013 &
//	AGENTHUB_TEST_APISERVER=http://127.0.0.1:8013 go test ./internal/runtime/ -run SpecWrite -v
func TestASpecWriteSurvivesTheOperatorWritingStatus(t *testing.T) {
	apiServer := os.Getenv("AGENTHUB_TEST_APISERVER")
	if apiServer == "" {
		t.Skip("set AGENTHUB_TEST_APISERVER to check spec writes against a real API server")
	}
	client, err := dynamic.NewForConfig(&rest.Config{
		Host:            apiServer,
		BearerToken:     os.Getenv("AGENTHUB_TEST_APISERVER_TOKEN"),
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
		Timeout:         30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	namespace := os.Getenv("AGENTHUB_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	object := (&KubernetesSpawner{}).object(aFullSpec())
	object.SetNamespace(namespace)
	resource := client.Resource(runtimeGVR).Namespace(namespace)
	_ = resource.Delete(ctx, object.GetName(), metav1.DeleteOptions{})
	created, err := resource.Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("the runtime object could not be created, so nothing was checked: %v", err)
	}
	defer func() { _ = resource.Delete(context.Background(), created.GetName(), metav1.DeleteOptions{}) }()

	// The operator writes status between the read and the write, which is the
	// collision — not a contrived one, since the environment push walks every
	// runtime at once and a person presses start while the phase is moving.
	operatorWrote := false
	stored, err := updateRuntimeObject(ctx, client, namespace, created.GetName(), func(object *unstructured.Unstructured) error {
		if !operatorWrote {
			operatorWrote = true
			fresh, getErr := resource.Get(ctx, created.GetName(), metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if setErr := unstructured.SetNestedMap(fresh.Object, map[string]any{"phase": "Running", "podName": "p-0"}, "status"); setErr != nil {
				return setErr
			}
			if _, statusErr := resource.UpdateStatus(ctx, fresh, metav1.UpdateOptions{}); statusErr != nil {
				return statusErr
			}
		}
		return unstructured.SetNestedField(object.Object, "Stopped", "spec", "lifecycle", "desiredState")
	})
	if err != nil {
		t.Fatalf("the spec write lost to the operator's status write: %v", err)
	}
	if !operatorWrote {
		t.Fatal("the operator never wrote status, so no collision was arranged and nothing was proved")
	}
	desired, _, _ := unstructured.NestedString(stored.Object, "spec", "lifecycle", "desiredState")
	if desired != "Stopped" {
		t.Fatalf("the spec write returned no error but did not take: desiredState is %q", desired)
	}
	after, err := resource.Get(ctx, created.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	desired, _, _ = unstructured.NestedString(after.Object, "spec", "lifecycle", "desiredState")
	phase, _, _ := unstructured.NestedString(after.Object, "status", "phase")
	if desired != "Stopped" {
		t.Errorf("the cluster does not have the spec the control plane wrote: desiredState is %q", desired)
	}
	if phase != "Running" {
		t.Errorf("retrying the spec write threw away the operator's status: phase is %q", phase)
	}
	t.Logf("spec written after a collision: desiredState=%s, operator's status kept: phase=%s", desired, phase)
}
