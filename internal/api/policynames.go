package api

import (
	"context"
	"sort"
	"strings"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
)

// Whether a policy rule names anything that exists.
//
// A rule's effect, actions and roles are checked when it is saved; the rest of
// what a rule names is free text. So a deny rule for agent "payment-bot" when
// the agent is called "payment_bot" matches nothing, for ever — and a deny rule
// that matches nothing reads exactly like protection. This is the platform's own
// policy engine, which makes it the worst place to keep that particular silence.
//
// Four of the five kinds can be checked against this deployment: users, agents,
// MCP servers and the content scanner's data classes all exist here. Tool names
// belong to a server the control plane may not reach, and are left alone — the
// per-agent tool policy checks those where it can.

// unmatchedName is one thing a rule names that this deployment does not have.
type unmatchedName struct {
	Rule  string `json:"rule"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Means string `json:"means,omitempty"`
}

// checkPolicyNames reports what the rules name and this deployment does not have.
//
// Wildcards are left alone: "github/*" is a pattern rather than a name, and
// reporting it as missing would be reporting the feature as a fault.
func (s *Server) checkPolicyNames(ctx context.Context, document policy.Document) []unmatchedName {
	known := s.knownNames(ctx)
	if known == nil {
		// Nothing could be read, so there is no evidence to complain from.
		return nil
	}
	return unmatchedIn(document, known)
}

// unmatchedIn is the comparison itself, apart from the reading. It is separate so
// the rule about wildcards can be checked without a database: a name with a star
// is a pattern, and reporting it as missing would report the feature as a fault.
func unmatchedIn(document policy.Document, known map[string]map[string]bool) []unmatchedName {
	unmatched := []unmatchedName{}
	for _, rule := range document.Rules {
		for _, check := range []struct {
			kind  string
			names []string
			known map[string]bool
		}{
			{"사용자", rule.Users, known["users"]},
			{"에이전트", rule.Agents, known["agents"]},
			{"MCP 서버", rule.Servers, known["servers"]},
			{"데이터 종류", rule.DataClasses, known["classes"]},
		} {
			if len(check.known) == 0 {
				continue
			}
			for _, name := range check.names {
				trimmed := strings.TrimSpace(name)
				if trimmed == "" || strings.Contains(trimmed, "*") {
					continue
				}
				if check.known[strings.ToLower(trimmed)] {
					continue
				}
				unmatched = append(unmatched, unmatchedName{
					Rule: rule.ID, Kind: check.kind, Name: trimmed,
					Means: ruleConsequence(rule.Effect),
				})
			}
		}
	}
	sort.Slice(unmatched, func(a, b int) bool {
		if unmatched[a].Rule != unmatched[b].Rule {
			return unmatched[a].Rule < unmatched[b].Rule
		}
		return unmatched[a].Name < unmatched[b].Name
	})
	return unmatched
}

// knownNames reads what this deployment actually has. A kind that cannot be read
// is left out rather than reported as empty, because "we could not ask" and
// "there are none" produce opposite advice.
func (s *Server) knownNames(ctx context.Context) map[string]map[string]bool {
	known := map[string]map[string]bool{}
	if users, err := s.store.Users(ctx); err == nil {
		set := map[string]bool{}
		for _, user := range users {
			set[strings.ToLower(user.Username)] = true
			set[strings.ToLower(user.ID)] = true
		}
		known["users"] = set
	}
	// Every agent on the deployment: a policy is a platform rule and names
	// agents that may belong to anybody.
	if agents, err := s.store.Agents(ctx, "", true); err == nil {
		set := map[string]bool{}
		for _, agent := range agents {
			set[strings.ToLower(agent.Name)] = true
			set[strings.ToLower(agent.ID)] = true
		}
		known["agents"] = set
	}
	if servers, err := s.store.MCPServers(ctx); err == nil {
		set := map[string]bool{}
		for _, server := range servers {
			set[strings.ToLower(server.Name)] = true
		}
		known["servers"] = set
	}
	classes := map[string]bool{}
	for _, detector := range dlp.Detectors() {
		classes[strings.ToLower(detector.Class)] = true
	}
	known["classes"] = classes
	if len(known) == 0 {
		return nil
	}
	return known
}

// ruleConsequence says what a rule that matches nothing does, which is the news.
func ruleConsequence(effect string) string {
	switch effect {
	case policy.Deny:
		return "이 규칙은 아무것도 차단하지 않습니다"
	case policy.RequireApproval:
		return "이 규칙은 아무것도 승인 대기에 걸지 않습니다"
	default:
		return "이 규칙은 아무에게도 적용되지 않습니다"
	}
}

// policyNameNotice is the sentence an administrator reads after saving.
func policyNameNotice(unmatched []unmatchedName) string {
	if len(unmatched) == 0 {
		return ""
	}
	parts := make([]string, 0, len(unmatched))
	for _, item := range unmatched {
		parts = append(parts, item.Rule+": "+item.Kind+" "+item.Name)
	}
	shown := parts
	if len(shown) > 5 {
		shown = append(shown[:5:5], "…")
	}
	return "저장했습니다. 다만 이 배포에 없는 대상을 가리키는 규칙이 있습니다 — " +
		strings.Join(shown, ", ") + ". " + unmatched[0].Means + "."
}
