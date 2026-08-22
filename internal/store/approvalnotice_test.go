package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Asking for an approval and telling nobody about it is the same as not asking.
//
// The reviewer on an approval is the requester's manager, and manager_id is an
// org-chart field most deployments never fill in. Both places that request an
// approval notified the reviewer only when there was one, so with no org chart the
// task parked at the gate, the requester was told it was waiting, and the approval
// sat in a queue that only an administrator could see — and only if they thought
// to look. Nothing anywhere said "nobody was told".
//
// So no caller may write that condition itself: they go through NotifyApprovers,
// which falls back to the administrators who are allowed to decide it.
func TestNobodyAsksForAnApprovalAndTellsNobody(t *testing.T) {
	byHand := regexp.MustCompile(`ReviewerID != nil`)
	requests := regexp.MustCompile(`CreateApproval\(`)
	sites := 0
	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == "node_modules" || name == ".git" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		source := string(body)
		for _, at := range requests.FindAllStringIndex(source, -1) {
			if strings.HasPrefix(source[strings.LastIndex(source[:at[0]], "\n")+1:at[0]], "func ") {
				continue
			}
			sites++
			window := source[at[1]:min(len(source), at[1]+1200)]
			if !strings.Contains(window, "NotifyApprovers(") {
				t.Errorf("%s asks for an approval without going through NotifyApprovers; on a deployment with no org chart nobody hears about it", path)
			}
			if byHand.MatchString(window) {
				t.Errorf("%s decides for itself whether an approval has a reviewer; that is the check that left nobody notified", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 2 {
		t.Fatalf("only %d approval request(s) found; this guard is not reading the tree", sites)
	}
}

// And the fallback has to be people who can actually answer. An administrator can
// decide an unassigned approval — DecideApproval allows it — and nobody else can,
// so anybody else would be a notification that leads to a button its reader is
// refused by.
func TestTheApprovalFallbackGoesToWhoeverCanDecide(t *testing.T) {
	body, err := os.ReadFile("approval.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) NotifyApprovers(")
	if at < 0 {
		t.Fatal("NotifyApprovers is gone; the guard above is checking for nothing")
	}
	fallback := source[at:]
	if end := strings.Index(fallback, "\n}\n"); end >= 0 {
		fallback = fallback[:end]
	}
	if !strings.Contains(fallback, `role='admin'`) {
		t.Error("the fallback no longer picks administrators; whoever it picks must be allowed to decide an unassigned approval")
	}
	if !strings.Contains(fallback, `status='active'`) {
		t.Error("the fallback notifies deactivated accounts; a notification nobody can read is the state this exists to prevent")
	}
}
