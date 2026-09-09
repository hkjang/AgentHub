package execution

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
)

// The wiring, not the senders. The two send functions are checked on their own
// in contentpolicy_test.go with a guard handed to them; this checks the thing
// that hands it over, which is where a rule about a person gets lost.
//
// The arrangement is the ordinary one and the reason the two owners have to be
// told apart: a shared review agent belongs to an administrator, and the task
// running on it belongs to somebody else — control.go delegates with the
// parent's OwnerID onto another owner's agent, and the scheduler and the
// dispatcher start tasks under a trigger's owner on whatever agent the trigger
// names. Everything else on this platform decides about the person the work
// belongs to: the workflow.Step this review builds carries task.OwnerID, and
// the decision record the export sends carries t.owner_id.
//
// Read the agent's owner here instead and the narrowest rule an operator can
// write evaluates against the administrator who owns the shared agent, matches
// nobody, and the class's global action — 기록만, which is what a site runs
// while it is still learning what its agents handle — publishes the number on
// somebody else's pull request.
//
// The credential is the opposite answer to a different question: the token is
// stored by whoever owns the agent, and it stays that way. Both are pinned
// below by storing the connection under the administrator and owning the task
// with somebody else — reading either one from the wrong person fails this.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestARuleAboutAPersonDecidesAtTheBoundariesThatPublish(t *testing.T) {
	ctx, db := liveStore(t)

	// 'contractor' is what the policy package's own example calls this person;
	// this platform's own roles are user, manager and admin, so the rule names
	// the plain user who owns the work and never the admin who owns the agent.
	admin := liveUser(ctx, t, db, "agenthub-policy-test-admin", "admin")
	worker := liveUser(ctx, t, db, "agenthub-policy-test-worker", "user")
	if admin.ID == worker.ID {
		t.Fatal("the two owners have to be different people for this to check anything")
	}
	agent := liveAgent(ctx, t, db, admin.ID)

	// 기록만: the class's own action records the finding and lets the text out,
	// so the central policy is the only thing that can refuse.
	restoreScan := liveSetting(ctx, t, db, admin.ID, dlp.SettingKey,
		dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Audit}})
	defer restoreScan()

	forge := recordingServer(t, http.StatusCreated)
	sink := recordingServer(t, http.StatusOK)
	restoreSink := liveSetting(ctx, t, db, admin.ID, store.ProvenanceSettingKey,
		store.ProvenanceSettings{Endpoint: sink.URL})
	defer restoreSink()

	// The credential belongs to the agent's owner, which is who stored it.
	host := strings.TrimPrefix(forge.URL, "http://")
	connection, err := db.PutSCMConnection(ctx, admin.ID, host, "gitea", forge.URL+"/api/v1", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.DeleteSCMConnection(ctx, admin.ID, connection.ID) }()

	// The work belongs to somebody else. The number is in the title because that
	// is what the decision record exports as its scenario.
	task, err := db.CreateAgentTask(ctx, store.CreateTaskInput{
		AgentID: agent.ID, OwnerID: worker.ID, CreatedBy: worker.ID,
		Title:     "민원인 " + livePolicyRRN + " 환급 검토",
		Input:     "환급 대상인지 확인해 주세요.",
		Source:    "webhook",
		SourceURL: forge.URL + "/acme/store/pulls/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The task goes with the agent: agent_tasks references agent_definitions ON
	// DELETE CASCADE, and liveAgent already arranged for that.

	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	dispatcher := NewDispatcher(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	finished := store.PlatformEvent{Type: store.EventTaskCompleted, SubjectType: "task", SubjectID: task.ID}
	findings := []store.ReviewFinding{{
		FilePath: "app/pay.py", StartLine: 5, Severity: "high",
		Message: "테스트 픽스처에 실제 주민등록번호 " + livePolicyRRN + " 가 그대로 들어 있습니다.",
	}}

	boundaries := []struct {
		name string
		send func()
	}{
		{"리뷰 코멘트", func() {
			forge.reset()
			orchestrator.announceReview(ctx, store.AgentRun{AgentID: agent.ID}, task, agent, "지적 1건", findings)
		}},
		{"결정 기록", func() {
			sink.reset()
			if err := dispatcher.exportDecision(ctx, finished); err != nil {
				t.Fatalf("the export failed for a reason that is not the policy: %v", err)
			}
		}},
	}
	arrived := map[string]func() string{"리뷰 코멘트": forge.body, "결정 기록": sink.body}

	for _, boundary := range boundaries {
		t.Run(boundary.name, func(t *testing.T) {
			// The rule an operator writes about the person the work belongs to.
			restore := liveSetting(ctx, t, db, admin.ID, policy.SettingKey, policyAbout(worker.Role))
			boundary.send()
			restore()
			if got := arrived[boundary.name](); strings.Contains(got, livePolicyRRN) {
				t.Errorf("a rule about the task's owner did not reach this boundary, and the value was published: %s", got)
			}

			// The same rule about the person who owns the agent instead. Nobody
			// wrote a rule about the person doing the work, so this text goes —
			// and a boundary reading the agent's owner refuses it here, which is
			// the same mistake seen from the other side.
			restore = liveSetting(ctx, t, db, admin.ID, policy.SettingKey, policyAbout(admin.Role))
			boundary.send()
			restore()
			if got := arrived[boundary.name](); !strings.Contains(got, livePolicyRRN) {
				t.Errorf("the boundary decided about the agent's owner rather than the task's: %q", got)
			}
		})
	}
}

// livePolicyRRN is a syntactically valid resident registration number the
// scanner recognises. It belongs to nobody: the check digit is wrong.
const livePolicyRRN = "900101-1234568"

// policyAbout is the sentence an operator writes about one role and the two
// paths that put text on a machine this deployment does not own.
func policyAbout(role string) policy.Document {
	return policy.Document{Rules: []policy.Rule{{
		ID:          "no-external-publishing-by-" + role,
		Effect:      policy.Deny,
		Actions:     []string{policy.ActionDecisionExport, policy.ActionReviewComment},
		Roles:       []string{role},
		DataClasses: []string{"rrn"},
		Reason:      role + "이 다룬 내용은 외부로 내보낼 수 없습니다.",
	}}}
}

// liveStore opens the deployment this check runs against and brings its schema
// up to date, so a database created for the run works as well as a long-lived
// one.
func liveStore(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	cipher, err := testCipher()
	if err != nil {
		t.Skip("no encryption key to store a credential with")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("the schema could not be brought up to date: %v", err)
	}
	return ctx, db
}

// liveUser finds or creates one person with the role this check needs. Upserted
// on a fixed subject so re-running leaves one of each rather than a pile.
func liveUser(ctx context.Context, t *testing.T, db *store.Store, username, role string) store.User {
	t.Helper()
	user, err := db.UpsertOIDCUser(ctx, "agenthub-policy-test:"+username, username, "", "", role == "admin")
	if err != nil {
		t.Skipf("this deployment will not let the check create the people it needs: %v", err)
	}
	if user.Role != role || user.Status != "active" {
		user, err = db.UpdateUserGovernance(ctx, user.ID, role, "active", nil)
		if err != nil {
			t.Skipf("the role could not be set on the test user: %v", err)
		}
	}
	return user
}

// liveAgent is the shared agent the work is delegated onto, owned by somebody
// other than whoever the task belongs to.
func liveAgent(ctx context.Context, t *testing.T, db *store.Store, ownerID string) store.Agent {
	t.Helper()
	return liveNamedAgent(ctx, t, db, ownerID, "정책-경계-검사 리뷰 에이전트")
}

// liveSetting writes one settings row as actor — system_settings records who
// last wrote it and the column is a foreign key — and hands back what puts it
// as it was, so a check does not leave a live deployment configured by a test.
func liveSetting(ctx context.Context, t *testing.T, db *store.Store, actor, key string, value any) func() {
	t.Helper()
	var before json.RawMessage
	had := db.Setting(ctx, key, &before) == nil
	if err := db.PutSetting(ctx, key, value, nil, actor); err != nil {
		t.Skipf("this deployment's %s setting cannot be written: %v", key, err)
	}
	return func() {
		var restore any = json.RawMessage("{}")
		if had {
			restore = before
		}
		if err := db.PutSetting(ctx, key, restore, nil, actor); err != nil {
			t.Errorf("the %s setting was left as the test set it: %v", key, err)
		}
	}
}

// recordingServer stands in for a machine this deployment does not own: it keeps
// whatever arrived and answers the way that forge or sink would.
type recorder struct {
	*httptest.Server
	got string
}

func recordingServer(t *testing.T, status int) *recorder {
	t.Helper()
	kept := &recorder{}
	kept.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			kept.got += string(body)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(kept.Close)
	return kept
}

func (r *recorder) reset()       { r.got = "" }
func (r *recorder) body() string { return r.got }
