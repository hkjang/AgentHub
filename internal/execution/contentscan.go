package execution

import (
	"context"
	"log/slog"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
)

// ContentGuard is what a sending path is given so that it can decide, and not
// merely scan.
//
// The scanner says what is in the text; the central policy says what this
// deployment does about it, and a rule there can be narrower than the global
// action for a class — one role, one person, one agent. That is the whole reason
// the two are separate, and it is how the model boundary has always worked. The
// two paths that leave the building for good read only the scanner's settings,
// so "계약직의 텍스트는 밖으로 나갈 수 없다" was written, saved, shown in the rule
// editor, answered 차단 by the simulator — and applied at every boundary except
// the two that actually publish.
type ContentGuard struct {
	// Scan is what the detectors look for and what this deployment does about
	// each class on its own.
	Scan dlp.Settings
	// Policy is the central rule set.
	Policy policy.Document
	// Agent and AgentID are what produced the text. A rule may name either, so
	// both are carried: the display name is what an operator writes and the id is
	// what survives a rename.
	Agent   string
	AgentID string
	// Owner is who the run belongs to. An owner nobody could read is left empty,
	// the same answer the model boundary and the runtime gate give — a database
	// that briefly cannot answer must not decide by itself that nothing may be
	// published.
	Owner store.User
}

// ContentOutcome is what the inspection found and what the platform decided
// about it, returned together because the caller holding the database has to
// write down both. The audit trail names the rule for the same reason every
// other decision point does: a policy nobody can prove was applied is one that
// gets argued about after an incident rather than before one.
type ContentOutcome struct {
	Scan     dlp.Result
	Decision policy.Decision
}

// Refused reports whether this text must not be sent — because the scanner's
// own action for the class is 차단, or because a rule said so. A rule asking for
// approval refuses here too: neither of these boundaries has anywhere for a
// reviewer to wait, and reading "somebody must look at this first" as an allow
// would be the worst available reading of it.
func (o ContentOutcome) Refused() bool {
	return o.Scan.Blocked || (o.Decision.Effect != "" && o.Decision.Effect != policy.Allow)
}

// Outcome is what the audit entry records. The scanner says what it did to the
// text; a rule can refuse text the scanner itself only recorded, so a refusal
// has the last word over both.
func (o ContentOutcome) Outcome() string {
	if o.Refused() {
		return dlp.OutcomeBlocked
	}
	return o.Scan.Outcome()
}

// inspect asks the policy what this deployment does about what the scanner
// found.
//
// Only when the scanner found something, which is how the model boundary asks
// as well. A rule that names data classes cannot match a request carrying none,
// and consulting the document on every clean send would let a rule with no
// action selector — which means "every action" — start refusing exports in
// deployments that wrote it about something else entirely.
func (g ContentGuard) inspect(action string, result dlp.Result) ContentOutcome {
	outcome := ContentOutcome{Scan: result, Decision: policy.Decision{Effect: policy.Allow}}
	if len(result.Findings) == 0 {
		return outcome
	}
	outcome.Decision = policy.Evaluate(g.Policy, policy.Request{
		Action: action, Agent: g.Agent, AgentID: g.AgentID,
		Role: g.Owner.Role, User: g.Owner.Username, UserID: g.Owner.ID,
		DataClasses: result.Classes(),
	})
	return outcome
}

// The audit actions the content scan is recorded under at the boundaries the
// control plane sends text from. They sit beside dlp.model and dlp.flow, which
// the model boundary writes, and dlp.tool, which the in-Pod gateway reports —
// the DLP settings screen names all of them, and a test here checks that it
// still does.
const (
	// scanEventExport is the decision record posted to whatever address a
	// deployment configured to receive it.
	scanEventExport = "dlp.export"
	// scanEventReview is the review comment written on somebody else's pull
	// request.
	scanEventReview = "dlp.review"
)

// recordContentScan writes what a scan found on text leaving the building.
//
// Every other boundary already does this. The model call, the flow run and the
// tool call each leave a dlp.* entry carrying the class, the count, the action
// and the masked sample the scanner produced. These two scanned their text and
// then recorded nothing unless they refused it — so a class set to 기록만, which
// is the documented way a site learns what its agents really handle before it
// starts blocking anything, produced no trail at all for the decision record
// posted to an external address, and none for the review comment written on a
// forge this deployment does not own. Both of those leave the building
// completely, and the operator deciding whether to move a class from 기록만 to
// 가리고 전송 was reading an empty page about them. Redaction was just as quiet:
// the text was rewritten on its way out and nothing said so.
//
// Nothing is recorded when the scan found nothing, which is almost every send.
// What is recorded is what the scanner reports and never the value itself, for
// the reason the scanner masks it in the first place.
func recordContentScan(ctx context.Context, db *store.Store, logger *slog.Logger, event, agentID string, outcome ContentOutcome, details map[string]any) {
	result := outcome.Scan
	if db == nil || len(result.Findings) == 0 {
		return
	}
	// policyRule beside the findings, the way the model boundary records it: a
	// send the central policy refused and one the scanner refused look identical
	// in a trail that does not say which rule spoke.
	entry := map[string]any{"findings": result.Findings, "truncated": result.Truncated,
		"policyRule": outcome.Decision.RuleID}
	for key, value := range details {
		entry[key] = value
	}
	// On a context of its own: these boundaries are the last thing a finished
	// task does, and an entry about text that already left must not be lost
	// because the run it belonged to was over.
	record, cancel := recordContext(ctx)
	defer cancel()
	db.Audit(record, nil, event, "agent", agentID, outcome.Outcome(), "", entry)
	if logger != nil {
		logger.Warn("sensitive data found leaving the platform",
			"boundary", event, "agent", agentID, "outcome", outcome.Outcome(),
			"classes", result.Summary(), "truncated", result.Truncated,
			"policyRule", outcome.Decision.RuleID)
	}
}
