package execution

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/guard"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// The scan limit is a real setting with a screen of its own, and what happens
// past it was written down nowhere.
//
// dlp.Result.Truncated says "a clean result is not mistaken for a complete one",
// and every boundary returned the moment the findings were empty — so the one
// payload the scanner cannot vouch for produced no log line and no audit entry,
// which is precisely what a payload read to its end and found clean produces.
// The operator reading the trail cannot tell "we looked at all of it" from "we
// looked at the first 64 kB of it", and a deployment that lowered MaxBytes to
// keep large tool results cheap made the second one the ordinary case.
//
// Checked against a real database and through the constructors the platform
// itself uses, because the gate that was wrong is in the recorder, not in the
// scanner: the result was right all along and nothing asked it.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestAPayloadPastTheScanLimitLeavesATrail(t *testing.T) {
	ctx, db := liveStore(t)

	admin := liveUser(ctx, t, db, "agenthub-scan-limit-admin", "admin")
	agent := liveNamedAgent(ctx, t, db, admin.ID, "검사 한도 확인 에이전트")

	// A limit small enough that the fixtures do not have to be megabytes, and one
	// an operator can really set: Validate accepts anything from 0 to 4 MB.
	restoreScan := liveSetting(ctx, t, db, admin.ID, dlp.SettingKey,
		dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}, MaxBytes: 64})
	defer restoreScan()

	// Nothing sensitive anywhere in it. There is simply more of it than the limit.
	long := strings.Repeat("점검 항목을 순서대로 확인했습니다. ", 8)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The model boundary, built the way cmd/worker builds it.
	t.Run("모델 호출", func(t *testing.T) {
		step := workflow.Step{ID: "task", AgentID: agent.ID, AgentName: agent.Name, OwnerID: admin.ID}
		sent, err := guard.NewModel(db, logger).Outbound(ctx, step, long)
		if err != nil {
			t.Fatalf("a long clean prompt was refused: %v", err)
		}
		// The tail past the limit is carried through unchanged; this is a record of
		// what was inspected, not a reason to hold the prompt back.
		if sent != long {
			t.Error("the prompt was rewritten although the scanner found nothing in it")
		}
		entry := liveScanEntry(ctx, t, db, "dlp.model", agent.ID)
		if entry["outcome"] != dlp.OutcomeUnscanned {
			t.Errorf("a prompt read only as far as the limit is filed as %q", entry["outcome"])
		}
	})

	// And the decision record, which is the same silence at the other end of the
	// platform: it leaves the building for an address the deployment configured.
	t.Run("결정 기록 전송", func(t *testing.T) {
		sink := recordingServer(t, http.StatusOK)
		restoreSink := liveSetting(ctx, t, db, admin.ID, store.ProvenanceSettingKey,
			store.ProvenanceSettings{Endpoint: sink.URL})
		defer restoreSink()

		task, err := db.CreateAgentTask(ctx, store.CreateTaskInput{
			AgentID: agent.ID, OwnerID: admin.ID, CreatedBy: admin.ID,
			Title: long, Input: "확인해 주세요.", Source: "manual",
		})
		if err != nil {
			t.Fatal(err)
		}

		dispatcher := NewDispatcher(db, logger)
		finished := store.PlatformEvent{Type: store.EventTaskCompleted, SubjectType: "task", SubjectID: task.ID}
		if err := dispatcher.exportDecision(ctx, finished); err != nil {
			t.Fatalf("a long clean record failed to export: %v", err)
		}
		if got := sink.body(); !strings.Contains(got, "점검 항목을") {
			t.Fatalf("the record never reached the sink: %q", got)
		}

		entry := liveScanEntry(ctx, t, db, scanEventExport, agent.ID)
		if entry["outcome"] != dlp.OutcomeUnscanned {
			t.Errorf("a record read only as far as the limit is filed as %q", entry["outcome"])
		}
	})

	// The other half of the sentence: what fits is still silent. An entry per
	// model call would bury the findings among the ordinary traffic, which is the
	// state this platform started from.
	t.Run("한도 안의 호출", func(t *testing.T) {
		quiet := liveNamedAgent(ctx, t, db, admin.ID, "짧은 호출 에이전트")
		step := workflow.Step{ID: "task", AgentID: quiet.ID, AgentName: quiet.Name, OwnerID: admin.ID}
		if _, err := guard.NewModel(db, logger).Outbound(ctx, step, "확인해 주세요."); err != nil {
			t.Fatalf("a short clean prompt was refused: %v", err)
		}
		page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "dlp.model", ResourceID: quiet.ID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Errorf("a prompt the scanner read to its end and found nothing in was recorded: %v", page.Items)
		}
	})
}

// liveScanEntry reads back the one entry a boundary wrote about a payload, and
// fails where the gap was: no entry at all.
func liveScanEntry(ctx context.Context, t *testing.T, db *store.Store, action, agentID string) map[string]any {
	t.Helper()
	page, err := db.AuditTrail(ctx, store.AuditFilter{Action: action, ResourceID: agentID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("%s read part of a payload and said so nowhere, so a partial scan looks exactly like a clean one", action)
	}
	return page.Items[0]
}
