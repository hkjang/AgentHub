package execution

import (
	"context"
	"log/slog"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/store"
)

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
func recordContentScan(ctx context.Context, db *store.Store, logger *slog.Logger, event, agentID string, result dlp.Result, details map[string]any) {
	if db == nil || len(result.Findings) == 0 {
		return
	}
	entry := map[string]any{"findings": result.Findings, "truncated": result.Truncated}
	for key, value := range details {
		entry[key] = value
	}
	// On a context of its own: these boundaries are the last thing a finished
	// task does, and an entry about text that already left must not be lost
	// because the run it belonged to was over.
	record, cancel := recordContext(ctx)
	defer cancel()
	db.Audit(record, nil, event, "agent", agentID, result.Outcome(), "", entry)
	if logger != nil {
		logger.Warn("sensitive data found leaving the platform",
			"boundary", event, "agent", agentID, "outcome", result.Outcome(),
			"classes", result.Summary(), "truncated", result.Truncated)
	}
}
