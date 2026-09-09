package execution

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
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
	if _, err := SendDecision(context.Background(), settings, ContentGuard{}, store.DecisionRecord{DecisionID: "run:abc", Outcome: "test"}); err != nil {
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
	_, err := SendDecision(context.Background(), store.ProvenanceSettings{Endpoint: refuses.URL}, ContentGuard{}, store.DecisionRecord{})
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

// The export is the fourth way text leaves this deployment, after the prompt,
// the model's answer and the tool call. Those three are scanned; this one was
// not, and it carries the same text — a title somebody typed and the reasoning
// that quotes what ran.
func TestARecordIsScannedOnItsWayOut(t *testing.T) {
	const id = "민원인 900101-1234568 환급 검토"
	record := store.DecisionRecord{
		TaskID: "t1", Scenario: id, Reasoning: "확인함: " + id, SourceURL: "https://x/case?rrn=900101-1234568",
	}

	// Nothing configured must change nothing: almost every deployment.
	same, untouched := scrubDecision(dlp.Settings{}, record)
	if untouched.Blocked || same.Scenario != id {
		t.Errorf("a deployment with no scanner had its record changed: %q", same.Scenario)
	}

	redacting := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Redact}}
	scrubbed, scan := scrubDecision(redacting, record)
	if scan.Blocked {
		t.Error("redaction withheld the record instead of redacting it")
	}
	if len(scan.Findings) != 3 {
		t.Errorf("want the national ID found in all three fields, found %d", len(scan.Findings))
	}
	for name, value := range map[string]string{
		"scenario": scrubbed.Scenario, "reasoning": scrubbed.Reasoning, "sourceUrl": scrubbed.SourceURL,
	} {
		if strings.Contains(value, "900101-1234568") {
			t.Errorf("the national ID left the building in %s: %q", name, value)
		}
	}

	blocking := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}
	if _, refused := scrubDecision(blocking, record); !refused.Blocked {
		t.Error("a class configured to block was sent to an external address anyway")
	}

	// The scan is part of sending rather than something a caller remembers to do:
	// a guard that only checked the order let a mutation through that scanned the
	// record and then sent the original.
	var arrived []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, _ := io.ReadAll(r.Body)
		arrived = append(arrived, string(read))
	}))
	defer server.Close()
	sink := store.ProvenanceSettings{Endpoint: server.URL}

	_, err := SendDecision(context.Background(), sink, ContentGuard{Scan: blocking}, record)
	var withheld WithheldError
	if !errors.As(err, &withheld) {
		t.Fatalf("sending a blocked record was not refused: %v", err)
	}
	if len(withheld.Classes) == 0 || withheld.Classes[0] != "rrn" {
		t.Errorf("the refusal does not say what it found: %v", withheld.Classes)
	}
	if len(arrived) != 0 {
		t.Fatalf("a blocked record reached the address anyway: %q", arrived)
	}

	if _, err := SendDecision(context.Background(), sink, ContentGuard{Scan: redacting}, record); err != nil {
		t.Fatalf("a redactable record was not sent: %v", err)
	}
	if len(arrived) != 1 || strings.Contains(arrived[0], "900101-1234568") {
		t.Fatalf("the national ID left the building: %q", arrived)
	}
}

// A record held back must be visible where somebody counting on the export will
// look, and must not be retried: the scanner will refuse it again, and the
// dispatcher's retry is for sinks that come back.
func TestAWithheldRecordIsLoudAndFinal(t *testing.T) {
	body, err := os.ReadFile("provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	send := strings.Index(source, "SendDecision(ctx, settings, d.contentGuard(ctx, record), record)")
	if send < 0 {
		t.Fatal("the dispatcher sends without handing the send this deployment's scanner settings")
	}
	withheld := source[send:]
	if end := strings.Index(withheld, "\n\td.logger.Info"); end > 0 {
		withheld = withheld[:end]
	}
	if !strings.Contains(withheld, `"provenance.withheld"`) {
		t.Error("a record held back leaves no audit entry, so nobody learns the export stopped")
	}
	if !strings.Contains(withheld, "errors.As(err, &withheld)") {
		t.Error("a refusal from the scanner is treated as a transport failure and retried")
	}
	if !strings.Contains(withheld, "d.logger.Warn") {
		t.Error("a record held back is not logged")
	}
	if !strings.Contains(withheld, "return nil") {
		t.Error("a blocked record is retried; the scanner will refuse it every time")
	}
}

// The whole record is marshalled and posted, so the whole record is what the
// scan has to cover — the way the review comment is scanned as the one text it
// is. It covered three fields of nine, and the ones it skipped are typed by
// people: the agent's name, the category the platform copies out of it and the
// name of the model endpoint.
func TestEveryWordInARecordIsScannedOnItsWayOut(t *testing.T) {
	const id = "900101-1234568"
	record := store.DecisionRecord{
		TaskID: "t1", Agent: "민원 " + id + " 담당", Category: "민원 " + id + " 담당",
		Model: "gpt-" + id, Scenario: "환급 검토", Outcome: "completed", Source: "manual",
	}

	redacting := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Redact}}
	scrubbed, scan := scrubDecision(redacting, record)
	if len(scan.Findings) != 3 {
		t.Errorf("want the national ID found in the agent, the category and the model, found %d", len(scan.Findings))
	}
	for name, value := range map[string]string{
		"agent": scrubbed.Agent, "category": scrubbed.Category, "model": scrubbed.Model,
	} {
		if strings.Contains(value, id) {
			t.Errorf("the national ID left the building in %s: %q", name, value)
		}
	}

	blocking := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}
	if _, refused := scrubDecision(blocking, record); !refused.Blocked {
		t.Error("a class configured to block left in the agent's name anyway")
	}
}

// The ids are left alone on purpose, and it is not only that a redacted id is a
// record nobody can join to anything. The account-number detector has nothing
// but grouping to go on, so an all-digit identifier is a finding on every export
// — and, redacting, an id rewritten on its way out.
func TestTheIdentifiersInARecordAreNotScanned(t *testing.T) {
	const uuid = "12345678-1234-1234-1234-123456789012"
	record := store.DecisionRecord{
		DecisionID: "run:" + uuid, TaskID: uuid, RunID: uuid, AgentID: uuid,
		OwnerID: uuid, ApprovalID: uuid,
	}
	settings := dlp.Settings{Enabled: true, Classes: map[string]string{"account": dlp.Redact}}
	// The detector really does claim one, which is why the exemption is written
	// down rather than assumed.
	if len(dlp.Scan(settings, uuid).Findings) == 0 {
		t.Fatal("this test no longer proves anything: the detector no longer claims an all-digit id")
	}
	scrubbed, scan := scrubDecision(settings, record)
	if len(scan.Findings) != 0 {
		t.Errorf("every export of this record reports a finding about its own identifiers: %v", scan.Findings)
	}
	if scrubbed.DecisionID != "run:"+uuid || scrubbed.TaskID != uuid || scrubbed.OwnerID != uuid {
		t.Errorf("an identifier was rewritten on its way out: %+v", scrubbed)
	}
}

// A field added to the record and left out of both lists would leave the
// building uninspected, and would do it quietly: nothing else in this package
// reads the record field by field. Three fields were already out.
func TestEveryFieldOfARecordIsEitherScannedOrAnIdentifier(t *testing.T) {
	scanned := map[string]bool{}
	for _, field := range decisionWords {
		scanned[field.name] = true
	}
	exempt := map[string]bool{}
	for _, name := range decisionIdentifiers {
		if scanned[name] {
			t.Errorf("%s is listed as both scanned and exempt", name)
		}
		exempt[name] = true
	}

	recordType := reflect.TypeOf(store.DecisionRecord{})
	declared := map[string]bool{}
	for i := 0; i < recordType.NumField(); i++ {
		field := recordType.Field(i)
		declared[field.Name] = true
		// Only the words: a time or a count carries nothing a detector looks for.
		if field.Type.Kind() != reflect.String {
			continue
		}
		if !scanned[field.Name] && !exempt[field.Name] {
			t.Errorf("DecisionRecord.%s is posted to an external address without being scanned; "+
				"add it to decisionWords, or to decisionIdentifiers if it is an id", field.Name)
		}
	}
	for name := range scanned {
		if !declared[name] {
			t.Errorf("decisionWords names %s, which the record no longer has", name)
		}
	}
	for name := range exempt {
		if !declared[name] {
			t.Errorf("decisionIdentifiers names %s, which the record no longer has", name)
		}
	}

	// The list drives the scan rather than sitting beside it, so a field named
	// here is a field actually read.
	body, err := os.ReadFile("provenance.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "for _, field := range decisionWords {") {
		t.Error("scrubDecision no longer scans the list this test checks")
	}
}
