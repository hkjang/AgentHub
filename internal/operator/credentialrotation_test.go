package operator

import (
	"encoding/json"
	"testing"
)

// The operator's own decision, over two specs that differ only in which
// credentials the runtime is meant to be using.
func TestCredentialRotationRollsThePod(t *testing.T) {
	parse := func(fingerprint string) spec {
		raw := `{"runtime":{"type":"opencode","image":"x"},"model":{"baseUrl":"https://api","name":"m","secretRef":"rt","credentialsFingerprint":"` + fingerprint + `"}}`
		var value spec
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := configHash("ns", "rt", parse("aaaa"))
	after := configHash("ns", "rt", parse("bbbb"))
	again := configHash("ns", "rt", parse("aaaa"))
	t.Logf("before rotation: %s", before[:16])
	t.Logf("after rotation:  %s", after[:16])
	if before == after {
		t.Error("rotating a credential leaves the Pod template identical; the running Pod keeps the old one")
	}
	if before != again {
		t.Error("the same credentials hash differently each time; every reconcile would roll the Pod")
	}
}
