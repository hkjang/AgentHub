package api

import (
	"strings"
	"testing"
)

// What a policy naming a tool that does not exist means.
//
// Everything else about a policy was checked at save time except the field that
// decides whether it does anything. The three modes fail in three different
// directions, and the message has to say which — "this tool is not there" is not
// the news; "nothing is being blocked" is.
func TestAPolicyNoticeSaysWhatWillHappen(t *testing.T) {
	unknown := []unknownTool{{Name: "execute_command", DidYouMean: "run_command"}}

	deny := policyToolNotice("deny", unknown, false)
	if !strings.Contains(deny, "차단되지 않습니다") {
		t.Errorf("a deny rule for a tool that does not exist does not say that nothing is blocked: %s", deny)
	}
	allow := policyToolNotice("allow", unknown, false)
	if !strings.Contains(allow, "계속 막힙니다") {
		t.Errorf("an allow rule for a tool that does not exist does not say the tool stays blocked: %s", allow)
	}
	gate := policyToolNotice("allow", unknown, true)
	if !strings.Contains(gate, "승인 대기") {
		t.Errorf("a gate on a tool that does not exist does not say nothing will be gated: %s", gate)
	}
	// And the nearest name is offered, because a typo's neighbour is the answer.
	if !strings.Contains(deny, "run_command") {
		t.Errorf("the notice does not offer the tool that was probably meant: %s", deny)
	}
	if policyToolNotice("deny", nil, false) != "" {
		t.Error("a policy that names only real tools was given a notice")
	}
}

// TestASuggestionIsOnlyMadeWhenItIsNearlyRight — a suggestion that is not nearly
// right invites somebody to accept a tool they did not mean, which is worse than
// offering none.
func TestASuggestionIsOnlyMadeWhenItIsNearlyRight(t *testing.T) {
	offered := []string{"run_command", "read_file", "write_file"}
	if got := nearestTool("run_commnd", offered); got != "run_command" {
		t.Errorf("a one-letter typo was not recognised: %q", got)
	}
	if got := nearestTool("delete_everything", offered); got != "" {
		t.Errorf("an unrelated name was matched to %q", got)
	}
	// Case alone is not a difference worth a suggestion of something else.
	if got := nearestTool("Read_File", offered); got != "read_file" {
		t.Errorf("a case difference was not recognised: %q", got)
	}
}

func TestEditDistanceCountsCharactersNotBytes(t *testing.T) {
	// A Korean tool name differing by one character is one edit away, not three.
	if got := editDistance("파일읽기", "파일쓰기"); got != 1 {
		t.Errorf("distance between two Korean names is %d, want 1", got)
	}
	if got := editDistance("", "abc"); got != 3 {
		t.Errorf("distance from empty is %d, want 3", got)
	}
}

// TestNoToolListMeansNoComplaint keeps the check honest about what it knows.
//
// A server that answers without listing any tools has not said the named ones
// are wrong. Reading that as "none of these exist" would report every policy on
// that server as broken, which is the same invented bad news this platform keeps
// removing.
func TestNoToolListMeansNoComplaint(t *testing.T) {
	if got := unknownAmong([]string{"run_command"}, nil); got != nil {
		t.Errorf("a server that listed nothing produced %d complaint(s)", len(got))
	}
	if got := unknownAmong(nil, []string{"run_command"}); len(got) != 0 {
		t.Errorf("a policy naming no tools produced %d complaint(s)", len(got))
	}
	// And a real mismatch is still found, in a stable order.
	got := unknownAmong([]string{"zzz_tool", "aaa_tool", "run_command"}, []string{"run_command"})
	if len(got) != 2 {
		t.Fatalf("expected two unknown tools, got %d", len(got))
	}
	if got[0].Name != "aaa_tool" {
		t.Errorf("the unknown tools are not in a stable order: %v", got)
	}
}
