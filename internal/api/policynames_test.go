package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/policy"
)

// What a policy rule naming something this deployment does not have means.
//
// A rule's effect, actions and roles are refused when they are wrong. The rest
// is free text, so a deny rule for an agent whose name is spelled differently
// matches nothing — for ever, silently, in the platform's own policy engine.

func TestARuleThatMatchesNothingSaysWhatItDoesNot(t *testing.T) {
	if got := ruleConsequence(policy.Deny); !strings.Contains(got, "차단하지 않습니다") {
		t.Errorf("a deny rule that matches nothing is described as %q", got)
	}
	if got := ruleConsequence(policy.RequireApproval); !strings.Contains(got, "승인 대기") {
		t.Errorf("an approval rule that matches nothing is described as %q", got)
	}
	if got := ruleConsequence(policy.Allow); !strings.Contains(got, "적용되지 않습니다") {
		t.Errorf("an allow rule that matches nothing is described as %q", got)
	}
	// The three must not read the same: what to do about them differs.
	if ruleConsequence(policy.Deny) == ruleConsequence(policy.Allow) {
		t.Error("a deny rule and an allow rule that match nothing are described identically")
	}
}

func TestTheNoticeNamesTheRuleAndWhatIsMissing(t *testing.T) {
	notice := policyNameNotice([]unmatchedName{
		{Rule: "block-payments", Kind: "에이전트", Name: "payment-bot", Means: ruleConsequence(policy.Deny)},
	})
	for _, needed := range []string{"block-payments", "payment-bot", "차단하지 않습니다"} {
		if !strings.Contains(notice, needed) {
			t.Errorf("the notice does not mention %q: %s", needed, notice)
		}
	}
	if policyNameNotice(nil) != "" {
		t.Error("a policy naming only things that exist was given a notice")
	}
	// A long list is trimmed rather than printed whole, and says that it was.
	many := []unmatchedName{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		many = append(many, unmatchedName{Rule: "r", Kind: "사용자", Name: name, Means: "x"})
	}
	if long := policyNameNotice(many); !strings.Contains(long, "…") {
		t.Errorf("a long list is printed whole rather than trimmed: %s", long)
	}
}

// TestAWildcardIsAPatternNotAName — reporting "github/*" as missing would report
// the feature as a fault.
func TestAWildcardIsAPatternNotAName(t *testing.T) {
	server := &Server{}
	known := map[string]map[string]bool{
		"agents": {"payment_bot": true},
	}
	_ = server
	// The filtering rule, exercised directly: a name with a star is skipped, a
	// literal one that is absent is reported.
	document := policy.Document{Rules: []policy.Rule{{
		ID: "r1", Effect: policy.Deny, Reason: "x",
		Agents: []string{"payment-*", "payment_bot", "ghost-bot"},
	}}}
	unmatched := unmatchedIn(document, known)
	if len(unmatched) != 1 {
		t.Fatalf("expected one unmatched name, got %d: %v", len(unmatched), unmatched)
	}
	if unmatched[0].Name != "ghost-bot" {
		t.Errorf("the wrong name was reported: %s", unmatched[0].Name)
	}
}

// TestAKindThatCouldNotBeReadIsNotReportedAsEmpty is the rule that bit the tool
// check a release ago: "we could not ask" and "there are none" produce opposite
// advice, and treating the first as the second reports every rule as broken.
func TestAKindThatCouldNotBeReadIsNotReportedAsEmpty(t *testing.T) {
	// Only agents could be read. The rule also names a user and a data class.
	known := map[string]map[string]bool{"agents": {"payment_bot": true}}
	document := policy.Document{Rules: []policy.Rule{{
		ID: "r1", Effect: policy.Deny, Reason: "x",
		Agents: []string{"payment_bot"}, Users: []string{"somebody"}, DataClasses: []string{"rrn"},
	}}}
	if unmatched := unmatchedIn(document, known); len(unmatched) != 0 {
		t.Errorf("kinds that could not be read were reported as missing: %v", unmatched)
	}
	// And when they can be read, a name that is absent is still reported.
	known["users"] = map[string]bool{"someone_else": true}
	unmatched := unmatchedIn(document, known)
	if len(unmatched) != 1 || unmatched[0].Name != "somebody" {
		t.Errorf("a user this deployment does not have was not reported: %v", unmatched)
	}
}
