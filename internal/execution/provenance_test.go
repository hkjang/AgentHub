package execution

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The export must be free for a deployment that has not configured one, which is
// almost every deployment: no request, no error, no log line.
func TestNoSinkMeansNoExport(t *testing.T) {
	body, err := os.ReadFile("provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	settings := strings.Index(source, "settings.Configured()")
	request := strings.Index(source, "http.NewRequestWithContext")
	if settings < 0 {
		t.Fatal("nothing checks whether a sink is configured")
	}
	if request >= 0 && settings > request {
		t.Error("a request is built before anybody asks whether there is anywhere to send it")
	}
	// The endings are one list, in the store, so that the screen advertising them
	// and the filter applying them cannot disagree — they did, for one release.
	if !strings.Contains(source, "store.Exports(event.Type)") {
		t.Error("the exporter decides for itself which endings count instead of reading the one list")
	}
	list, err := os.ReadFile("../store/provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	events := string(list)
	at := strings.Index(events, "var ProvenanceEvents = ")
	if at < 0 {
		t.Fatal("there is no list of exported endings")
	}
	line := events[at : at+strings.Index(events[at:], "\n")]
	// Measured: a dead-lettered task publishes task.dead_lettered and nothing
	// else, so leaving it out exported everything except the ending somebody most
	// wants explained — thirty of them in the deployment this was found on.
	for _, ending := range []string{"EventTaskCompleted", "EventTaskFailed", "EventTaskDeadLettered"} {
		if !strings.Contains(line, ending) {
			t.Errorf("%s is not exported, so that ending leaves no record", ending)
		}
	}
}

// The screen tells an operator which endings are exported. It must be reading
// the same list the dispatcher filters on: for one release it held its own copy
// of two endings while the dispatcher sent three, so the answer the operator read
// was wrong about the deployment they were configuring.
func TestTheScreenAdvertisesWhatIsActuallySent(t *testing.T) {
	body, err := os.ReadFile("../api/provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, `"events":          store.ProvenanceEvents`) {
		t.Error("the settings screen is not told the exported endings from the one list")
	}
	if strings.Contains(source, "store.EventTask") {
		t.Error("the screen names endings itself; that copy is what drifted last time")
	}
}

// The address an operator typed is proven before their audit trail depends on
// it, using the request the dispatcher will make.
func TestSendingReachesTheAddressWithItsCredential(t *testing.T) {
	var gotAuth, gotType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Audit-Key")
		gotType = r.Header.Get("Content-Type")
		read, _ := io.ReadAll(r.Body)
		gotBody = string(read)
	}))
	defer server.Close()
	settings := store.ProvenanceSettings{Endpoint: server.URL, Header: "X-Audit-Key", Token: "s3cret"}
	if err := SendDecision(context.Background(), settings, store.DecisionRecord{DecisionID: "run:abc", Outcome: "test"}); err != nil {
		t.Fatalf("a receiver that answered 200 was reported as a failure: %v", err)
	}
	if gotAuth != "s3cret" {
		t.Errorf("the credential did not arrive: %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("the receiver was not told this is JSON: %q", gotType)
	}
	if !strings.Contains(gotBody, `"decisionId":"run:abc"`) {
		t.Errorf("the record did not arrive: %q", gotBody)
	}

	// A receiver that refuses must be reported as a refusal, with its own status
	// in the sentence: "HTTP 404" and "no such host" send an operator to
	// different places.
	refuses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer refuses.Close()
	err := SendDecision(context.Background(), store.ProvenanceSettings{Endpoint: refuses.URL}, store.DecisionRecord{})
	if err == nil {
		t.Fatal("a receiver answering 404 was read as success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("the refusal does not say what the receiver answered: %v", err)
	}
}

// A record this platform decided to export and then lost quietly// A record this platform decided to export and then lost quietly would be worse
// than one that arrives late. The dispatcher already knows how to keep an event
// pending, back off, and tell somebody when it gives up.
func TestAnExportThatFailedKeepsTheEventPending(t *testing.T) {
	body, err := os.ReadFile("dispatcher.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "d.exportDecision(finish, event)")
	if at < 0 {
		t.Fatal("the decision is never exported; this guard is reading nothing")
	}
	retry := strings.Index(source, "d.retry(finish, event, failed)")
	delivered := strings.Index(source, "d.store.MarkEventDelivered(finish, event.ID)")
	if retry < 0 || at > retry {
		t.Error("the export runs after the event is retried or not at all")
	}
	if delivered >= 0 && at > delivered {
		t.Error("the event is marked delivered before the record is sent, so a failed export is lost")
	}
	// It must fail the delivery rather than only logging.
	tail := source[at:]
	if end := strings.Index(tail, "\n\tif failed"); end >= 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "failed = err.Error()") {
		t.Error("an export failure does not reach the retry, so the record is dropped silently")
	}
}

// What is exported is what this platform observed. The agent's own claim of
// success is not a field: the outcome is the task's recorded status and the
// reasoning is the evaluator's verdict.
func TestTheRecordCarriesThePlatformsAccount(t *testing.T) {
	body, err := os.ReadFile("../store/provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, field := range []string{"AgentVersion", "RuntimeImage", "ApprovalID", "Source ", "Model "} {
		if !strings.Contains(source, field) {
			t.Errorf("the record does not say %s, which is part of what an auditor follows", strings.TrimSpace(field))
		}
	}
	// The version and image are what ran, so they are read from the task's own
	// row rather than from whatever the agent is configured with now.
	if !strings.Contains(source, "record.Reasoning = verdict.Reason") {
		t.Error("the reasoning is the agent's last sentence rather than the evaluator's verdict")
	}
	if !strings.Contains(source, `"task:" + record.TaskID`) {
		t.Error("the decision has no stable identity, so the same decision arrives twice as two")
	}
}

// A definition is edited. The agent that ran version 3 against one model is
// version 7 against another by the time an auditor asks, so a record that
// reports the agent's current configuration is a record of something that never
// happened.
func TestTheRecordReportsWhatRanNotWhatIsConfiguredNow(t *testing.T) {
	body, err := os.ReadFile("../store/provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	// The version, the model and the image all come from the run, or from that
	// version's own snapshot, before any fallback to the definition.
	for _, want := range []struct{ sql, why string }{
		{"COALESCE(r.agent_version, a.version)", "the version reported is the definition's current one, not the one that ran"},
		{"COALESCE(NULLIF(r.model_name,''), m.name)", "the model reported is whatever the agent points at now"},
		{"COALESCE(vi.version, i.version)", "the image reported is the agent's current pin rather than the one that version ran"},
		{"v.agent_id = a.id AND v.version = r.agent_version", "the image is not read from the version that actually ran"},
		{"COALESCE(r.model_endpoint_id, a.model_endpoint_id)", "the endpoint is resolved from the definition even when the run recorded one"},
	} {
		if !strings.Contains(source, want.sql) {
			t.Error(want.why)
		}
	}
}

// One attempt, one decision.
//
// A task that fails and then succeeds on the retry publishes both endings.
// Naming the record after the task made those one decision arriving twice with
// different outcomes, in no guaranteed order — measured on a deployment where
// completed tasks had published task.failed as well.
func TestEachAttemptIsItsOwnDecision(t *testing.T) {
	body, err := os.ReadFile("../store/provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, `record.DecisionID = "run:" + record.RunID`) {
		t.Error("two attempts at the same task collide on one decision id")
	}
	// A task with no run recorded still has an identity rather than an empty one.
	if !strings.Contains(source, `record.DecisionID = "task:" + record.TaskID`) {
		t.Error("a decision with no run has no identity at all")
	}
	fallback := strings.Index(source, `"task:" + record.TaskID`)
	perRun := strings.Index(source, `"run:" + record.RunID`)
	if fallback < 0 || perRun < 0 || fallback > perRun {
		t.Error("the fallback overwrites the per-attempt identity")
	}
}
