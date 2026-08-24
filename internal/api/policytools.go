package api

import (
	"context"
	"sort"
	"strings"

	"github.com/hkjang/AgentHub/internal/store"
)

// Whether a tool policy names tools that exist.
//
// Everything else about a policy is checked when it is saved — the mode, that
// the server is one this agent is bound to, that a blocked tool is not also
// gated. The tool names were not, and they are what decides whether the policy
// does anything at all.
//
// A deny rule naming a tool the server does not offer blocks nothing and reads
// as protection, which is the worst of the three: somebody wrote a rule, the
// screen shows it, and the call it was meant to stop goes through. An allow rule
// with the same typo refuses work the agent was supposed to do, and a gate with
// one never asks anybody.
//
// So the names are compared against what the server actually offers, and the
// answer informs rather than refuses: a server that is down, or one running
// inside the Pod where the control plane cannot reach it, must not stop somebody
// editing a policy.

// unknownTool is one name the server does not offer, with the closest thing it
// does.
type unknownTool struct {
	Name string `json:"name"`
	// DidYouMean is the offered tool whose name is nearest, when one is close
	// enough to be worth showing. A typo's neighbour is the whole answer.
	DidYouMean string `json:"didYouMean,omitempty"`
}

// checkPolicyTools asks the server what it offers and reports what the policy
// names that is not there.
//
// It returns nothing at all when the question cannot be asked, which is the same
// rule the event filter check follows: no evidence, no complaint.
func (s *Server) checkPolicyTools(ctx context.Context, server store.MCPServer, named []string) []unknownTool {
	if len(named) == 0 {
		return nil
	}
	verdict, _, offered := askMCPServer(ctx, server.Mode, server.Endpoint)
	if verdict != "ok" {
		return nil
	}
	return unknownAmong(named, offered)
}

// unknownAmong is the comparison itself, apart from the asking.
//
// An empty list of offered tools is not evidence that the named ones are wrong —
// it is a server that answered without saying what it can do — so it produces no
// complaint at all. Treating it as "none of these exist" would report every
// policy on that server as broken.
func unknownAmong(named, offered []string) []unknownTool {
	if len(offered) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, tool := range offered {
		known[tool] = true
	}
	unknown := []unknownTool{}
	for _, tool := range named {
		if known[tool] {
			continue
		}
		unknown = append(unknown, unknownTool{Name: tool, DidYouMean: nearestTool(tool, offered)})
	}
	sort.Slice(unknown, func(a, b int) bool { return unknown[a].Name < unknown[b].Name })
	return unknown
}

// nearestTool finds the offered name closest to what somebody typed, or nothing
// when none is close. A suggestion that is not nearly right is worse than none:
// it invites somebody to accept a tool they did not mean.
func nearestTool(typed string, offered []string) string {
	best, bestDistance := "", 0
	limit := len(typed)/3 + 1
	for _, candidate := range offered {
		distance := editDistance(strings.ToLower(typed), strings.ToLower(candidate))
		if distance > limit {
			continue
		}
		if best == "" || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// editDistance is the ordinary Levenshtein distance, over runes so a Korean or
// otherwise non-ASCII tool name is measured in characters rather than bytes.
func editDistance(a, b string) int {
	left, right := []rune(a), []rune(b)
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, min(current[j-1]+1, previous[j-1]+cost))
		}
		copy(previous, current)
	}
	return previous[len(right)]
}

// policyToolNotice says what the unknown names mean for this policy, in terms of
// what will happen rather than what is missing.
func policyToolNotice(mode string, unknown []unknownTool, gated bool) string {
	if len(unknown) == 0 {
		return ""
	}
	names := make([]string, 0, len(unknown))
	for _, tool := range unknown {
		if tool.DidYouMean != "" {
			names = append(names, tool.Name+"(→ "+tool.DidYouMean+"?)")
			continue
		}
		names = append(names, tool.Name)
	}
	list := strings.Join(names, ", ")
	switch {
	case gated:
		return "저장했습니다. 다만 이 서버에 없는 도구에 승인을 걸어 두었습니다: " + list + " — 이름이 틀렸다면 아무것도 승인 대기에 걸리지 않습니다."
	case mode == "deny":
		return "저장했습니다. 다만 이 서버에 없는 도구를 차단하고 있습니다: " + list + " — 이름이 틀렸다면 아무것도 차단되지 않습니다."
	default:
		return "저장했습니다. 다만 이 서버에 없는 도구만 허용하고 있습니다: " + list + " — 이름이 틀렸다면 그 도구는 계속 막힙니다."
	}
}
