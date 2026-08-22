package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// The operator mints a runtime-token when the Secret is missing, and it has to:
// a CRD applied without AgentHub's control plane still needs one, and the
// runtime's own server password is that token.
//
// This is here as the premise of the fix on the other side. The token the
// operator makes is shown to nobody, so the control plane's stored hash is not
// the one the Pod holds and every request from that Pod's gateway is answered
// 401 — a runtime that looks healthy with its approval gate quietly off. The
// control plane reads the token back out of the Secret because of what this test
// proves happens; if the minting ever goes away, that reading is dead code and
// somebody should know.
func TestTheOperatorMintsATokenTheControlPlaneNeverSaw(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("token-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))

	if err := controller.ensureSecret(context.Background(), "agent-runtime-dev", "token-runtime", owner); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets("agent-runtime-dev").Get(context.Background(), "token-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the operator did not create the Secret at all: %v", err)
	}
	minted := string(secret.Data["runtime-token"]) + string(secret.StringData["runtime-token"])
	if minted == "" {
		t.Fatal("the operator created the Secret without a runtime-token; the Pod has no server password and no way to reach the control plane")
	}

	// Reconciling again must not mint a second one: a token that changes under a
	// running Pod is a Pod whose credential stops working where it stands.
	if err := controller.ensureSecret(context.Background(), "agent-runtime-dev", "token-runtime", owner); err != nil {
		t.Fatal(err)
	}
	again, err := client.CoreV1().Secrets("agent-runtime-dev").Get(context.Background(), "token-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second := string(again.Data["runtime-token"]) + string(again.StringData["runtime-token"]); second != minted {
		t.Error("the operator replaced the token on a reconcile; the running Pod's credential just stopped working")
	}
}
