package runtime

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/runtimecfg"
	"github.com/hkjang/AgentHub/internal/runtimeenv"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Everything the control plane writes into an AgentRuntime has to survive the
// CRD, and a field that does not survive says nothing about it.
//
// The API server prunes a field the schema does not declare, silently, on a write
// that otherwise succeeds. The credential fingerprint was exactly this: the
// control plane wrote it, the object came back without it, and every credential
// rotation went on being ignored with no error anywhere. That was found by
// reading the code. This finds the next one by asking the cluster.
//
// It reaches the API server the way this platform does — a host and a token —
// rather than through a kubeconfig, so it needs no dependency the product does not
// already have. `kubectl proxy` is the easiest host to give it:
//
//	kubectl proxy --port=8011 &
//	AGENTHUB_TEST_APISERVER=http://127.0.0.1:8011 go test ./internal/runtime/ -run CRDKeeps -v
func TestCRDKeepsEverythingTheControlPlaneWrites(t *testing.T) {
	apiServer := os.Getenv("AGENTHUB_TEST_APISERVER")
	if apiServer == "" {
		t.Skip("set AGENTHUB_TEST_APISERVER to check the CRD against a real API server")
	}
	config := &rest.Config{
		Host:            apiServer,
		BearerToken:     os.Getenv("AGENTHUB_TEST_APISERVER_TOKEN"),
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
		Timeout:         30 * time.Second,
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	namespace := os.Getenv("AGENTHUB_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	sent := (&KubernetesSpawner{}).object(aFullSpec())
	sent.SetNamespace(namespace)
	resource := client.Resource(runtimeGVR).Namespace(namespace)
	_ = resource.Delete(ctx, sent.GetName(), metav1.DeleteOptions{})
	stored, err := resource.Create(ctx, sent, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("the cluster refused the object this platform writes: %v", err)
	}
	defer func() { _ = resource.Delete(context.Background(), sent.GetName(), metav1.DeleteOptions{}) }()

	var missing []string
	comparePaths(sent.Object["spec"], stored.Object["spec"], "spec", &missing)
	sort.Strings(missing)
	for _, path := range missing {
		t.Errorf("%s is written by the control plane and pruned by the CRD; whatever it configures is silently ignored", path)
	}
	if len(missing) == 0 {
		t.Logf("every field the control plane writes survived the CRD")
	}
}

// comparePaths walks what was sent and records the leaves that did not come back.
// Only absence is a finding: the API server may add defaults, and a value it
// normalised is still a value it kept.
func comparePaths(sent, stored any, path string, missing *[]string) {
	switch value := sent.(type) {
	case map[string]any:
		storedMap, ok := stored.(map[string]any)
		if !ok {
			*missing = append(*missing, path)
			return
		}
		for key, child := range value {
			next, found := storedMap[key]
			if !found {
				*missing = append(*missing, path+"."+key)
				continue
			}
			comparePaths(child, next, path+"."+key, missing)
		}
	case []any:
		storedList, ok := stored.([]any)
		if !ok || len(storedList) != len(value) {
			*missing = append(*missing, path)
			return
		}
		for index, child := range value {
			comparePaths(child, storedList[index], fmt.Sprintf("%s[%d]", path, index), missing)
		}
	}
}

// aFullSpec fills in everything the platform can put in an AgentRuntime, because
// a field only present on some runtimes is exactly the one nobody notices is
// being dropped.
func aFullSpec() Spec {
	spec := Spec{
		Image:                          "example/opencode:v1",
		HostNetwork:                    true,
		WorkspaceType:                  "git",
		WorkspacePVC:                   "crdprobe-workspace",
		WorkspaceSizeGB:                7,
		WorkspaceRepositoryURL:         "https://example.invalid/repo.git",
		WorkspaceBranch:                "main",
		WorkspaceSnapshot:              "crdprobe-snapshot",
		WorkspaceGitCredentialKind:     "token",
		WorkspaceGitCredentialUsername: "git",
		WorkspaceGitCredential:         "secret-token",
		ModelBaseURL:                   "https://api.example.invalid/v1",
		ModelName:                      "a-model",
		ModelAPIKey:                    "sk-probe",
		MCPServers: []MCPBinding{{
			Name: "github", Endpoint: "https://mcp.example.invalid", Mode: "shared",
			AuthType: "bearer", AuthHeader: "Authorization", Credential: "ghp-probe", Port: 8931,
		}},
	}
	spec.Agent.ID, spec.Agent.OwnerID, spec.Agent.RuntimeType, spec.Agent.Version = "crdprobe-agent", "crdprobe-owner", "opencode", 3
	spec.Runtime.ID, spec.Runtime.CRDName = "crdprobe-runtime", "crdprobe"
	spec.Profile.CPUMillis, spec.Profile.MemoryMB, spec.Profile.StorageGB = 2000, 4096, 10
	spec.Profile.GPUCount, spec.Profile.IdleTimeoutSeconds = 1, 1800
	spec.Security.RunAsNonRoot, spec.Security.ReadOnlyRootFilesystem = true, true
	spec.Security.AllowPrivilegeEscalation, spec.Security.AutomountServiceAccountToken = false, false
	spec.Security.SeccompProfile, spec.Security.ClusterRead = "RuntimeDefault", true
	spec.Network.DefaultDeny, spec.Network.AllowDNS = true, true
	spec.Network.AllowedDestinations = []string{"api.example.invalid:443"}
	// The blocks that are left off entirely when unconfigured. These are where a
	// pruned field hides best: a site that never sets one would never see the
	// difference, and the site that does see nothing happen.
	spec.ProvisionedFiles = []runtimeenv.File{{Path: "/etc/agenthub/site.conf", Content: "key = value", Mode: "0644"}}
	spec.ProvisionedVariables = []runtimeenv.Variable{{Name: "SITE", Value: "probe"}}
	spec.DLP = dlp.Settings{Enabled: true, ScanResponses: true, MaxBytes: 65536,
		Classes: map[string]string{"email": "redact", "api_key": "block"}}
	spec.RuntimeSettings = runtimecfg.Profile{
		Config: map[string]any{"theme": "dark", "nested": map[string]any{"depth": int64(2)}},
		Env:    map[string]string{"OPENCODE_FLAG": "1"},
	}
	return spec
}

var _ = unstructured.Unstructured{}
