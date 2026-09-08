package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/korean"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
)

// provenanceTimeout bounds one export. The dispatcher is a loop with other
// events waiting, and a sink that has stopped answering must not hold it.
const provenanceTimeout = 10 * time.Second

// exportDecision sends this platform's account of a finished task to whatever a
// deployment has configured to receive it.
//
// A deployment with no endpoint configured does nothing here — no request, no
// error, no log line — because that is almost every deployment.
//
// A failure is reported to the caller rather than swallowed: the dispatcher
// already knows how to keep an event pending, back off and eventually dead-letter
// it with somebody told. A record this platform decided to export and then lost
// quietly would be worse than one that arrives late.
func (d *Dispatcher) exportDecision(ctx context.Context, event store.PlatformEvent) error {
	// The endings the platform decides, including giving up: a task that ran out
	// of retries is the outcome an auditor asks about most, and it publishes
	// task.dead_lettered rather than task.failed. The list lives in the store so
	// that the screen advertising these endings cannot drift from the filter
	// applying them — it did, for one release.
	if !store.Exports(event.Type) {
		return nil
	}
	settings, err := d.store.ProvenanceEndpoint(ctx)
	if err != nil {
		return fmt.Errorf("결정 기록을 보낼 곳을 읽지 못했습니다: %w", err)
	}
	if !settings.Configured() {
		return nil
	}
	record, err := d.store.DecisionForTask(ctx, event.SubjectID)
	if err != nil {
		return fmt.Errorf("결정 기록을 만들지 못했습니다: %w", err)
	}
	outcome, err := SendDecision(ctx, settings, d.contentGuard(ctx, record), record)
	// Recorded once the send settled — sent, or refused here. A sink that
	// answered with an error is retried by the dispatcher, and an entry per
	// attempt would count one payload as many.
	var withheld WithheldError
	if err == nil || errors.As(err, &withheld) {
		recordContentScan(ctx, d.store, d.logger, scanEventExport, record.AgentID, outcome, map[string]any{
			"boundary": "결정 기록", "agent": record.Agent, "task": record.TaskID, "run": record.RunID,
		})
	}
	if err != nil {
		if errors.As(err, &withheld) {
			// Not retried: a record this deployment refuses to send will be refused
			// again, and the dispatcher's retry exists for sinks that come back. It
			// is loud instead — somebody is counting on these records arriving, so
			// one held back has to be visible where they will look for it.
			d.logger.Warn("decision withheld by content scan", "task", record.TaskID,
				"classes", withheld.Classes, "policyRule", outcome.Decision.RuleID)
			d.store.Audit(ctx, nil, "provenance.withheld", "task", record.TaskID, "blocked", "",
				map[string]any{"classes": withheld.Classes, "policyRule": outcome.Decision.RuleID})
			return nil
		}
		return err
	}
	d.logger.Info("decision exported", "task", record.TaskID, "type", event.Type, "outcome", record.Outcome)
	return nil
}

// SendDecision posts one record to the configured sink.
//
// It is one function so that the "보내보기" button on the settings screen exercises
// the request the dispatcher will make — the same address, the same header, the
// same timeout — rather than a second implementation that can agree with the
// screen and disagree with the deployment.
// What the scan found and what the policy decided come back with the error so
// the caller can record both. A finding the platform acted on and did not write
// down is one the operator reading the DLP trail cannot see, and a redaction is
// exactly as invisible as a block is loud.
func SendDecision(ctx context.Context, settings store.ProvenanceSettings, guard ContentGuard, record store.DecisionRecord) (ContentOutcome, error) {
	// Scanned and decided here rather than by the caller, because a caller can
	// forget. The export was one of two ways text left this deployment
	// uninspected — the other was the review comment posted back to a forge — and
	// a sending path added later must not be able to repeat that.
	record, result := scrubDecision(guard.Scan, record)
	outcome := guard.inspect(policy.ActionDecisionExport, result)
	if outcome.Refused() {
		return outcome, WithheldError{Subject: "결정 기록", Classes: result.Classes(), Labels: result.Labels(),
			Reason: outcome.Decision.Reason}
	}
	body, err := json.Marshal(record)
	if err != nil {
		return outcome, fmt.Errorf("결정 기록을 인코딩하지 못했습니다: %w", err)
	}
	send, cancel := context.WithTimeout(ctx, provenanceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(send, http.MethodPost, settings.Endpoint, bytes.NewReader(body))
	if err != nil {
		return outcome, fmt.Errorf("결정 기록을 보내지 못했습니다: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if settings.Header != "" && settings.Token != "" {
		request.Header.Set(settings.Header, settings.Token)
	}
	response, err := provenanceClient.Do(request)
	if err != nil {
		return outcome, fmt.Errorf("결정 기록을 보내지 못했습니다: %s", modelCallReasonLike(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return outcome, fmt.Errorf("결정 기록을 받는 쪽이 HTTP %d 로 답했습니다", response.StatusCode)
	}
	return outcome, nil
}

// contentGuard reads what this deployment allows out of the building: the
// scanner's settings, the central policy, and who this record belongs to.
//
// The record's owner is the task's owner — DecisionForTask reads t.owner_id —
// which is the person every other policy point on this platform decides about,
// and not the owner of the agent the task happened to run on.
//
// An unreadable setting scans nothing and decides nothing rather than blocking
// everything: the export is not the place to discover that the settings table is
// unavailable. An owner nobody can read is left empty and logged, which is the
// answer the model boundary and the runtime gate already give — a rule about a
// person then matches nothing, and the class's own action decides.
//
// The owner is read here rather than lazily behind the scan because an export
// happens once per finished task on a deployment that configured a sink, not
// once per model call: one query on that path costs nothing worth arranging
// around.
func (d *Dispatcher) contentGuard(ctx context.Context, record store.DecisionRecord) ContentGuard {
	guard := ContentGuard{Agent: record.Agent, AgentID: record.AgentID}
	if err := d.store.Setting(ctx, dlp.SettingKey, &guard.Scan); err != nil {
		guard.Scan = dlp.Settings{}
	}
	if err := d.store.Setting(ctx, policy.SettingKey, &guard.Policy); err != nil {
		guard.Policy = policy.Document{}
	}
	if owner := strings.TrimSpace(record.OwnerID); owner != "" {
		user, err := d.store.UserByID(ctx, owner)
		if err != nil {
			d.logger.Warn("the owner of this record is unreadable; user and role rules are not being applied",
				"owner", owner, "task", record.TaskID, "error", err)
		} else {
			guard.Owner = user
		}
	}
	return guard
}

// WithheldError says something was not sent because the deployment's content
// scanner found a class it is configured to block. It is a refusal, not a
// transport failure: retrying it changes nothing.
//
// Subject names what was held back, because there is more than one thing that
// leaves this deployment carrying free text and an operator reading a log needs
// to know which one stopped.
type WithheldError struct {
	Subject string
	// Classes is what the platform files the finding under — "rrn", "card" — and
	// it goes in the audit entry and the log, where a machine-readable name is
	// the point.
	Classes []string
	// Labels is what the same finding is called in the sentence somebody reads.
	// Kept apart from Classes because the first version of this message printed
	// the class id at a person: "결정 기록에 rrn 가 포함되어".
	Labels []string
	// Reason is what the operator wrote on the rule that refused, when a rule
	// refused rather than the class's own action. A refusal nobody can act on is
	// a support ticket, which is why the policy requires the sentence in the
	// first place — dropping it here would throw it away at the one moment it
	// was written for.
	Reason string
}

func (e WithheldError) Error() string {
	subject := e.Subject
	if subject == "" {
		subject = "보내려던 내용"
	}
	found := e.Labels
	if len(found) == 0 {
		found = e.Classes
	}
	// The particle follows the last word, which is whatever the scanner found —
	// 주민등록번호 takes 가 and 여권번호 takes 이, and picking one is picking wrong
	// half the time.
	names := strings.Join(found, ", ")
	message := subject + "에 " + names + korean.Subject(names) + " 포함되어 보내지 않았습니다"
	if e.Reason != "" {
		message += " — " + e.Reason
	}
	return message
}

// scrubDecision applies the content scanner to the free text in a record.
//
// The export is one of the ways text leaves this deployment — alongside the
// model call, the model's answer, the MCP tool call and the review comment
// posted back to a forge. It was not scanned, and the text it carries is the
// same text: the scenario is
// whatever a person typed as the title, and the reasoning quotes what ran. A
// deployment set to block national IDs in a prompt was posting them to an
// external address in the clear.
// The findings of every field are gathered into one result, because one export
// is one thing that happened: an entry per field would tell the operator the
// same record left three times.
func scrubDecision(settings dlp.Settings, record store.DecisionRecord) (store.DecisionRecord, dlp.Result) {
	scan := dlp.Result{}
	for _, field := range []struct {
		read  func() string
		write func(string)
	}{
		{func() string { return record.Scenario }, func(v string) { record.Scenario = v }},
		{func() string { return record.Reasoning }, func(v string) { record.Reasoning = v }},
		{func() string { return record.SourceURL }, func(v string) { record.SourceURL = v }},
	} {
		result := dlp.Scan(settings, field.read())
		field.write(result.Text)
		scan.Findings = append(scan.Findings, result.Findings...)
		scan.Blocked = scan.Blocked || result.Blocked
		scan.Truncated = scan.Truncated || result.Truncated
	}
	return record, scan
}

var provenanceClient = &http.Client{Timeout: provenanceTimeout}

// modelCallReasonLike keeps what an operator can act on out of a transport
// error and drops the rest, the same way the workflow engine does with its own.
func modelCallReasonLike(err error) string {
	message := err.Error()
	if at := lastIndex(message, ": "); at >= 0 && len(message)-at < 80 {
		return message[at+2:]
	}
	return message
}

func lastIndex(value, sep string) int {
	for i := len(value) - len(sep); i >= 0; i-- {
		if value[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
