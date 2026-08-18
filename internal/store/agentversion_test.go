package store

import (
	"strings"
	"testing"
)

// The gate decides whether a scheduled run happens at all, so both of its
// mistakes are expensive: refusing an agent nobody gated stops work that was
// running fine yesterday, and letting an unpromoted definition through is the
// exact thing the gate was turned on to prevent.
func TestPromotionBlock(t *testing.T) {
	version := func(v int) *int { return &v }
	cases := []struct {
		name     string
		state    AgentRelease
		blocked  bool
		mentions string
	}{
		{name: "gate off", state: AgentRelease{Current: 7}},
		{name: "gate off even when an older version is promoted",
			state: AgentRelease{Current: 7, PromotedVersion: version(3)}},
		{name: "promoted version is the live one",
			state: AgentRelease{Current: 7, PromotedVersion: version(7), RequirePromotion: true}},
		{name: "nothing promoted yet",
			state:   AgentRelease{Current: 1, RequirePromotion: true},
			blocked: true, mentions: "v1"},
		{name: "live definition is newer than the promoted one",
			state:   AgentRelease{Current: 7, PromotedVersion: version(3), RequirePromotion: true},
			blocked: true, mentions: "v3"},
		// A restore rewinds the definition but not the counter, so the live version
		// can be older than the promoted one without being it.
		{name: "live definition is older than the promoted one",
			state:   AgentRelease{Current: 2, PromotedVersion: version(5), RequirePromotion: true},
			blocked: true, mentions: "v5"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reason := promotionBlock(test.state)
			if test.blocked && reason == "" {
				t.Fatalf("the task must be refused")
			}
			if !test.blocked && reason != "" {
				t.Fatalf("the task must run; got %q", reason)
			}
			if test.mentions != "" && !strings.Contains(reason, test.mentions) {
				t.Fatalf("the reason must name %s so it can be acted on; got %q", test.mentions, reason)
			}
		})
	}
}
