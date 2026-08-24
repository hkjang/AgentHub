package execution

import (
	"os"
	"strings"
	"testing"
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
	// The three endings the platform decides, including the one where it gives up.
	// Measured: a dead-lettered task publishes task.dead_lettered and nothing
	// else, so leaving it out exported everything except the ending somebody most
	// wants explained — thirty of them in the deployment this was found on.
	for _, ending := range []string{"store.EventTaskCompleted", "store.EventTaskFailed", "store.EventTaskDeadLettered"} {
		if !strings.Contains(source, ending) {
			t.Errorf("%s is not exported, so that ending leaves no record", ending)
		}
	}
}

// A record this platform decided to export and then lost quietly would be worse
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
