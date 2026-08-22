package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// The report itself, against a real API server: write the object this platform
// writes, read back what the cluster kept, and see whether the platform says what
// went missing. A warning nobody can trigger is not a warning.
//
//	kubectl proxy --port=8011 &
//	AGENTHUB_TEST_APISERVER=http://127.0.0.1:8011 go test ./internal/runtime/ -run PrunedReport -v
func TestPrunedReportSaysWhatARealClusterDropped(t *testing.T) {
	apiServer := os.Getenv("AGENTHUB_TEST_APISERVER")
	if apiServer == "" {
		t.Skip("set AGENTHUB_TEST_APISERVER to check this against a real API server")
	}
	client, err := dynamic.NewForConfig(&rest.Config{Host: apiServer, TLSClientConfig: rest.TLSClientConfig{Insecure: true}, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	namespace := os.Getenv("AGENTHUB_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	spec := aFullSpec()
	sent := (&KubernetesSpawner{}).object(spec)
	// A field no CRD has ever declared stands in for the next one somebody adds
	// without touching crd.yaml.
	sent.Object["spec"].(map[string]any)["somethingNew"] = "a setting the schema has never heard of"
	sent.SetNamespace(namespace)
	resource := client.Resource(runtimeGVR).Namespace(namespace)
	_ = resource.Delete(ctx, sent.GetName(), metav1.DeleteOptions{})
	stored, err := resource.Create(ctx, sent, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("the cluster refused the object: %v", err)
	}
	defer func() { _ = resource.Delete(context.Background(), sent.GetName(), metav1.DeleteOptions{}) }()

	var log bytes.Buffer
	spawner := (&KubernetesSpawner{}).WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))
	spawner.reportPruned(spec, sent, stored)
	written := log.String()
	t.Logf("what the platform said: %s", strings.TrimSpace(written))
	if !strings.Contains(written, "spec.somethingNew") {
		t.Error("the cluster dropped a field and the platform said nothing about it")
	}
	if !strings.Contains(written, "crd.yaml") {
		t.Error("the warning does not say what to do about it")
	}
}
