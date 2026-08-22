package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Rotating a credential has to reach a running runtime.
//
// The Secret is updated in place — a new model key, a changed MCP credential —
// and nothing about the Pod changes with it. The values arrive as environment
// variables and files the agent process read once at startup, so the platform
// reported the rotation done while every running runtime went on using the
// credential that had just been revoked. The new one took effect whenever the Pod
// next happened to restart, which might be never.
func TestRotatingACredentialChangesTheFingerprint(t *testing.T) {
	base := Spec{ModelAPIKey: "sk-old", MCPServers: []MCPBinding{{Name: "github", AuthType: "bearer", Credential: "ghp-old"}}}
	rotatedModel := base
	rotatedModel.ModelAPIKey = "sk-new"
	rotatedMCP := base
	rotatedMCP.MCPServers = []MCPBinding{{Name: "github", AuthType: "bearer", Credential: "ghp-new"}}
	unbound := base
	unbound.MCPServers = nil
	sameAgain := Spec{ModelAPIKey: "sk-old", MCPServers: []MCPBinding{{Name: "github", AuthType: "bearer", Credential: "ghp-old"}}}

	start := credentialFingerprint(base)
	if start == "" {
		t.Fatal("no fingerprint at all; the Pod would never notice a rotation")
	}
	for _, change := range []struct {
		what string
		spec Spec
	}{
		{"the model key was rotated", rotatedModel},
		{"an MCP credential was rotated", rotatedMCP},
		{"an MCP server was unbound", unbound},
	} {
		if credentialFingerprint(change.spec) == start {
			t.Errorf("%s and the fingerprint did not change; the running Pod keeps the old credential", change.what)
		}
	}
	if credentialFingerprint(sameAgain) != start {
		t.Error("the same credentials fingerprint differently; every reconcile would roll the Pod")
	}
	// The fingerprint is what crosses into the CRD; the credentials must not.
	for _, secret := range []string{"sk-old", "ghp-old"} {
		if strings.Contains(start, secret) {
			t.Errorf("the fingerprint contains %q; credentials belong in the Secret and nowhere else", secret)
		}
	}
}

// And the whole path has to be intact: the control plane puts it in the object,
// the CRD schema allows it through, and the operator folds it into the hash that
// decides whether the Pod rolls. A break anywhere in that chain is silent — the
// API server prunes an undeclared field without complaint.
func TestTheFingerprintSurvivesTheWholePath(t *testing.T) {
	for _, step := range []struct{ file, needle, why string }{
		{"kubernetes.go", `"credentialsFingerprint": credentialFingerprint(spec)`, "the control plane does not put the fingerprint in the AgentRuntime object"},
		{filepath.Join("..", "operator", "controller.go"), `CredentialsFingerprint string`, "the operator does not read the fingerprint out of the spec"},
		{filepath.Join("..", "operator", "controller.go"), "value.Model.CredentialsFingerprint", "the operator reads the fingerprint and does not fold it into the config hash, so nothing rolls"},
		{filepath.Join("..", "..", "deploy", "kubernetes", "crd.yaml"), "credentialsFingerprint:", "the CRD schema does not declare the field; the API server prunes it before the operator sees it"},
	} {
		body, err := os.ReadFile(step.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), step.needle) {
			t.Error(step.why)
		}
	}
}

// A cluster whose CRD predates this field drops it on the way in — the API
// server prunes an undeclared field without a word, so the control plane writes
// the fingerprint, the object comes back without it, and every rotation goes on
// being ignored exactly as before. That is worth saying out loud, because the
// symptom is a rotation that appears to have worked.
func TestAPrunedFingerprintIsNoticed(t *testing.T) {
	spec := Spec{ModelAPIKey: "sk-live"}
	kept := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"model": map[string]any{"credentialsFingerprint": credentialFingerprint(spec)}},
	}}
	dropped := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"model": map[string]any{"secretRef": "rt"}},
	}}
	emptied := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"model": map[string]any{"credentialsFingerprint": ""}},
	}}
	if credentialFingerprintPruned(spec, kept) {
		t.Error("a cluster that kept the fingerprint is reported as having dropped it")
	}
	if !credentialFingerprintPruned(spec, dropped) {
		t.Error("a cluster that pruned the field is not noticed; rotations would silently keep failing")
	}
	if !credentialFingerprintPruned(spec, emptied) {
		t.Error("a field present but empty is the same as absent and has to be noticed too")
	}
	if credentialFingerprintPruned(spec, nil) {
		t.Error("nothing was stored, so nothing was pruned")
	}
}
