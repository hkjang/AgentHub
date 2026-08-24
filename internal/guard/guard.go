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

// settingsRead bounds the configuration read itself, which is a single row and
// must not be able to hold a run open.
const settingsRead = 3 * time.Second

// Model is the inspector for text leaving the platform on a model call — and,
// with NewFlow, for text handed to a runtime's own engine to execute. The two
// differ only in which policy action decides and which audit trail records it;
// the detectors, the cache and the refusal are the same, and a second copy of
// them would be a second place for the rules to drift.
type Model struct {
	store  *store.Store
	logger *slog.Logger
	// action is the policy action this inspector evaluates.
	action string
	// event is the audit action it records under.
	event string
	// subject names the boundary in the message a person reads.
	subject string

	mu       sync.Mutex
	settings dlp.Settings
	document policy.Document
	until    time.Time
}

// NewModel builds the inspector the completion client uses.
func NewModel(db *store.Store, logger *slog.Logger) *Model {
	return &Model{store: db, logger: logger, action: policy.ActionModelCall, event: "dlp.model", subject: "모델"}
}

// NewFlow builds the inspector for a Langflow flow run.
//
// A flow is not a prompt: what goes in is executed by components that may call
// their own models, read their own knowledge bases and post to their own
// endpoints. The platform cannot see inside it, which is exactly why the text on
// the way in and the answer on the way out are inspected here — this is the last
// place it can.
func NewFlow(db *store.Store, logger *slog.Logger) *Model {
	// The subject is "에이전트" rather than "흐름" because this one inspector
	// guards every backend the worker runs — cli, acp, rpc, orca, review and the
	// agent server all pass their text through it. Naming it after the flow
	// backend told somebody running an ACP agent that their "흐름 요청" had been
	// blocked, which is a boundary they never used and a word they cannot act on.
	//
	// The event type stays dlp.flow: it is what past runs were recorded under,
	// and a timeline that renames its own history is worse than one with an old
	// name in it.
	return &Model{store: db, logger: logger, action: policy.ActionWorkflowRun, event: "dlp.flow", subject: "에이전트"}
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
	// The configuration is read on a context of its own. It was read on the
	// caller's, and a caller is one task: when a run reached its time limit, the
	// read failed with that run's expired deadline and the scanner reported
	// itself unconfigured — measured live, as "DLP settings are unreadable; text
	// is not being scanned" logged for a task that had merely run long. One
	// task's clock is not a reason to stop inspecting anybody else's text.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settingsRead)
	defer cancel()
	var settings dlp.Settings
	failed := false
	if err := m.store.Setting(ctx, dlp.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.logger.Warn("DLP settings are unreadable; text is not being scanned", "error", err)
		settings, failed = dlp.Settings{}, true
	}
	var document policy.Document
	if err := m.store.Setting(ctx, policy.SettingKey, &document); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.logger.Warn("policy is unreadable; data class rules are not being applied", "error", err)
		document, failed = policy.Document{}, true
	}
	// A failed read is not cached. Not scanning is the documented answer to a
	// configuration nobody can read, but holding that answer for the full TTL
	// would turn one unreadable moment into a window in which every other run
	// goes uninspected, and would hide the cause: the next caller reads again.
	if failed {
		return settings, document
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
		Action: m.policyAction(), Agent: step.AgentName, AgentID: step.AgentID,
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
		return "", fmt.Errorf("%w: %s %s에 민감정보가 포함되어 있습니다 — %s", workflow.ErrBlocked, m.subjectName(), direction, reason)
	}
	return result.Text, nil
}

// The three describers default to the model boundary, so an inspector built
// before NewFlow existed keeps behaving exactly as it did.
func (m *Model) policyAction() string {
	if m.action == "" {
		return policy.ActionModelCall
	}
	return m.action
}

func (m *Model) auditEvent() string {
	if m.event == "" {
		return "dlp.model"
	}
	return m.event
}

func (m *Model) subjectName() string {
	if m.subject == "" {
		return "모델"
	}
	return m.subject
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
	m.store.Audit(ctx, nil, m.auditEvent(), "agent", step.AgentID, outcome, "", map[string]any{
		"direction": direction, "agent": step.AgentName, "findings": findings,
		"policyRule": decision.RuleID, "truncated": result.Truncated,
	})
	m.logger.Warn("sensitive data found leaving the platform",
		"boundary", m.subjectName(),
		"agent", step.AgentName, "direction", direction, "outcome", outcome,
		"classes", result.Summary(), "policyRule", decision.RuleID)
}
