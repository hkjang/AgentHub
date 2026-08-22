package operator

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The same decision against a real API server, because a fake client accepts an
// expansion no cluster would.
//
// What a cluster does with the request is not this platform's to decide: a
// storage class that cannot resize refuses, and the refusal names that as the
// reason. This checks that the platform asks, and that being told no leaves the
// runtime running on the volume it has rather than failing the reconcile.
//
//	kubectl proxy --port=8011 &
//	AGENTHUB_TEST_APISERVER=http://127.0.0.1:8011 go test ./internal/operator/ -run LivePVC -v
func TestLivePVCGrowthAsksTheClusterAndAcceptsItsAnswer(t *testing.T) {
	apiServer := os.Getenv("AGENTHUB_TEST_APISERVER")
	if apiServer == "" {
		t.Skip("set AGENTHUB_TEST_APISERVER to check this against a real API server")
	}
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host: apiServer, TLSClientConfig: rest.TLSClientConfig{Insecure: true}, Timeout: 30 * time.Second,
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

	name := "agenthub-growprobe"
	_ = client.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	claim, err := client.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: apiresource.MustParse("1Gi")},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("the cluster would not take the probe volume: %v", err)
	}
	defer func() {
		_ = client.CoreV1().PersistentVolumeClaims(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	}()

	var log bytes.Buffer
	controller := &Controller{client: client, logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	var value spec
	value.Workspace.SizeGB = 2
	if err := controller.growPVC(ctx, namespace, claim, value); err != nil {
		t.Errorf("being refused an expansion must not fail the reconcile: %v", err)
	}
	written := strings.TrimSpace(log.String())
	t.Logf("what the platform said: %s", written)
	// One of the two is true of any cluster, and both are the platform saying what
	// happened rather than nothing.
	if !strings.Contains(written, "workspace volume grown") && !strings.Contains(written, "could not be grown") {
		t.Errorf("the platform neither grew the volume nor said why not: %q", written)
	}
	// The cluster has several legitimate ways to say no, and which one arrives
	// depends on the claim: a storage class that cannot resize says so, an unbound
	// claim says its spec is immutable until it binds. Asserting one of them was
	// asserting the wrong thing — what matters is that the platform passes on
	// whatever the cluster said instead of swallowing it.
	if strings.Contains(written, "could not be grown") && !strings.Contains(written, "error=") {
		t.Errorf("the refusal does not carry the cluster's own reason: %q", written)
	}
}
