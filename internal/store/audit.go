package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Reading the audit trail.
//
// The trail was written carefully and read badly: the console asked for the last
// 100 rows and nothing else. Answering "what did this person do last Tuesday" or
// "who changed the model endpoints" meant scrolling, and once a deployment is
// busy the answer has already fallen off the end of the list.

// AuditFilter narrows the trail. Every field is optional; an empty filter is the
// previous behaviour.
type AuditFilter struct {
	// Actor matches the recorded username, case-insensitively and partially,
	// because an operator looking for somebody rarely has the exact string.
	Actor string
	// Action matches by prefix, so "agent." selects everything about agents.
	Action       string
	ResourceType string
	ResourceID   string
	Outcome      string
	From         *time.Time
	To           *time.Time
	Limit        int
	Offset       int
}

// AuditPage is one page of the trail, with the size of the whole result so the
// console can say how much is behind it.
type AuditPage struct {
	Items  []map[string]any `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// auditWhere builds the shared predicate. Every value is a bound parameter: an
// audit trail assembled by string concatenation would be the one table where an
// injection is least likely to be noticed.
func auditWhere(filter AuditFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if actor := strings.TrimSpace(filter.Actor); actor != "" {
		add("actor_name ILIKE ?", "%"+actor+"%")
	}
	if action := strings.TrimSpace(filter.Action); action != "" {
		add("action LIKE ?", action+"%")
	}
	if value := strings.TrimSpace(filter.ResourceType); value != "" {
		add("resource_type = ?", value)
	}
	if value := strings.TrimSpace(filter.ResourceID); value != "" {
		add("resource_id = ?", value)
	}
	if value := strings.TrimSpace(filter.Outcome); value != "" {
		add("outcome = ?", value)
	}
	if filter.From != nil {
		add("occurred_at >= ?", *filter.From)
	}
	if filter.To != nil {
		add("occurred_at < ?", *filter.To)
	}
	return strings.Join(clauses, " AND "), args
}

// AuditTrail reads one page of the trail.
func (s *Store) AuditTrail(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	if filter.Limit < 1 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	where, args := auditWhere(filter)
	page := AuditPage{Items: []map[string]any{}, Limit: filter.Limit, Offset: filter.Offset}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE `+where, args...).Scan(&page.Total); err != nil {
		return AuditPage{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,occurred_at,actor_name,action,resource_type,resource_id,outcome,ip_address,details
		FROM audit_events WHERE `+where+` ORDER BY occurred_at DESC, id DESC
		LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanAuditRow(rows)
		if err != nil {
			return AuditPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

// AuditActions lists the action names present in the trail, so the console can
// offer the filter values this deployment actually has rather than a list of
// every action the code might ever write.
func (s *Store) AuditActions(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT action FROM audit_events ORDER BY action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return nil, err
		}
		items = append(items, action)
	}
	return items, rows.Err()
}

// rowScanner is what pgx.Rows satisfies; naming it keeps the scan in one place.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAuditRow(rows rowScanner) (map[string]any, error) {
	var id int64
	var at time.Time
	var actor, action, resourceType, resourceID, outcome, ip string
	var details any
	if err := rows.Scan(&id, &at, &actor, &action, &resourceType, &resourceID, &outcome, &ip, &details); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": id, "occurredAt": at, "actor": actor, "action": action,
		"resourceType": resourceType, "resourceId": resourceID,
		"outcome": outcome, "ipAddress": ip, "details": details,
	}, nil
}
