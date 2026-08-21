package store

import (
	"os"
	"strings"
	"testing"
)

// A column added to userColumns and forgotten in one of the scans does not fail
// in a way anybody would recognise. The login query reads the user columns
// alongside the password hash; when its scan is one target short the row does
// not decode, the hash stays nil, and the platform answers "아이디 또는 비밀번호를
// 확인해 주세요" to a correct password. Every account, at once.
func TestColumnListsAndScanTargetsAgree(t *testing.T) {
	var u User
	var r AgentRun
	for _, tc := range []struct {
		name    string
		columns string
		targets int
	}{
		{"userColumns", userColumns, len(u.scanTargets())},
		{"runColumns", runColumns, len(r.scanTargets())},
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
		} {
			if strings.Contains(string(body), byHand) {
				t.Errorf("%s scans a shared column list by hand (%s…); use scanTargets() so a new column reaches it", name, byHand[:24])
			}
		}
	}
}
