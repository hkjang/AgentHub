// Package policy is the platform's central rule set: who may do what, to which
// agent, with which tool, on which kind of data.
//
// The controls were all real and all separate. An agent's MCP tools were an
// allow/deny list on that agent. High-risk approval was a global switch. Who
// could run what was ownership plus a role. Spend was a quota. Every one of them
// was configured in a different screen, and none of them could express the
// sentence a security review actually asks for — "contractors may not call
// anything that writes, and nobody may send resident registration numbers to a
// model" — because there was nowhere to write it down.
//
// Rules are evaluated in order, first match wins, like a firewall. That is not
// the most expressive arrangement, but it is the one an operator can read top to
// bottom and predict, and the console shows exactly which rule decided.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SettingKey is the system_settings row the document lives in.
const SettingKey = "policy"

// Effects a rule can have.
const (
	// Allow lets the request through. Explicit allows exist so a narrow exception
	// can sit above a broad deny.
	Allow = "allow"
	// Deny refuses it, with the rule's reason.
	Deny = "deny"
	// RequireApproval sends it to a reviewer rather than refusing it outright.
	// Only actions that have somewhere to wait honour this; the others treat it
	// as a deny and say so.
	RequireApproval = "require_approval"
)

// Actions the platform evaluates. Keeping them as constants means a rule can be
// validated against the list instead of silently never matching.
const (
	ActionTaskCreate   = "task.create"
	ActionRuntimeStart = "runtime.start"
	ActionToolCall     = "tool.call"
	ActionModelCall    = "model.call"
	ActionWorkflowRun  = "workflow.run"
	ActionAgentUpdate  = "agent.update"
)

// Actions is every action a rule may name, in the order the console offers them.
var Actions = []string{ActionTaskCreate, ActionRuntimeStart, ActionToolCall, ActionModelCall, ActionWorkflowRun, ActionAgentUpdate}

// Effects is every effect a rule may carry.
var Effects = []string{Allow, Deny, RequireApproval}

// Rule is one line of the policy.
//
// Every selector left empty matches everything, and a rule matches when all of
// its non-empty selectors match. Within one selector the values are alternatives.
// That is the arrangement people already expect from firewall and IAM rules, and
// it keeps a rule readable as a sentence.
type Rule struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Enabled left unset means enabled: a rule written through the API without
	// the field should apply rather than silently do nothing.
	Enabled *bool  `json:"enabled,omitempty"`
	Effect  string `json:"effect"`
	// Actions this rule decides. Empty means every action, which is usually not
	// what anyone means — the console warns about it rather than forbidding it.
	Actions []string `json:"actions,omitempty"`
	// Who. Roles are exact; users match a username or a user id.
	Roles []string `json:"roles,omitempty"`
	Users []string `json:"users,omitempty"`
	// What. Agents match a name or an id, servers an MCP server name, tools a
	// tool name — each accepting a trailing * so "github/*" or "delete_*" works.
	Agents  []string `json:"agents,omitempty"`
	Servers []string `json:"servers,omitempty"`
	Tools   []string `json:"tools,omitempty"`
	// Which kind of data the request carries, from the content scanner. A rule
	// with data classes only matches a request that was scanned and found them.
	DataClasses []string `json:"dataClasses,omitempty"`
	// Reason is shown to whoever is refused. A denial nobody can act on is a
	// support ticket, so this is required for deny and require_approval.
	Reason string `json:"reason,omitempty"`
}

// Active reports whether this rule is applied.
func (r Rule) Active() bool { return r.Enabled == nil || *r.Enabled }

// Document is the whole policy.
type Document struct {
	// DefaultEffect decides a request no rule matched. It defaults to allow: a
	// deployment that writes its first rule must not lock everyone out of
	// everything else in the process.
	DefaultEffect string `json:"defaultEffect,omitempty"`
	Rules         []Rule `json:"rules"`
}

// Request is what is being decided.
type Request struct {
	Action string
	// Role and User describe the person or key acting. UserID is matched by the
	// same selector as User so a rule can name either.
	Role   string
	User   string
	UserID string
	// Agent, AgentID, Server and Tool describe what is being acted on.
	Agent   string
	AgentID string
	Server  string
	Tool    string
	// DataClasses are what the content scanner found in this request, if it was
	// scanned. Empty means "not scanned or nothing found", and a rule that names
	// data classes will not match it.
	DataClasses []string
}

// Decision is the answer, and which rule gave it.
type Decision struct {
	Effect string `json:"effect"`
	// RuleID is empty when the default decided, which the console shows as
	// "기본 정책" rather than leaving a blank.
	RuleID string `json:"ruleId,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Matched is every rule that would have matched, in order. The first one is
	// the decision; the rest are what an operator needs to see when they wonder
	// why their new rule did nothing.
	Matched []string `json:"matched,omitempty"`
}

// Allowed reports whether the request may proceed without a person.
func (d Decision) Allowed() bool { return d.Effect == Allow }

// Evaluate applies the document to one request.
//
// The first enabled rule that matches decides. Nothing about a later rule can
// change that, which is what makes the order the policy rather than an accident.
func Evaluate(document Document, request Request) Decision {
	decision := Decision{Effect: defaultEffect(document)}
	for _, rule := range document.Rules {
		if !rule.Active() || !matches(rule, request) {
			continue
		}
		decision.Matched = append(decision.Matched, rule.ID)
		if len(decision.Matched) == 1 {
			decision.Effect, decision.RuleID, decision.Reason = rule.Effect, rule.ID, rule.Reason
		}
	}
	if decision.Effect == "" {
		decision.Effect = Allow
	}
	return decision
}

func defaultEffect(document Document) string {
	switch document.DefaultEffect {
	case Deny, RequireApproval:
		return document.DefaultEffect
	default:
		return Allow
	}
}

// matches reports whether every selector this rule sets is satisfied.
func matches(rule Rule, request Request) bool {
	if !selects(rule.Actions, request.Action) {
		return false
	}
	if len(rule.Roles) > 0 && !selects(rule.Roles, request.Role) {
		return false
	}
	if len(rule.Users) > 0 && !selects(rule.Users, request.User) && !selects(rule.Users, request.UserID) {
		return false
	}
	if len(rule.Agents) > 0 && !selects(rule.Agents, request.Agent) && !selects(rule.Agents, request.AgentID) {
		return false
	}
	if len(rule.Servers) > 0 && !selects(rule.Servers, request.Server) {
		return false
	}
	if len(rule.Tools) > 0 && !toolSelects(rule.Tools, request.Server, request.Tool) {
		return false
	}
	if len(rule.DataClasses) > 0 && !anySelects(rule.DataClasses, request.DataClasses) {
		return false
	}
	return true
}

// selects reports whether a value matches one of the patterns. An empty pattern
// list matches everything; an empty value matches only a wildcard, so a rule
// about tools never fires on a request that has none.
func selects(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if match(pattern, value) {
			return true
		}
	}
	return false
}

func anySelects(patterns []string, values []string) bool {
	for _, value := range values {
		if selects(patterns, value) {
			return true
		}
	}
	return false
}

// toolSelects matches a tool pattern against both "tool" and "server/tool", so a
// rule can name a tool on one server without naming it everywhere.
func toolSelects(patterns []string, server, tool string) bool {
	if tool == "" {
		return false
	}
	qualified := tool
	if server != "" {
		qualified = server + "/" + tool
	}
	for _, pattern := range patterns {
		if match(pattern, tool) || match(pattern, qualified) {
			return true
		}
	}
	return false
}

// match is an exact, case-insensitive comparison with an optional trailing star.
//
// Full globbing was tempting and would have been worse: a policy language nobody
// can predict is a policy nobody trusts, and "delete_*" covers what people
// actually write.
func match(pattern, value string) bool {
	pattern, value = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(value))
	if pattern == "*" {
		return true
	}
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return value != "" && strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == value
}

// Validate rejects a document the engine would only misapply later.
func (d Document) Validate() error {
	if d.DefaultEffect != "" && !valid(Effects, d.DefaultEffect) {
		return fmt.Errorf("기본 정책은 %s 중 하나여야 합니다", strings.Join(Effects, ", "))
	}
	if len(d.Rules) > 200 {
		return errors.New("정책 규칙은 최대 200개까지 등록할 수 있습니다")
	}
	seen := map[string]bool{}
	for index, rule := range d.Rules {
		position := fmt.Sprintf("%d번째 규칙", index+1)
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return fmt.Errorf("%s: 규칙 ID를 입력해 주세요", position)
		}
		if seen[strings.ToLower(id)] {
			return fmt.Errorf("규칙 ID %q 가 중복됩니다", id)
		}
		seen[strings.ToLower(id)] = true
		if !valid(Effects, rule.Effect) {
			return fmt.Errorf("%s(%s): 효과는 %s 중 하나여야 합니다", position, id, strings.Join(Effects, ", "))
		}
		for _, action := range rule.Actions {
			if !valid(Actions, action) && action != "*" {
				return fmt.Errorf("%s(%s): 지원하지 않는 동작입니다: %s", position, id, action)
			}
		}
		for _, role := range rule.Roles {
			if !valid([]string{"user", "manager", "admin"}, role) {
				return fmt.Errorf("%s(%s): 지원하지 않는 역할입니다: %s", position, id, role)
			}
		}
		// A refusal has to say why. This is the difference between a policy an
		// operator can run and one that generates support tickets.
		if rule.Effect != Allow && strings.TrimSpace(rule.Reason) == "" {
			return fmt.Errorf("%s(%s): 차단·승인 규칙에는 사유가 필요합니다", position, id)
		}
		if len(rule.Description) > 300 || len(rule.Reason) > 300 {
			return fmt.Errorf("%s(%s): 설명과 사유는 300자 이하여야 합니다", position, id)
		}
		if total := len(rule.Roles) + len(rule.Users) + len(rule.Agents) + len(rule.Servers) + len(rule.Tools) + len(rule.DataClasses); total > 100 {
			return fmt.Errorf("%s(%s): 조건 값은 규칙당 100개 이하여야 합니다", position, id)
		}
	}
	return nil
}

func valid(allowed []string, value string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

// ServerRules is what the in-Pod gateway has to enforce for one agent and one
// MCP server, compiled from the central policy.
//
// The gateway is the only place an agent cannot route around, and it is a
// separate process with no database: the rules are compiled into its
// configuration when the runtime is provisioned. They stay as patterns rather
// than resolved tool names because the tool list is not known until the server is
// running — a policy that only covers the tools we happened to know about at
// provisioning time is not a policy.
type ServerRules struct {
	// Denied never runs; Gated waits for a person. Both hold the patterns from
	// the matching rules, matched by the gateway with MatchTool.
	Denied []string `json:"denied,omitempty"`
	Gated  []string `json:"gated,omitempty"`
	// Allowed are the exceptions: tool patterns an allow rule named above the
	// restrictions below it. The gateway checks them first, which is what makes
	// "nobody may use this server, except read_file" mean the same thing in the
	// Pod as it does at every other decision point.
	Allowed []string `json:"allowed,omitempty"`
	// DenyAll and GateAll come from a rule with no tool selector, which covers
	// every tool the server offers including the ones nobody has seen yet.
	DenyAll bool `json:"denyAll,omitempty"`
	GateAll bool `json:"gateAll,omitempty"`
}

// Empty reports whether the policy has nothing to say about this server, so the
// provisioning path can leave the binding exactly as it was.
//
// Exceptions alone are nothing to say: an allow rule with no restriction under
// it permits what was already permitted.
func (r ServerRules) Empty() bool {
	return len(r.Denied) == 0 && len(r.Gated) == 0 && !r.DenyAll && !r.GateAll
}

// CompileServer resolves the tool.call rules for one agent and one MCP server.
//
// An allow rule that sits above a deny still wins, and that is decided here.
// A blanket allow ends the compilation, exactly as it ends the evaluation at
// call time; a narrow one becomes an exception the gateway checks before the
// restrictions written below it.
func CompileServer(document Document, request Request) ServerRules {
	compiled := ServerRules{}
	request.Action = ActionToolCall
	for _, rule := range document.Rules {
		if !rule.Active() || !matchesWithoutTools(rule, request) {
			continue
		}
		switch {
		case rule.Effect == Allow && len(rule.Tools) == 0:
			// A blanket allow for this agent on this server ends the evaluation the
			// same way it would at call time: first match wins. What was compiled
			// above it stays — those rules matched first, so at call time they are
			// what decides, and dropping them here would let a broad allow written
			// below a restriction quietly undo it in the Pod alone.
			return sorted(compiled)
		case rule.Effect == Allow:
			// The narrow exception the effect exists for: "nobody may use this
			// server, except read_file". It only holds over the restrictions
			// written below it — one written above already decided at call time,
			// so it decides here too, and a pattern it caught is not carried.
			for _, tool := range rule.Tools {
				if restrictedAbove(compiled, request.Server, tool) {
					continue
				}
				compiled.Allowed = appendUnique(compiled.Allowed, tool)
			}
		case rule.Effect == Deny && len(rule.Tools) == 0:
			compiled.DenyAll = true
		case rule.Effect == Deny:
			compiled.Denied = appendUnique(compiled.Denied, rule.Tools...)
		case len(rule.Tools) == 0:
			compiled.GateAll = true
		default:
			compiled.Gated = appendUnique(compiled.Gated, rule.Tools...)
		}
	}
	return sorted(compiled)
}

// sorted puts the compiled patterns in a stable order, so provisioning the same
// policy twice produces the same object and the operator sees no change.
func sorted(compiled ServerRules) ServerRules {
	sort.Strings(compiled.Denied)
	sort.Strings(compiled.Gated)
	sort.Strings(compiled.Allowed)
	return compiled
}

// restrictedAbove reports whether something already compiled would catch a tool
// this allow rule names — in which case that restriction is above it in the
// document, decides at call time, and the exception must not travel.
//
// It is answered on patterns rather than tool names because that is all either
// end has: the tools a server offers are not known until it is running.
func restrictedAbove(compiled ServerRules, server, tool string) bool {
	if compiled.DenyAll || compiled.GateAll {
		return true
	}
	for _, restriction := range compiled.Denied {
		if overlaps(restriction, tool, server) {
			return true
		}
	}
	for _, restriction := range compiled.Gated {
		if overlaps(restriction, tool, server) {
			return true
		}
	}
	return false
}

// overlaps reports whether some tool name could match both patterns.
//
// The question is asked conservatively: when it cannot be ruled out, the two
// patterns are treated as the same tool and the restriction wins, because the
// cost of being wrong in that direction is a call refused rather than a call the
// policy meant to refuse getting through.
func overlaps(left, right, server string) bool {
	for _, a := range toolMatchers(left, server) {
		for _, b := range toolMatchers(right, server) {
			if matcherOverlap(a, b) {
				return true
			}
		}
	}
	return false
}

// toolMatchers reduces one pattern to the tool names it can match on this
// server. A pattern is compared against both "tool" and "server/tool", so
// "github/delete_*" and "delete_*" are one restriction on the github server and
// have to be recognised as one here too.
func toolMatchers(pattern, server string) []string {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil
	}
	matchers := []string{pattern}
	qualifier := strings.ToLower(strings.TrimSpace(server)) + "/"
	stem, star := strings.TrimSuffix(pattern, "*"), strings.HasSuffix(pattern, "*")
	switch {
	case star && strings.HasPrefix(qualifier, stem):
		// The star falls inside the server name, so every tool's qualified form
		// begins with it.
		matchers = append(matchers, "*")
	case server != "" && strings.HasPrefix(pattern, qualifier):
		matchers = append(matchers, strings.TrimPrefix(pattern, qualifier))
	}
	return matchers
}

// matcherOverlap is the same comparison as match(), asked of two patterns rather
// than a pattern and a value.
func matcherOverlap(left, right string) bool {
	leftStem, leftStar := strings.TrimSuffix(left, "*"), strings.HasSuffix(left, "*")
	rightStem, rightStar := strings.TrimSuffix(right, "*"), strings.HasSuffix(right, "*")
	switch {
	case leftStar && rightStar:
		return strings.HasPrefix(leftStem, rightStem) || strings.HasPrefix(rightStem, leftStem)
	case leftStar:
		return strings.HasPrefix(right, leftStem)
	case rightStar:
		return strings.HasPrefix(left, rightStem)
	default:
		return left == right
	}
}

// MatchTool reports whether a tool matches one of the compiled patterns. The
// gateway calls it, so both processes decide with the same code rather than with
// two implementations that agree until they do not.
func MatchTool(patterns []string, server, tool string) bool {
	return toolSelects(patterns, server, tool)
}

// matchesWithoutTools is matches() with the tool selector skipped, for compiling
// a rule against a whole server rather than one call.
func matchesWithoutTools(rule Rule, request Request) bool {
	stripped := rule
	stripped.Tools = nil
	probe := request
	probe.Tool = ""
	return matches(stripped, probe)
}

func appendUnique(values []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, existing := range values {
			if existing == addition {
				found = true
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}
