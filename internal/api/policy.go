package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
)

// The central policy: reading it, changing it, trying it out, and enforcing it.
//
// The controls this replaces were all real and all separate — an allow list on
// one agent, a global approval switch, ownership, a quota — so the sentence a
// security review asks for ("contractors may not call anything that writes") had
// nowhere to live. It lives here, and every decision point below says which rule
// decided so that a refusal is something a person can act on.

// policyDocument reads the stored policy. An unreadable one is logged and
// treated as empty: a document that no longer parses must not stop the platform,
// and the API validates every document on the way in, so only a hand-edited row
// can get here.
func (s *Server) policyDocument(r *http.Request) policy.Document {
	var document policy.Document
	if err := s.store.Setting(r.Context(), policy.SettingKey, &document); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("policy document is unreadable; requests are decided without it", "error", err)
	}
	return document
}

// decide evaluates one request against the policy and audits anything that is
// not a plain allow.
//
// Audit is not optional here: a policy nobody can prove was applied is a policy
// that will be argued about after an incident rather than before one.
func (s *Server) decide(r *http.Request, u store.User, request policy.Request) policy.Decision {
	request.Role, request.User, request.UserID = u.Role, u.Username, u.ID
	decision := policy.Evaluate(s.policyDocument(r), request)
	if decision.Allowed() {
		return decision
	}
	s.store.Audit(r.Context(), &u, "policy."+request.Action, "policy", decision.RuleID, "denied", clientIP(r),
		map[string]any{"effect": decision.Effect, "agent": request.Agent, "server": request.Server, "tool": request.Tool, "reason": decision.Reason})
	s.logger.Info("policy refused a request", "action", request.Action, "rule", decision.RuleID,
		"effect", decision.Effect, "user", u.Username, "agent", request.Agent)
	return decision
}

// policyRefusal turns a decision into the message the caller reads, or "" when
// the request may proceed.
//
// require_approval is treated as a refusal at these decision points, and says so:
// task creation and runtime start have nowhere to wait, and silently allowing
// what a rule wanted reviewed would be the worst reading of it.
func policyRefusal(decision policy.Decision) string {
	switch decision.Effect {
	case policy.Deny:
		return refusalText(decision, "플랫폼 정책에 의해 차단되었습니다.")
	case policy.RequireApproval:
		return refusalText(decision, "플랫폼 정책이 사전 승인을 요구하는 요청입니다. 이 작업에는 승인 경로가 없어 차단되었습니다.")
	default:
		return ""
	}
}

func refusalText(decision policy.Decision, fallback string) string {
	message := strings.TrimSpace(decision.Reason)
	if message == "" {
		message = fallback
	}
	if decision.RuleID != "" {
		return message + " (정책 규칙: " + decision.RuleID + ")"
	}
	return message
}

// adminPolicy returns the document with everything the console needs to edit it.
func (s *Server) adminPolicy(w http.ResponseWriter, r *http.Request) {
	document := s.policyDocument(r)
	if document.Rules == nil {
		document.Rules = []policy.Rule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"document": document,
		"actions":  policy.Actions,
		"effects":  policy.Effects,
		"roles":    []string{"user", "manager", "admin"},
	})
}

// putPolicy replaces the document.
//
// The whole document is written at once rather than rule by rule because the
// order is the policy: a rule inserted in the wrong place is a different policy,
// and an API that could only append would hide that.
func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var document policy.Document
	if !decodeJSON(w, r, &document) {
		return
	}
	if err := document.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), policy.SettingKey, document, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "policy.update", "policy", "", "success", clientIP(r),
		map[string]any{"rules": len(document.Rules), "defaultEffect": document.DefaultEffect})
	s.logger.Warn("platform policy changed", "by", u.Username, "rules", len(document.Rules))
	// Tool rules are compiled into each runtime when it is provisioned, so a
	// change reaches running Pods the same way the runtime environment does.
	applied := s.syncRuntimeEnvironment(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "rules": len(document.Rules),
		"runtimes": map[string]any{"applied": applied.applied, "failed": applied.failed, "skipped": applied.skipped},
		"message":  policySaved(len(document.Rules), applied),
	})
}

// policySaved says what the save actually did, including to runtimes that were
// already running — the part that would otherwise look like nothing happened.
func policySaved(rules int, applied syncResult) string {
	switch {
	case applied.pruned:
		return "정책을 저장했지만 클러스터의 AgentRuntime CRD가 오래되어 도구 규칙이 Pod에 전달되지 않습니다. deploy/kubernetes/crd.yaml을 다시 적용해 주세요."
	case applied.applied > 0:
		return plural(rules) + " 규칙을 저장하고 실행 중인 런타임 " + plural(applied.applied) + "에 적용했습니다. 도구 규칙이 바뀐 Pod는 재시작됩니다."
	case applied.failed > 0:
		return plural(rules) + " 규칙을 저장했지만 런타임 " + plural(applied.failed) + "에 적용하지 못했습니다. 로그를 확인해 주세요."
	default:
		return plural(rules) + " 규칙을 저장했습니다. 새로 시작하는 런타임부터 적용됩니다."
	}
}

// simulatePolicy answers "what would happen if" without changing anything.
//
// A policy language people cannot test is a policy people write once and then
// work around. This is the affordance that makes the order — first match wins —
// something an operator can see rather than infer.
func (s *Server) simulatePolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		// Document lets the console try an unsaved edit. Omitted, the stored one
		// is used, which is how "why was this blocked" is answered.
		Document *policy.Document `json:"document"`
		Request  struct {
			Action      string   `json:"action"`
			Role        string   `json:"role"`
			User        string   `json:"user"`
			Agent       string   `json:"agent"`
			Server      string   `json:"server"`
			Tool        string   `json:"tool"`
			DataClasses []string `json:"dataClasses"`
		} `json:"request"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	document := s.policyDocument(r)
	if input.Document != nil {
		if err := input.Document.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_policy", err.Error())
			return
		}
		document = *input.Document
	}
	request := policy.Request{
		Action: strings.TrimSpace(input.Request.Action), Role: input.Request.Role,
		User: input.Request.User, UserID: input.Request.User,
		Agent: input.Request.Agent, AgentID: input.Request.Agent,
		Server: input.Request.Server, Tool: input.Request.Tool, DataClasses: input.Request.DataClasses,
	}
	if request.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "시뮬레이션할 동작을 선택해 주세요.")
		return
	}
	writeJSON(w, http.StatusOK, policy.Evaluate(document, request))
}
