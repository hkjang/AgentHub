// Package guard enforces the platform's data-loss rules at the boundary where
// text leaves it.
//
// It sits between the pure scanner, which knows what a resident registration
// number looks like and nothing else, and the central policy, which knows what
// this deployment does about one. Keeping the scanner free of the database is
// what lets the in-Pod sidecar use the same detectors without linking a
// PostgreSQL driver into a sidecar.
package guard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// settingsTTL is how long the scanner's configuration is cached.
//
// Every model call reads it, and a change that takes five seconds to apply is
// not a change anybody notices — while a database round trip per call would be.
const settingsTTL = 5 * time.Second

// Model is the inspector for model calls.
type Model struct {
	store  *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	settings dlp.Settings
	document policy.Document
	until    time.Time
}

// NewModel builds the inspector the completion client uses.
func NewModel(db *store.Store, logger *slog.Logger) *Model {
	return &Model{store: db, logger: logger}
}

// config returns the scanner settings and the policy, cached.
//
// A configuration that cannot be read leaves both empty, which means "do not
// scan". That is the honest failure: refusing every model call because a
// settings row was briefly unreadable would take the platform down to protect
// data that may not even be there, and the alternative — scanning with stale
// rules — is what the cache already does for five seconds anyway.
func (m *Model) config(ctx context.Context) (dlp.Settings, policy.Document) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().Before(m.until) {
		return m.settings, m.document
	}
	var settings dlp.Settings
	if err := m.store.Setting(ctx, dlp.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.logger.Warn("DLP settings are unreadable; text is not being scanned", "error", err)
		settings = dlp.Settings{}
	}
	var document policy.Document
	if err := m.store.Setting(ctx, policy.SettingKey, &document); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.logger.Warn("policy is unreadable; data class rules are not being applied", "error", err)
		document = policy.Document{}
	}
	m.settings, m.document, m.until = settings, document, time.Now().Add(settingsTTL)
	return settings, document
}

// Outbound inspects a prompt on its way to a model.
func (m *Model) Outbound(ctx context.Context, step workflow.Step, text string) (string, error) {
	return m.inspect(ctx, step, text, "요청")
}

// Inbound inspects an answer on its way back.
//
// It is the same check for a different reason: a model that was given a
// customer record will happily repeat it, and the answer is what gets written
// into a run's transcript where far more people can read it.
func (m *Model) Inbound(ctx context.Context, step workflow.Step, text string) (string, error) {
	settings, _ := m.config(ctx)
	if !settings.ScanResponses {
		return text, nil
	}
	return m.inspect(ctx, step, text, "응답")
}

func (m *Model) inspect(ctx context.Context, step workflow.Step, text, direction string) (string, error) {
	settings, document := m.config(ctx)
	result := dlp.Scan(settings, text)
	if len(result.Findings) == 0 {
		return text, nil
	}

	// The scanner says what is there; the policy says what this deployment does
	// about it. A rule can be narrower than the global action — one agent, one
	// role — which is the whole reason the two are separate.
	decision := policy.Evaluate(document, policy.Request{
		Action: policy.ActionModelCall, Agent: step.AgentName, AgentID: step.AgentID,
		DataClasses: result.Classes(),
	})
	blocked := result.Blocked || decision.Effect != policy.Allow
	reason := result.Reason
	if reason == "" && decision.Reason != "" {
		reason = decision.Reason
	}
	if reason == "" {
		reason = fmt.Sprintf("민감정보(%s)가 포함되어 전송을 차단했습니다.", result.Summary())
	}

	m.record(ctx, step, result, decision, direction, blocked)
	if blocked {
		// Wrapped in the sentinel so the execution plane fails the task instead of
		// retrying a decision that cannot change.
		return "", fmt.Errorf("%w: 모델 %s에 민감정보가 포함되어 있습니다 — %s", workflow.ErrBlocked, direction, reason)
	}
	return result.Text, nil
}

// record writes what was found — never what it was.
//
// The audit entry carries the class, the count and the masked sample the scanner
// produced. A DLP trail that quotes the value it found has moved the problem
// rather than solved it.
func (m *Model) record(ctx context.Context, step workflow.Step, result dlp.Result, decision policy.Decision, direction string, blocked bool) {
	outcome := "redacted"
	if blocked {
		outcome = "blocked"
	}
	findings := make([]map[string]any, 0, len(result.Findings))
	for _, finding := range result.Findings {
		findings = append(findings, map[string]any{
			"class": finding.Class, "count": finding.Count, "action": finding.Action, "sample": finding.Sample,
		})
	}
	m.store.Audit(ctx, nil, "dlp.model", "agent", step.AgentID, outcome, "", map[string]any{
		"direction": direction, "agent": step.AgentName, "findings": findings,
		"policyRule": decision.RuleID, "truncated": result.Truncated,
	})
	m.logger.Warn("sensitive data found on a model call",
		"agent", step.AgentName, "direction", direction, "outcome", outcome,
		"classes", result.Summary(), "policyRule", decision.RuleID)
}
