package execution

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// The trail records the finding and never the value — that sentence is written
// on recordContentScan, and this is the path that could break it.
//
// SendDecision takes the record by value, so the record the dispatcher holds
// afterwards is the one the database gave it, before the scan rewrote anything.
// The agent's name is one of the fields the scan exists to rewrite: it is typed
// by a person, and somebody naming an agent after the case it handles is the
// whole reason this boundary scans more than three fields. Writing that name
// into the entry stores it in audit_events.details, which AuditTrail hands back
// out — so an export the deployment refused at the HTTP boundary would have kept
// the number instead of sending it, in the one table whose purpose is to say a
// number was found without saying which.
//
// Checked here rather than only on the details map, because the map is only
// safe as long as the entry the dispatcher writes is the map: this runs the
// dispatcher's own export against a real database and reads the row back.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestTheExportsAuditEntryKeepsNoValue(t *testing.T) {
	ctx, db := liveStore(t)

	admin := liveUser(ctx, t, db, "agenthub-scan-test-admin", "admin")
	// The number is in the agent's name, which is where it was invisible: the
	// scan read three fields, the name was not one of them, and the finding count
	// was zero — so there was no audit entry at all to be wrong about.
	agent := liveNamedAgent(ctx, t, db, admin.ID, "민원 "+livePolicyRRN+" 담당")

	// 차단: nothing may leave. The value must not leave by the other door either.
	restoreScan := liveSetting(ctx, t, db, admin.ID, dlp.SettingKey,
		dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}})
	defer restoreScan()

	sink := recordingServer(t, http.StatusOK)
	restoreSink := liveSetting(ctx, t, db, admin.ID, store.ProvenanceSettingKey,
		store.ProvenanceSettings{Endpoint: sink.URL})
	defer restoreSink()

	task, err := db.CreateAgentTask(ctx, store.CreateTaskInput{
		AgentID: agent.ID, OwnerID: admin.ID, CreatedBy: admin.ID,
		Title: "환급 검토", Input: "환급 대상인지 확인해 주세요.", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	finished := store.PlatformEvent{Type: store.EventTaskCompleted, SubjectType: "task", SubjectID: task.ID}
	if err := dispatcher.exportDecision(ctx, finished); err != nil {
		t.Fatalf("a record the scanner refused was reported as a transport failure: %v", err)
	}

	if got := sink.body(); strings.Contains(got, livePolicyRRN) {
		t.Fatalf("the record left for the sink carrying the number: %q", got)
	}

	// The entry the boundary wrote. It has to exist — a refusal the operator
	// cannot find is the thing the dlp.export action was added for — and it has
	// to be free of the value.
	page, err := db.AuditTrail(ctx, store.AuditFilter{Action: scanEventExport, ResourceID: agent.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatal("the export was refused and left no dlp.export entry, so nobody can see it stopped")
	}
	for _, item := range page.Items {
		entry, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(entry), livePolicyRRN) {
			t.Errorf("the audit trail stored the number the export was refused for: %s", entry)
		}
	}

	// One value in the record is one thing to tell the operator about, however
	// many fields the platform copied it into — DecisionForTask puts the agent's
	// name in the category as well.
	var details map[string]any
	if raw, ok := page.Items[0]["details"].(map[string]any); ok {
		details = raw
	} else if raw, ok := page.Items[0]["details"].([]byte); ok {
		if err := json.Unmarshal(raw, &details); err != nil {
			t.Fatal(err)
		}
	}
	findings, _ := details["findings"].([]any)
	if len(findings) != 1 {
		t.Errorf("one value found in two fields of one record is reported %d times: %v", len(findings), findings)
	}
}

// liveNamedAgent is liveAgent with the name the check is about: what an operator
// types here is free text, and this one carries a national ID because somebody
// named the agent after the case it handles.
func liveNamedAgent(ctx context.Context, t *testing.T, db *store.Store, ownerID, name string) store.Agent {
	t.Helper()
	agent, err := db.CreateAgent(ctx, ownerID, store.CreateAgentInput{
		Name: name, Description: "테스트가 만든 에이전트",
		RuntimeType: runtimetype.OpenCode,
	})
	if err != nil {
		t.Skipf("this deployment will not let the check create an agent: %v", err)
	}
	t.Cleanup(func() { _ = db.DeleteAgent(ctx, agent.ID, ownerID, true) })
	return agent
}
