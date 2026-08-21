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
func TestUserColumnsAndScanTargetsAgree(t *testing.T) {
	var u User
	if got, want := len(u.scanTargets()), len(strings.Split(userColumns, ",")); got != want {
		t.Fatalf("userColumns has %d columns but scanTargets reads %d; add the new column to both", want, got)
	}
}

// The two queries that cannot use scanUser must still go through scanTargets.
// Spelling the fields out again is how the login query drifted in the first place.
func TestNobodyScansTheUserColumnsByHand(t *testing.T) {
	for _, name := range []string{"store.go", "secrets.go", "admin.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "&user.ID, &user.Username, &user.Email") {
			t.Errorf("%s scans the user columns by hand; use user.scanTargets() so a new column reaches it", name)
		}
	}
}
