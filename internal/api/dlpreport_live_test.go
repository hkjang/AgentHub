package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/dlp"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// A finding the in-Pod gateway reports has to end up in the audit trail, filed
// under what the gateway did.
//
// This endpoint is the only reason a tool-call finding is visible anywhere but a
// Pod log, and it was the one boundary nothing exercised end to end: the
// gateway's own test posts to a stand-in that decodes leniently, and the handler
// had no test at all. Between the two sat a report the handler refused — every
// one of them, since the day the scanner shipped — with a 400 the gateway is
// built to ignore. The trail promised by the DLP settings screen was empty on
// every deployment, and nothing said so.
//
// So this goes through the production router with a real runtime token, posting
// exactly the document cmd/runtime-proxy's record() writes, and reads the trail
// back from the database.
//
// Point it at a database with AGENTHUB_TEST_DSN and AGENTHUB_ENCRYPTION_KEY.
func TestAGatewaysFindingReachesTheTrail(t *testing.T) {
	ctx, db, handler, owner := gatewayDeployment(t)

	// The report as the gateway writes it: its log entry under the runtime id,
	// discriminator included. That "event" key is what the handler choked on.
	report := func(runtime store.Runtime, blocked, truncated bool, findings []dlp.Finding) map[string]any {
		return map[string]any{
			"runtimeId": runtime.ID,
			"event": map[string]any{
				"event": "dlp", "server": "jira", "tool": "create_issue", "direction": "요청",
				"blocked": blocked, "findings": findings, "truncated": truncated,
			},
		}
	}
	found := []dlp.Finding{{Class: "rrn", Label: "주민등록번호", Count: 1, Action: dlp.Audit, Sample: "900101-*******"}}

	t.Run("기록만 하는 발견", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		response := post(handler, token, report(runtime, false, false, found))
		if response.Code != http.StatusAccepted {
			t.Fatalf("the gateway's report was refused with %d: %s", response.Code, response.Body)
		}
		entry := trailEntry(ctx, t, db, runtime.AgentID)
		if entry["outcome"] != dlp.OutcomeAudited {
			t.Errorf("a finding the gateway only recorded is filed as %q", entry["outcome"])
		}
		if entry["actor"] != "dlp-report-owner" {
			t.Errorf("the entry's actor is %q; the trail cannot be filtered by the person answerable for the agent", entry["actor"])
		}
		details, _ := entry["details"].(map[string]any)
		if details["tool"] != "create_issue" || details["server"] != "jira" {
			t.Errorf("the entry does not say which call it was about: %v", details)
		}
		if raw, _ := json.Marshal(details["findings"]); !strings.Contains(string(raw), "900101-*******") {
			t.Errorf("the masked sample did not make it into the trail: %s", raw)
		}
	})

	t.Run("거절된 호출", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		blocked := []dlp.Finding{{Class: "rrn", Label: "주민등록번호", Count: 1, Action: dlp.Block, Sample: "900101-*******"}}
		if response := post(handler, token, report(runtime, true, false, blocked)); response.Code != http.StatusAccepted {
			t.Fatalf("the gateway's report was refused with %d: %s", response.Code, response.Body)
		}
		if entry := trailEntry(ctx, t, db, runtime.AgentID); entry["outcome"] != dlp.OutcomeBlocked {
			t.Errorf("a call the gateway refused is filed as %q", entry["outcome"])
		}
	})

	t.Run("한도까지만 읽은 깨끗한 페이로드", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		if response := post(handler, token, report(runtime, false, true, []dlp.Finding{})); response.Code != http.StatusAccepted {
			t.Fatalf("the gateway's report was refused with %d: %s", response.Code, response.Body)
		}
		if entry := trailEntry(ctx, t, db, runtime.AgentID); entry["outcome"] != dlp.OutcomeUnscanned {
			t.Errorf("a payload read only as far as the limit is filed as %q", entry["outcome"])
		}
	})

	// A report with nothing in it is not an event. The gateway never sends one,
	// and a handler that files it anyway files it as "audited" — an entry claiming
	// a finding nobody made, under an agent that did nothing.
	t.Run("아무것도 없는 보고", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		response := post(handler, token, report(runtime, false, false, []dlp.Finding{}))
		if response.Code != http.StatusBadRequest {
			t.Errorf("a report of nothing was accepted with %d: %s", response.Code, response.Body)
		}
		page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "dlp.tool", ResourceID: runtime.AgentID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Errorf("a report of nothing was filed in the trail: %v", page.Items)
		}
	})

	t.Run("남의 토큰", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		if response := post(handler, "not-"+token, report(runtime, false, false, found)); response.Code != http.StatusUnauthorized {
			t.Errorf("a report under a token no runtime holds was answered with %d", response.Code)
		}
		body := report(runtime, false, false, found)
		body["runtimeId"] = "somebody-else"
		if response := post(handler, token, body); response.Code != http.StatusForbidden {
			t.Errorf("a report about another runtime was answered with %d", response.Code)
		}
		page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "dlp.tool", ResourceID: runtime.AgentID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Errorf("a report that was refused was filed anyway: %v", page.Items)
		}
	})
}

// gatewayDeployment is a control plane over a real database, with the person
// whose agents the reports will be about.
func gatewayDeployment(t *testing.T) (context.Context, *store.Store, http.Handler, store.User) {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("AGENTHUB_ENCRYPTION_KEY"))
	if err != nil || len(rawKey) == 0 {
		t.Skip("no encryption key to open the store with")
	}
	cipher, err := cryptox.New(rawKey)
	if err != nil {
		t.Skip("no encryption key to open the store with")
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
	// Upserted on a fixed subject: re-running leaves one of them rather than a pile.
	owner, err := db.UpsertOIDCUser(ctx, "agenthub-dlp-report-test:owner", "dlp-report-owner", "", "", false)
	if err != nil {
		t.Skipf("this deployment will not let the check create the owner it needs: %v", err)
	}
	server := New(db, cipher, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), appLog.NewRing(8), nil, nil)
	return ctx, db, server.Handler(), owner
}

// gatewayRuntime is one runtime of its own agent, holding the gateway token the
// way the Pod does. Each case gets its own so the trail one case reads cannot
// have been written by another.
func gatewayRuntime(ctx context.Context, t *testing.T, db *store.Store, owner store.User) (store.Runtime, string) {
	t.Helper()
	agent, err := db.CreateAgent(ctx, owner.ID, store.CreateAgentInput{
		Name: "게이트웨이 보고 에이전트", Description: "테스트가 만든 에이전트", RuntimeType: runtimetype.OpenCode,
	})
	if err != nil {
		t.Skipf("this deployment will not let the check create an agent: %v", err)
	}
	t.Cleanup(func() { _ = db.DeleteAgent(ctx, agent.ID, owner.ID, true) })
	runtime, err := db.CreateRuntime(ctx, agent, "running")
	if err != nil {
		t.Fatal(err)
	}
	token := "gateway-token-" + runtime.ID
	if err := db.SetRuntimeGatewayToken(ctx, runtime.ID, token); err != nil {
		t.Fatal(err)
	}
	return runtime, token
}

func post(handler http.Handler, token string, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-gateway/dlp-events", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// trailEntry reads back the one entry the report should have produced, and
// fails where the gap was: no entry at all.
func trailEntry(ctx context.Context, t *testing.T, db *store.Store, agentID string) map[string]any {
	t.Helper()
	page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "dlp.tool", ResourceID: agentID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("the gateway reported a finding and the trail holds %d entries for it; what the Pod's scanner does is visible nowhere but the Pod log", len(page.Items))
	}
	return page.Items[0]
}
