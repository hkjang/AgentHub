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
	"unicode/utf8"

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

// How long the strings in the trail are is the control plane's decision, not the
// Pod's.
//
// Every string in a report — the server, the tool, the direction, and each
// finding's class, label, action and masked sample — went into audit_events.details
// and into the operator's log exactly as long as the Pod wrote it, bounded only
// by the 1MiB body reader. The same server has cut an mcp.tool_call's client id
// to 200 runes since it shipped, so two audit paths on one deployment disagreed
// about whose word the size was.
//
// The bodies below are sized to fit under that body cap, which is why the
// oversized fields are hundreds of thousands of runes rather than a megabyte
// each; the limit under test is 512, so the margin is not the point.
func TestAPodDoesNotDecideHowLongTheTrailsStringsAre(t *testing.T) {
	ctx, db, handler, owner := gatewayDeployment(t)

	report := func(runtime store.Runtime, server, tool, direction string, findings []map[string]any) map[string]any {
		return map[string]any{
			"runtimeId": runtime.ID,
			"event": map[string]any{
				"event": "dlp", "server": server, "tool": tool, "direction": direction,
				"blocked": false, "truncated": false, "findings": findings,
			},
		}
	}

	t.Run("과대 문자열은 상한에서 잘린다", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		huge := strings.Repeat("a", 130000)
		// A short finding the settings said to redact, so the outcome this report is
		// filed under is one that has to be read off the report the gateway sent
		// rather than the one the trail kept.
		redacted := map[string]any{"class": "rrn", "label": "주민등록번호", "count": 1, "action": dlp.Redact, "sample": "900101-*******"}
		oversized := map[string]any{"class": huge, "label": huge, "count": 2, "action": huge, "sample": huge}
		response := post(handler, token, report(runtime, huge, huge, huge, []map[string]any{redacted, oversized}))
		if response.Code != http.StatusAccepted {
			t.Fatalf("an oversized report was answered with %d: %s", response.Code, response.Body)
		}
		entry := trailEntry(ctx, t, db, runtime.AgentID)
		if entry["outcome"] != dlp.OutcomeRedacted {
			t.Errorf("filed as %q; the outcome is read off what the gateway reported, before anything is cut", entry["outcome"])
		}
		details, _ := entry["details"].(map[string]any)
		for _, key := range []string{"server", "tool", "direction"} {
			text, _ := details[key].(string)
			if runes := len([]rune(text)); runes > maxReportedTextLen {
				t.Errorf("the trail kept %d runes of the %q the Pod sent; the Pod is choosing the size of an audit row", runes, key)
			}
		}
		stored := storedFindings(t, details)
		if len(stored) != 2 {
			t.Fatalf("the trail kept %d of the 2 findings reported", len(stored))
		}
		for _, key := range []string{"class", "label", "action", "sample"} {
			text, _ := stored[1][key].(string)
			if runes := len([]rune(text)); runes > maxReportedTextLen {
				t.Errorf("the trail kept %d runes of a finding's %q", runes, key)
			}
		}
		// The finding an operator actually reads is well under the limit, so it is
		// the report, character for character.
		if stored[0]["action"] != dlp.Redact || stored[0]["sample"] != "900101-*******" || stored[0]["label"] != "주민등록번호" {
			t.Errorf("a finding well under the limit came back changed: %v", stored[0])
		}
	})

	// Cutting by bytes would leave half a Korean character in the row, and the
	// labels this platform ships are Korean.
	t.Run("절단은 룬 경계에서 일어난다", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		long, edge := strings.Repeat("한", 2000), strings.Repeat("한", maxReportedTextLen)
		finding := map[string]any{"class": "rrn", "label": long, "count": 1, "action": dlp.Audit, "sample": long}
		if response := post(handler, token, report(runtime, long, long, long, []map[string]any{finding})); response.Code != http.StatusAccepted {
			t.Fatalf("a multibyte report was answered with %d: %s", response.Code, response.Body)
		}
		entry := trailEntry(ctx, t, db, runtime.AgentID)
		details, _ := entry["details"].(map[string]any)
		stored := storedFindings(t, details)
		if len(stored) != 1 {
			t.Fatalf("the trail kept %d of the 1 finding reported", len(stored))
		}
		for name, text := range map[string]string{
			"server": asString(details["server"]), "tool": asString(details["tool"]), "direction": asString(details["direction"]),
			"finding.label": asString(stored[0]["label"]), "finding.sample": asString(stored[0]["sample"]),
		} {
			if !utf8.ValidString(text) {
				t.Errorf("%s came back from the trail with broken bytes in it", name)
				continue
			}
			if text != edge {
				t.Errorf("%s is %d runes of %q; the cut is not on a rune boundary", name, len([]rune(text)), firstRunes(text, 4))
			}
		}
	})

	// The limit is a ceiling, not a rewrite: a report inside it is stored as it was
	// sent, including one that lands exactly on the limit.
	t.Run("상한 이하 보고는 한 글자도 바뀌지 않는다", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		edge := strings.Repeat("한", maxReportedTextLen)
		finding := map[string]any{"class": "rrn", "label": "주민등록번호", "count": 1, "action": dlp.Audit, "sample": edge}
		if response := post(handler, token, report(runtime, edge, "create_issue", "요청", []map[string]any{finding})); response.Code != http.StatusAccepted {
			t.Fatalf("a report at the limit was answered with %d: %s", response.Code, response.Body)
		}
		entry := trailEntry(ctx, t, db, runtime.AgentID)
		if entry["outcome"] != dlp.OutcomeAudited {
			t.Errorf("filed as %q", entry["outcome"])
		}
		details, _ := entry["details"].(map[string]any)
		if details["server"] != edge || details["tool"] != "create_issue" || details["direction"] != "요청" {
			t.Errorf("a report at the limit was altered: server %d runes, tool %v, direction %v",
				len([]rune(asString(details["server"]))), details["tool"], details["direction"])
		}
		stored := storedFindings(t, details)
		if len(stored) != 1 || stored[0]["sample"] != edge || stored[0]["label"] != "주민등록번호" || stored[0]["action"] != dlp.Audit {
			t.Errorf("a finding at the limit was altered: %v", stored)
		}
	})

	// The count limit is older than the length limit and is not replaced by it.
	t.Run("발견 개수 상한은 그대로다", func(t *testing.T) {
		runtime, token := gatewayRuntime(ctx, t, db, owner)
		findings := make([]map[string]any, 0, maxReportedFindings+8)
		for i := 0; i < maxReportedFindings+8; i++ {
			findings = append(findings, map[string]any{"class": "rrn", "label": "주민등록번호", "count": 1, "action": dlp.Audit, "sample": "900101-*******"})
		}
		if response := post(handler, token, report(runtime, "jira", "create_issue", "요청", findings)); response.Code != http.StatusAccepted {
			t.Fatalf("a report of many findings was answered with %d: %s", response.Code, response.Body)
		}
		details, _ := trailEntry(ctx, t, db, runtime.AgentID)["details"].(map[string]any)
		if stored := storedFindings(t, details); len(stored) != maxReportedFindings {
			t.Errorf("the trail kept %d findings, want the %d the count limit allows", len(stored), maxReportedFindings)
		}
	})
}

// storedFindings is the findings as the database gave them back, so what is
// asserted is the audit row rather than anything the handler held.
func storedFindings(t *testing.T, details map[string]any) []map[string]any {
	t.Helper()
	raw, ok := details["findings"].([]any)
	if !ok {
		t.Fatalf("the entry's findings came back as %T: %v", details["findings"], details["findings"])
	}
	findings := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		finding, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("a finding came back as %T", item)
		}
		findings = append(findings, finding)
	}
	return findings
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func firstRunes(text string, n int) string {
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n])
	}
	return text
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
