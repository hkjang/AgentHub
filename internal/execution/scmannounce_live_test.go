package execution

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/store"
)

// The poster is checked on its own elsewhere. This is the wiring: a finished
// review, a task that came from a pull request, and a credential stored for that
// host — the three things that have to meet for anybody outside AgentHub to read
// what the review found.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestAFinishedReviewReachesThePullRequest(t *testing.T) {
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

	var posted string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posted = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	host := strings.TrimPrefix(forge.URL, "http://")

	owner := anyUser(ctx, t, db)
	connection, err := db.PutSCMConnection(ctx, owner, host, "gitea", forge.URL+"/api/v1", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.DeleteSCMConnection(ctx, owner, connection.ID) }()

	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	task := store.AgentTask{SourceURL: forge.URL + "/acme/store/pulls/1"}
	findings := []store.ReviewFinding{
		{FilePath: "internal/api/review.go", StartLine: 42, Severity: "high", Message: "빠뜨린 오류 처리"},
	}
	orchestrator.announceReview(ctx, store.AgentRun{}, task, owner, "지적 1건", findings)

	if posted == "" {
		t.Fatal("the review did not reach the pull request")
	}
	for _, want := range []string{"지적 1건", "internal/api/review.go:42", "빠뜨린 오류 처리"} {
		if !strings.Contains(posted, want) {
			t.Errorf("the comment does not carry %q: %s", want, posted)
		}
	}
	after, _, err := db.SCMTokenFor(ctx, owner, host)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastUsedAt == nil {
		t.Error("a connection that was just used has no record of it")
	}
	if after.LastError != "" {
		t.Errorf("a comment that was accepted left an error: %q", after.LastError)
	}
}

// A task from a host nobody stored a credential for posts nothing, and says
// nothing: posting back is opted into by storing a token.
func TestAReviewWithNoCredentialSaysNothingAnywhere(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	ctx := context.Background()
	cipher, err := testCipher()
	if err != nil {
		t.Skip("no encryption key")
	}
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reached := false
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	orchestrator.announceReview(ctx, store.AgentRun{}, store.AgentTask{SourceURL: forge.URL + "/acme/store/pulls/1"},
		anyUser(ctx, t, db), "지적 없음", nil)
	if reached {
		t.Fatal("a comment was posted to a host nobody stored a credential for")
	}
}

// testCipher reads the deployment's key the way the server does.
func testCipher() (*cryptox.Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("AGENTHUB_ENCRYPTION_KEY"))
	if err != nil {
		return nil, err
	}
	return cryptox.New(key)
}

func anyUser(ctx context.Context, t *testing.T, db *store.Store) string {
	t.Helper()
	users, err := db.Users(ctx)
	if err != nil || len(users) == 0 {
		t.Skip("this deployment has no user to own a connection")
	}
	return users[0].ID
}

// A task the platform started itself has no page to speak on. Reaching a forge
// anyway would mean commenting on whatever page a stale address happened to name.
func TestAReviewWithNoSourcePageSpeaksNowhere(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	ctx := context.Background()
	cipher, err := testCipher()
	if err != nil {
		t.Skip("no encryption key")
	}
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reached := false
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	owner := anyUser(ctx, t, db)
	host := strings.TrimPrefix(forge.URL, "http://")
	connection, err := db.PutSCMConnection(ctx, owner, host, "gitea", forge.URL+"/api/v1", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.DeleteSCMConnection(ctx, owner, connection.ID) }()

	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	orchestrator.announceReview(ctx, store.AgentRun{}, store.AgentTask{}, owner, "지적 없음", nil)
	if reached {
		t.Fatal("a review with no source page still commented somewhere")
	}
}
