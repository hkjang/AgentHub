package execution

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/store"
)

// A review comment carries text a model wrote while quoting somebody's code, and
// it goes to a host this deployment does not own. When the deployment's scanner
// is set to block a class and the comment carries it, nothing may leave — and
// the credential that was never used must not be marked as the thing that failed,
// because that sends somebody to re-issue a working token.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestAReviewBlockedByTheScannerLeavesTheForgeAlone(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	ctx := context.Background()
	cipher, err := testCipher()
	if err != nil {
		t.Skip("no encryption key to read a stored credential with")
	}
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner := anyUser(ctx, t, db)
	restore := blockRRNForThisTest(ctx, t, db, owner)
	defer restore()

	reached := false
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	host := strings.TrimPrefix(forge.URL, "http://")

	connection, err := db.PutSCMConnection(ctx, owner, host, "gitea", forge.URL+"/api/v1", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.DeleteSCMConnection(ctx, owner, connection.ID) }()

	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	task := store.AgentTask{SourceURL: forge.URL + "/acme/store/pulls/1"}
	findings := []store.ReviewFinding{{
		FilePath: "app/pay.py", StartLine: 5, Severity: "high",
		Message: "테스트 픽스처에 실제 주민등록번호 900101-1234568 가 그대로 들어 있습니다.",
	}}
	orchestrator.announceReview(ctx, store.AgentRun{}, task, owner, "지적 1건", findings)

	if reached {
		t.Fatal("a comment the scanner blocked was posted to the forge anyway")
	}
	after, _, err := db.SCMTokenFor(ctx, owner, host)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastError != "" {
		t.Errorf("the forge was blamed for text this deployment chose not to send: %q", after.LastError)
	}
	if after.LastUsedAt != nil {
		t.Error("a credential that was never sent anywhere is recorded as used")
	}
}

// blockRRNForThisTest sets the deployment's scanner to block national IDs and
// hands back what puts it as it was, so a check does not leave a live database
// configured by a test.
func blockRRNForThisTest(ctx context.Context, t *testing.T, db *store.Store, actor string) func() {
	t.Helper()
	var before dlp.Settings
	had := db.Setting(ctx, dlp.SettingKey, &before) == nil
	blocking := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}
	if err := db.PutSetting(ctx, dlp.SettingKey, blocking, nil, actor); err != nil {
		t.Skipf("this deployment's content settings cannot be written: %v", err)
	}
	return func() {
		if !had {
			before = dlp.Settings{}
		}
		if err := db.PutSetting(ctx, dlp.SettingKey, before, nil, actor); err != nil {
			t.Errorf("the deployment's content settings were left as the test set them: %v", err)
		}
	}
}
