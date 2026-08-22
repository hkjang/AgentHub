package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// A password nobody could change.
//
// The local admin's password is written once, from
// AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD, on the first start of a deployment. Nothing
// ever wrote password_hash again — not the product, not a restart with a
// different value in the manifest. That manifest is the kind of file that
// reaches a git history, a CI log or a shared runbook, and on an offline site
// this account is the only way in.
//
// This rotates the deployment's own password and puts it back, which is the only
// honest way to check that a rotation works. If it stops halfway the deployment
// is left on the second password, and the test says so loudly enough to fix by
// hand.
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run PasswordRotation -v
func TestPasswordRotationEndsTheOtherSessions(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check password rotation against a running deployment")
	}
	user, original := os.Getenv("AGENTHUB_TEST_USER"), os.Getenv("AGENTHUB_TEST_PASSWORD")
	interim := original + "-rotated-by-a-test"

	// Two sessions: the one that changes the password, and one that should be
	// signed out by it. A rotation that leaves the old password's browsers logged
	// in is a gesture.
	changer := loginAs(t, base, user, original)
	elsewhere := loginAs(t, base, user, original)
	if _, status := elsewhere.get("/api/v1/me"); status != http.StatusOK {
		t.Fatalf("the second session was not usable to begin with (%d); nothing was proved", status)
	}

	raw, status := changer.do(http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": original, "newPassword": interim,
	})
	if status != http.StatusOK {
		t.Fatalf("the password could not be changed (%d): %s", status, first(raw, 200))
	}
	restored := false
	defer func() {
		if !restored {
			t.Errorf("THIS DEPLOYMENT IS NOW ON THE PASSWORD %q — put it back by hand", interim)
		}
	}()

	if _, status := changer.get("/api/v1/me"); status != http.StatusOK {
		t.Error("the session that changed the password was signed out by it; doing the right thing threw the person out")
	}
	if _, status := elsewhere.get("/api/v1/me"); status == http.StatusOK {
		t.Error("a session opened with the old password still works; the rotation ended nothing")
	}

	// The old password must be dead, and the answer must not leak which part was
	// wrong.
	body, status := postLogin(t, base, user, original)
	if status == http.StatusOK {
		t.Fatalf("the old password still logs in: %s", first(body, 120))
	}
	if _, status := postLogin(t, base, user, interim); status != http.StatusOK {
		t.Fatalf("the new password does not log in either (%d) — the account may be unreachable", status)
	}

	back := loginAs(t, base, user, interim)
	if raw, status := back.do(http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": interim, "newPassword": original,
	}); status != http.StatusOK {
		t.Fatalf("the password could not be put back (%d): %s", status, first(raw, 200))
	}
	restored = true
	if _, status := postLogin(t, base, user, original); status != http.StatusOK {
		t.Fatalf("the original password does not work after being restored (%d)", status)
	}
	t.Log("rotated and restored: the old password stopped working, other sessions ended, the changing session survived")
}

// postLogin tries one login without disturbing the caller's cookies.
func postLogin(t *testing.T, base, username, password string) (string, int) {
	t.Helper()
	client := &apiClient{base: base, http: &http.Client{}}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	return client.do(http.MethodPost, "/api/v1/auth/login", json.RawMessage(payload))
}
