package store

import (
	"strings"
	"testing"
	"time"
)

// The trail is the one table where a filter assembled by string concatenation
// would be least likely to be noticed, so every value must arrive as a bound
// parameter and the clause must match what was asked for.
func TestAuditWhere(t *testing.T) {
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		filter  AuditFilter
		clauses []string
		args    int
	}{
		{name: "no filter matches everything", filter: AuditFilter{}, clauses: []string{"1=1"}},
		{name: "actor is a partial, case-insensitive match",
			filter: AuditFilter{Actor: " kim "}, clauses: []string{"actor_name ILIKE $1"}, args: 1},
		{name: "action matches by prefix so a family selects together",
			filter: AuditFilter{Action: "agent."}, clauses: []string{"action LIKE $1"}, args: 1},
		{name: "everything at once numbers its parameters in order",
			filter: AuditFilter{Actor: "a", Action: "b", ResourceType: "agent", ResourceID: "x", Outcome: "success", From: &when, To: &when},
			clauses: []string{"actor_name ILIKE $1", "action LIKE $2", "resource_type = $3",
				"resource_id = $4", "outcome = $5", "occurred_at >= $6", "occurred_at < $7"},
			args: 7},
		{name: "blank values are not filters", filter: AuditFilter{Actor: "   ", Outcome: ""}, clauses: []string{"1=1"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			where, args := auditWhere(test.filter)
			if len(args) != test.args {
				t.Fatalf("bound %d parameters, want %d (%s)", len(args), test.args, where)
			}
			for _, clause := range test.clauses {
				if !strings.Contains(where, clause) {
					t.Errorf("missing %q in %q", clause, where)
				}
			}
			// A value must never appear in the SQL itself.
			for _, literal := range []string{"kim", "agent.", "success"} {
				if strings.Contains(where, literal) {
					t.Errorf("%q was inlined into the query: %s", literal, where)
				}
			}
		})
	}
	// Trimming matters: a trailing space in a search box would otherwise match
	// nothing and look like an empty trail.
	_, args := auditWhere(AuditFilter{Actor: " kim "})
	if args[0] != "%kim%" {
		t.Errorf("actor bound as %q, want %%kim%%", args[0])
	}
}
