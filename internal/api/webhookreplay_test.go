package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A webhook signature proves the sender knew the secret. It does not prove this
// is the first time the request has been sent.
//
// The signature is a function of the body, so a captured request stays valid for
// ever and each replay of it queued another task — anybody who saw one request,
// in a proxy log or a CI transcript or a shared curl command, could fire that
// agent again whenever they liked, and the audit trail would show a perfectly
// valid webhook every time.
func TestAWebhookDeliveryIsClaimedOnce(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) triggerWebhook(")
	if at < 0 {
		t.Fatal("the webhook handler is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\n}\n"); end >= 0 {
		handler = handler[:end]
	}
	if !strings.Contains(handler, "ClaimWebhookDelivery(") {
		t.Error("a signed request is accepted however many times it arrives")
	}
	// The claim has to come before the work, or a replay creates the task and is
	// refused afterwards.
	claim, create := strings.Index(handler, "ClaimWebhookDelivery("), strings.Index(handler, "CreateAgentTask(")
	if create >= 0 && claim > create {
		t.Error("the delivery is claimed after the task is created, which is not a claim")
	}
	// And the signature still has to be checked first: claiming an unsigned
	// request would let anybody fill the ledger with signatures of their choosing.
	if verify := strings.Index(handler, "authorizeWebhook("); verify < 0 || verify > claim {
		t.Error("the delivery is claimed before the signature is verified")
	}
	// A ledger that cannot be read must refuse rather than wave the request past.
	if !strings.Contains(handler, "delivery_unverifiable") {
		t.Error("a ledger error is not distinguished from a first delivery; the replay check would fail open")
	}
}

// The record is not history: past the replay window it cannot refuse anything,
// and it is a table every webhook writes to. It is swept whether or not anybody
// configured retention, like an expired session.
func TestWebhookDeliveriesAreSweptWithoutBeingAskedTo(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "execution", "caretaker.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "expireWebhookDeliveries(ctx)") {
		t.Error("nothing sweeps the delivery ledger; it grows by one row per webhook for ever")
	}
	at := strings.Index(source, "func (c *Caretaker) expireWebhookDeliveries(")
	if at < 0 {
		t.Fatal("the sweep is gone; this guard is reading nothing")
	}
	fn := source[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	if strings.Contains(fn, "Retention") {
		t.Error("the sweep waits for retention to be configured; the ledger is not history")
	}
}
