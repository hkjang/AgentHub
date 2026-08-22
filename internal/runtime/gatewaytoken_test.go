package runtime

import (
	"os"
	"strings"
	"testing"
)

// The control plane has to recognise the token the Pod is actually holding.
//
// Two processes can mint it. The control plane makes one when it creates the
// Secret and stores its hash; the operator makes one when it reconciles a
// runtime whose Secret is missing, and shows it to nobody. A Pod that boots with
// the operator's token has every gateway request answered 401 — no tool approval
// can be asked for, no content-scanner finding reported, no configuration report
// delivered — and the runtime looks healthy while its approval gate is off.
//
// ensureSecret reads the token out of the Secret and stores that, which settles
// the disagreement whichever way it went. This is a source guard because the
// spawner talks to a real database and the check is one statement in a long
// function; the operator half — that it does mint one — is checked against a
// client in internal/operator.
func TestTheControlPlaneAdoptsTheTokenInTheSecret(t *testing.T) {
	body, err := os.ReadFile("kubernetes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (k *KubernetesSpawner) ensureSecret(")
	if at < 0 {
		t.Fatal("ensureSecret is gone; this guard is reading nothing")
	}
	block := source[at:]
	if end := strings.Index(block, "\nfunc "); end >= 0 {
		block = block[:end]
	}
	if !strings.Contains(block, `existing.Data["runtime-token"]`) {
		t.Error("ensureSecret does not read the token out of the Secret; a Secret the operator created leaves the gateway unable to authenticate for good")
	}
	if !strings.Contains(block, "SetRuntimeGatewayToken(") {
		t.Error("ensureSecret does not store the token it found, so reading it changes nothing")
	}
}

// A runtime that has been deleted is not a runtime, and its token is not a
// credential. The session lookup has always had the equivalent rule — it refuses
// a token whose user is no longer active — and this one had nothing.
func TestADeletedRuntimesTokenIsRefusedAndCleared(t *testing.T) {
	lookup, err := os.ReadFile("../store/toolapproval.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lookup), `WHERE gateway_token_hash=$1 AND desired_state<>'deleted'`) {
		t.Error("the gateway token lookup accepts a deleted runtime's token")
	}
	lifecycle, err := os.ReadFile("../store/runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lifecycle), `gateway_token_hash=CASE WHEN $1='deleted' THEN NULL ELSE gateway_token_hash END`) {
		t.Error("deleting a runtime leaves its gateway token on file; a credential that still exists is one somebody can still present")
	}
}
