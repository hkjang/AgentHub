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
	if err := SendDecision(ctx, settings, d.contentSettings(ctx), record); err != nil {
		var withheld WithheldError
		if errors.As(err, &withheld) {
			// Not retried: a record this deployment refuses to send will be refused
			// again, and the dispatcher's retry exists for sinks that come back. It
			// is loud instead — somebody is counting on these records arriving, so
			// one held back has to be visible where they will look for it.
			d.logger.Warn("decision withheld by content scan", "task", record.TaskID, "classes", withheld.Classes)
			d.store.Audit(ctx, nil, "provenance.withheld", "task", record.TaskID, "blocked", "",
				map[string]any{"classes": withheld.Classes})
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
func SendDecision(ctx context.Context, settings store.ProvenanceSettings, scan dlp.Settings, record store.DecisionRecord) error {
	// Scanned here rather than by the caller, because a caller can forget. The
	// export was one of two ways text left this deployment uninspected — the other
	// was the review comment posted back to a forge — and a sending path added
	// later must not be able to repeat that.
	record, findings, blocked := scrubDecision(scan, record)
	if blocked {
		result := dlp.Result{Findings: findings}
		return WithheldError{Subject: "결정 기록", Classes: result.Classes(), Labels: result.Labels()}
	}
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("결정 기록을 인코딩하지 못했습니다: %w", err)
	}
	send, cancel := context.WithTimeout(ctx, provenanceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(send, http.MethodPost, settings.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("결정 기록을 보내지 못했습니다: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if settings.Header != "" && settings.Token != "" {
		request.Header.Set(settings.Header, settings.Token)
	}
	response, err := provenanceClient.Do(request)
	if err != nil {
		return fmt.Errorf("결정 기록을 보내지 못했습니다: %s", modelCallReasonLike(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("결정 기록을 받는 쪽이 HTTP %d 로 답했습니다", response.StatusCode)
	}
	return nil
}

// contentSettings reads what this deployment allows out of the building. An
// unreadable setting scans nothing rather than blocking everything: the export
// is not the place to discover that the settings table is unavailable.
func (d *Dispatcher) contentSettings(ctx context.Context) dlp.Settings {
	var settings dlp.Settings
	if err := d.store.Setting(ctx, dlp.SettingKey, &settings); err != nil {
		return dlp.Settings{}
	}
	return settings
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
	return subject + "에 " + names + korean.Subject(names) + " 포함되어 보내지 않았습니다"
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
func scrubDecision(settings dlp.Settings, record store.DecisionRecord) (store.DecisionRecord, []dlp.Finding, bool) {
	var findings []dlp.Finding
	blocked := false
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
		findings = append(findings, result.Findings...)
		if result.Blocked {
			blocked = true
		}
	}
	return record, findings, blocked
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
