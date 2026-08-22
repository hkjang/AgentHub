package operator

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"errors"

	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// A workspace volume was created once and never looked at again. Raising the
// storage on a runtime profile, or on a workspace, changed the number on the
// screen and nothing in the cluster: the setting saved, the volume stayed the
// size it was provisioned at, and whoever asked for more space found out when
// something ran out of it.
func TestAWorkspaceThatWasMadeBiggerGrows(t *testing.T) {
	for _, want := range []struct {
		name             string
		current, request string
		expect           string
	}{
		{"grown when the profile asks for more", "10Gi", "20", "20Gi"},
		{"left alone when nothing changed", "10Gi", "10", "10Gi"},
		// Kubernetes does not shrink a volume, and asking it to produces an error
		// about the request rather than about the intention.
		{"never shrunk", "20Gi", "10", "20Gi"},
		// A spec that names no size is not a request for zero.
		{"unspecified size changes nothing", "10Gi", "0", "10Gi"},
	} {
		t.Run(want.name, func(t *testing.T) {
			claim := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "runtimes"},
				Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: apiresource.MustParse(want.current)},
				}},
			}
			client := fake.NewSimpleClientset(claim)
			controller := &Controller{client: client, logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
			var value spec
			value.Workspace.SizeGB = parseSize(t, want.request)
			if err := controller.growPVC(context.Background(), "runtimes", claim, value); err != nil {
				t.Fatal(err)
			}
			after, err := client.CoreV1().PersistentVolumeClaims("runtimes").Get(context.Background(), "workspace", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := after.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != want.expect {
				t.Errorf("storage = %s, want %s", got.String(), want.expect)
			}
		})
	}
}

// A cluster whose storage class cannot resize refuses, and that is not a reason
// to fail the reconcile: the runtime runs, on the volume it has. Verified against
// a real API server too — minikube's hostpath class answers "only dynamically
// provisioned pvc can be resized and the storageclass that provisions the pvc
// must support resize", which is the sentence the platform passes on.
func TestARefusedExpansionDoesNotFailTheReconcile(t *testing.T) {
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "runtimes"},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: apiresource.MustParse("10Gi")},
		}},
	}
	client := fake.NewSimpleClientset(claim)
	client.PrependReactor("patch", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("only dynamically provisioned pvc can be resized")
	})
	controller := &Controller{client: client, logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	var value spec
	value.Workspace.SizeGB = 20
	if err := controller.growPVC(context.Background(), "runtimes", claim, value); err != nil {
		t.Errorf("a cluster that will not resize must not fail the reconcile: %v", err)
	}
}

func parseSize(t *testing.T, value string) int64 {
	t.Helper()
	quantity := apiresource.MustParse(value)
	return quantity.Value()
}
