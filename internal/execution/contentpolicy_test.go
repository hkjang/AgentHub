package execution

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
)

const contractorRRN = "900101-1234568"

// auditedRRN is the setting a site runs while it is still learning what its
// agents handle: the class is recorded and the text goes out untouched. It is
// the interesting case here, because it is what leaves the central policy as the
// only thing that can refuse.
func auditedRRN() dlp.Settings {
	return dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Audit}}
}

// contractorsMayNotPublish is the sentence an operator writes about the two
// paths that leave the building for good.
func contractorsMayNotPublish(effect string) policy.Document {
	return policy.Document{Rules: []policy.Rule{{
		ID:          "no-external-publishing-by-contractors",
		Effect:      effect,
		Actions:     []string{policy.ActionDecisionExport, policy.ActionReviewComment},
		Roles:       []string{"contractor"},
		DataClasses: []string{"rrn"},
		Reason:      "계약직이 다룬 내용은 외부로 내보낼 수 없습니다.",
	}}}
}

// eachOutwardBoundary is the two paths that put text on a machine this
// deployment does not own, each with the server that would receive it.
func eachOutwardBoundary(t *testing.T) []struct {
	name     string
	send     func(ContentGuard) (ContentOutcome, error)
	received func() string
} {
	t.Helper()

	var arrived string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		arrived = string(body)
	}))
	t.Cleanup(sink.Close)
	record := store.DecisionRecord{TaskID: "t1", Agent: "환급 심사", AgentID: "agent-1",
		Scenario: "민원인 " + contractorRRN + " 환급 검토"}

	var commented string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		commented = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(forge.Close)
	connection := store.SCMConnection{Kind: "gitea", Host: strings.TrimPrefix(forge.URL, "http://"),
		APIBase: forge.URL + "/api/v1"}

	return []struct {
		name     string
		send     func(ContentGuard) (ContentOutcome, error)
		received func() string
	}{
		{"결정 기록", func(guard ContentGuard) (ContentOutcome, error) {
			arrived = ""
			return SendDecision(context.Background(), store.ProvenanceSettings{Endpoint: sink.URL}, guard, record)
		}, func() string { return arrived }},
		{"리뷰 코멘트", func(guard ContentGuard) (ContentOutcome, error) {
			commented = ""
			return PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
				forge.URL+"/acme/store/pulls/1", "리뷰 대상 코드에 "+contractorRRN+" 이 있습니다", guard)
		}, func() string { return commented }},
	}
}

// A rule narrower than the global action for a class is the whole reason the
// central policy and the scanner are separate things. It decides at the model
// call and at the flow run; at the two boundaries that publish outside the
// building it decided nowhere, because they read only the scanner's settings.
//
// So "계약직이 다룬 내용은 외부로 내보낼 수 없습니다" — written, saved, listed in the
// rule editor, answered 차단 by the simulator — let the resident registration
// number through to an external address and onto somebody else's pull request,
// while the class's own action recorded a finding and passed the text on.
//
// This is the two senders given a guard. Who ends up in that guard is a
// separate question and a separate mistake, and it is checked against a real
// database in contentpolicy_live_test.go.
func TestTheTwoSendersRefuseWhatTheRuleRefuses(t *testing.T) {
	for _, boundary := range eachOutwardBoundary(t) {
		t.Run(boundary.name, func(t *testing.T) {
			guard := ContentGuard{Scan: auditedRRN(), Policy: contractorsMayNotPublish(policy.Deny),
				Agent: "환급 심사", AgentID: "agent-1", Owner: store.User{ID: "u1", Username: "kim", Role: "contractor"}}

			outcome, err := boundary.send(guard)
			if err == nil {
				t.Fatal("the rule was written, saved and shown in force, and the text went out anyway")
			}
			var withheld WithheldError
			if !asWithheld(err, &withheld) {
				t.Fatalf("the refusal does not read as one this deployment made: %v", err)
			}
			if !strings.Contains(err.Error(), "계약직이 다룬 내용은 외부로 내보낼 수 없습니다.") {
				t.Errorf("the refusal drops the sentence the operator wrote on the rule: %v", err)
			}
			if got := boundary.received(); strings.Contains(got, contractorRRN) {
				t.Errorf("the value reached the other end: %s", got)
			}
			if outcome.Decision.RuleID != "no-external-publishing-by-contractors" {
				t.Errorf("the trail cannot say which rule refused: %+v", outcome.Decision)
			}
			if outcome.Outcome() != dlp.OutcomeBlocked {
				t.Errorf("a send the policy refused is recorded as %q", outcome.Outcome())
			}

			// The same text, the same class, somebody else. A rule about one role
			// that refuses everybody is not narrower than the global action, it is
			// the global action with extra steps.
			guard.Owner = store.User{ID: "u2", Username: "park", Role: "engineer"}
			sent, err := boundary.send(guard)
			if err != nil {
				t.Fatalf("a rule about contractors refused an engineer: %v", err)
			}
			if got := boundary.received(); !strings.Contains(got, contractorRRN) {
				t.Errorf("nothing arrived for somebody no rule named: %s", got)
			}
			if sent.Outcome() != dlp.OutcomeAudited || len(sent.Scan.Findings) == 0 {
				t.Errorf("the finding stopped being recorded for the send that went through: %+v", sent)
			}
		})
	}
}

// A rule asking for approval refuses here, and says why. Neither boundary has
// anywhere for a reviewer to wait — the export is the dispatcher finishing with
// a task, the comment is the last thing a review does — and reading "somebody
// must look at this first" as an allow would be the worst available reading of
// the rule that asked for it.
func TestARuleAskingForApprovalRefusesWhereNobodyCanWait(t *testing.T) {
	for _, boundary := range eachOutwardBoundary(t) {
		t.Run(boundary.name, func(t *testing.T) {
			_, err := boundary.send(ContentGuard{
				Scan:   auditedRRN(),
				Policy: contractorsMayNotPublish(policy.RequireApproval),
				Owner:  store.User{Role: "contractor"},
			})
			if err == nil {
				t.Fatal("a rule asking for review was read as an allow at a boundary with no reviewer")
			}
			if got := boundary.received(); strings.Contains(got, contractorRRN) {
				t.Errorf("the text went out while waiting for a reviewer nobody will fetch: %s", got)
			}
		})
	}
}

// The policy is asked about text the scanner found something in, and only then —
// which is how the model boundary asks as well.
//
// A rule that names no action means every action, and deployments have written
// those about tools and tasks. Consulting the document on every clean send would
// turn one of those into a deployment that silently stopped exporting anything,
// with a default policy of 차단 doing it without a rule at all.
func TestACleanSendIsNotDecidedByThePolicy(t *testing.T) {
	for _, boundary := range eachOutwardBoundary(t) {
		t.Run(boundary.name, func(t *testing.T) {
			if _, err := boundary.send(ContentGuard{
				Policy: policy.Document{DefaultEffect: policy.Deny},
				Owner:  store.User{Role: "contractor"},
			}); err != nil {
				t.Fatalf("a deployment that scans nothing stopped sending: %v", err)
			}
			if boundary.received() == "" {
				t.Error("nothing arrived from a send nobody scanned and no rule named")
			}
		})
	}
}
