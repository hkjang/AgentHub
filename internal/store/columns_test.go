package store

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A column added to userColumns and forgotten in one of the scans does not fail
// in a way anybody would recognise. The login query reads the user columns
// alongside the password hash; when its scan is one target short the row does
// not decode, the hash stays nil, and the platform answers "아이디 또는 비밀번호를
// 확인해 주세요" to a correct password. Every account, at once.
func TestColumnListsAndScanTargetsAgree(t *testing.T) {
	var u User
	var r AgentRun
	var t2 AgentTask
	for _, tc := range []struct {
		name    string
		columns string
		targets int
	}{
		{"userColumns", userColumns, len(u.scanTargets())},
		{"runColumns", runColumns, len(r.scanTargets())},
		{"taskOwnColumns", taskOwnColumns, len(t2.scanTargets())},
	} {
		if want := len(strings.Split(tc.columns, ",")); tc.targets != want {
			t.Errorf("%s has %d columns but its scanTargets reads %d; add the new column to both", tc.name, want, tc.targets)
		}
	}
}

// A query that cannot use the shared scanner must still go through scanTargets.
// Spelling the fields out again is how the login query drifted, and then the run
// insert after it — the same mistake twice, in two different column lists.
func TestNobodyScansASharedColumnListByHand(t *testing.T) {
	for _, name := range []string{"store.go", "secrets.go", "admin.go", "execution.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, byHand := range []string{
			"&user.ID, &user.Username, &user.Email",
			"&run.ID, &run.TaskID, &run.AgentID",
			"&item.ID, &item.TaskID, &item.AgentID, &item.OwnerID, &item.Attempt, &item.Status",
			"&item.ID, &item.AgentID, &item.OwnerID, &item.Title, &item.Input",
		} {
			if strings.Contains(string(body), byHand) {
				t.Errorf("%s scans a shared column list by hand (%s…); use scanTargets() so a new column reaches it", name, byHand[:24])
			}
		}
	}
}

// Postgres reports a reused name as `duplicate key value violates unique
// constraint "agent_definitions_owner_id_name_key" (SQLSTATE 23505)`, and that
// string went to the person who had simply picked a name somebody else's agent
// already had — a schema detail, in English, presented as a platform failure.
func TestAReusedNameIsAConflictAndNotAServerError(t *testing.T) {
	taken := &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "agent_definitions_owner_id_name_key"`}
	err := conflictIfTaken(taken, "같은 이름의 에이전트가 이미 있습니다")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a unique violation was not recognised: %v", err)
	}
	if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique constraint") {
		t.Errorf("the database's own words reached the message: %q", err.Error())
	}
	// err.Error() is printed by more places than the one that classifies it, so
	// the sentinel's own word must not be part of the sentence.
	if err.Error() != "같은 이름의 에이전트가 이미 있습니다" {
		t.Errorf("the message carries something besides itself: %q", err.Error())
	}
	// Everything else has to pass through untouched, including nil: a caller that
	// wrapped every failure as a name collision would hide real ones.
	other := errors.New("connection refused")
	if got := conflictIfTaken(other, "이름 중복"); !errors.Is(got, other) || errors.Is(got, ErrConflict) {
		t.Errorf("an unrelated failure was relabelled: %v", got)
	}
	if conflictIfTaken(nil, "이름 중복") != nil {
		t.Error("success was turned into a failure")
	}
}
