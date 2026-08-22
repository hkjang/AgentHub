package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One Secret, two writers. The operator owns its metadata and writes it on every
// reconcile; the control plane owns the credentials inside it and writes them on
// every spawn and every settings sync.
//
// Both used to read the object and send the whole thing back, so whichever wrote
// second was refused — "the object has been modified", confirmed against a real
// API server by having two readers of the same version write in turn. The loser
// was either a reconcile that failed and retried, or a person who had just pressed
// start and got a Kubernetes conflict in their face.
//
// Each patches what it owns now. A patch carries no resource version, so there is
// nothing for the two of them to disagree about.
func TestNeitherWriterSendsTheWholeSecretBack(t *testing.T) {
	for _, writer := range []struct{ file, function, owns string }{
		{"kubernetes.go", "func (k *KubernetesSpawner) ensureSecret(", "the credentials"},
		{filepath.Join("..", "operator", "controller.go"), "func (c *Controller) ensureSecret(", "the metadata"},
	} {
		body, err := os.ReadFile(writer.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		at := strings.Index(source, writer.function)
		if at < 0 {
			t.Fatalf("%s is gone; this guard is reading nothing", writer.function)
		}
		fn := source[at:]
		if end := strings.Index(fn, "\n}\n"); end >= 0 {
			fn = fn[:end]
		}
		if strings.Contains(fn, "Secrets(") && strings.Contains(fn, ").Update(") {
			t.Errorf("%s writes the whole Secret back; the other writer of the same object loses on the next collision", writer.function)
		}
		if !strings.Contains(fn, "MergePatchType") {
			t.Errorf("%s does not patch %s it owns", writer.function, writer.owns)
		}
	}
}

// Unbinding an MCP server has to revoke its credential, and a merge patch removes
// a key by setting it to null rather than by omitting it. Omitting it would leave
// the credential in the Pod's Secret after the server it belonged to was detached.
func TestARevokedCredentialIsSetToNullRatherThanOmitted(t *testing.T) {
	body, err := os.ReadFile("kubernetes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (k *KubernetesSpawner) ensureSecret(")
	fn := source[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "data[key] = nil") {
		t.Error("a credential that is no longer wanted is not set to null; a merge patch that omits a key leaves it in the Secret")
	}
	if !strings.Contains(fn, "mcp-credential-") || !strings.Contains(fn, "gitCredentialKey") {
		t.Error("the keys a detached server leaves behind are no longer cleared")
	}
}
