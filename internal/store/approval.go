package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Approval struct {
	ID            string          `json:"id"`
	RequesterID   string          `json:"requesterId"`
	RequesterName string          `json:"requesterName"`
	ReviewerID    *string         `json:"reviewerId,omitempty"`
	ResourceType  string          `json:"resourceType"`
	ResourceID    string          `json:"resourceId"`
	Action        string          `json:"action"`
	Reason        string          `json:"reason"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	DecidedAt     *time.Time      `json:"decidedAt,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (s *Store) CreateApproval(ctx context.Context, requesterID, resourceType, resourceID, action, reason string, payload any) (Approval, error) {
	raw, _ := json.Marshal(payload)
	id := uuid.NewString()
	var a Approval
	err := s.pool.QueryRow(ctx, `INSERT INTO approvals(id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload) SELECT $1,$2,manager_id,$3,$4,$5,$6,$7 FROM users WHERE id=$2 RETURNING id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload,status,decided_at,created_at`, id, requesterID, resourceType, resourceID, action, reason, raw).Scan(&a.ID, &a.RequesterID, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt)
	return a, err
}

// NotifyApprovers tells whoever can actually decide this approval, and reports
// how many people that was.
//
// The reviewer is the requester's manager, and manager_id is an org-chart field
// that most deployments never fill in. Both places that ask for an approval sent
// their notification only when there was a reviewer, so on a deployment without an
// org chart nobody with the authority to answer was ever told: the task parked at
// the gate, the requester was told it was waiting, and the approval sat in a queue
// only an administrator could see and only if they thought to look.
//
// An unassigned approval can be decided by an administrator — DecideApproval says
// so — so an administrator is who hears about it. Returning the count is what lets
// the caller notice it told nobody, which is the state this function exists to
// stop being silent.
func (s *Store) NotifyApprovers(ctx context.Context, approval Approval, title, message string) (int, error) {
	if approval.ReviewerID != nil && *approval.ReviewerID != "" {
		if err := s.CreateNotification(ctx, *approval.ReviewerID, "approval", title, message, "/reviews"); err != nil {
			return 0, err
		}
		return 1, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE role='admin' AND status='active'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	admins := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		admins = append(admins, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	told := 0
	for _, id := range admins {
		if err := s.CreateNotification(ctx, id, "approval", title, message, "/reviews"); err != nil {
			return told, err
		}
		told++
	}
	return told, nil
}

// approvalsShown bounds one read of the review queue. It is not a page size a
// reviewer can move — there is no next page — so what does not fit has to be
// counted and said out loud.
const approvalsShown = 200

// ApprovalList is what a reviewer can see, and what they cannot.
//
// Items alone was the whole answer, and it hid the failure this list is least
// able to afford. Two hundred rows came back with the waiting ones first and
// newest-first inside that, so on a deployment with more than two hundred
// requests waiting it was the ones that had waited longest that fell off the
// end — with nothing in the response to say they existed. A request nobody can
// see is not a slow decision, it is a task that never runs again.
//
// This was arranged and measured against a running deployment: 210 waiting, 200
// returned, and the ten missing were the ten oldest.
type ApprovalList struct {
	Items []Approval `json:"items"`
	// Pending counts every request still waiting, not only the ones that fit.
	Pending int `json:"pending"`
	// Hidden is how many waiting requests did not fit. The console says so.
	Hidden int `json:"hidden"`
}

func (s *Store) Approvals(ctx context.Context, reviewerID string, admin bool) (ApprovalList, error) {
	list := ApprovalList{Items: []Approval{}}
	where, args := ``, []any{}
	if !admin {
		where = ` WHERE a.reviewer_id=$1`
		args = append(args, reviewerID)
	}
	pendingWhere := where + ` AND a.status='pending'`
	if where == "" {
		pendingWhere = ` WHERE a.status='pending'`
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM approvals a`+pendingWhere, args...).Scan(&list.Pending); err != nil {
		return list, err
	}
	// Waiting first and oldest first among those: this half of the list is a
	// queue, and the request that has waited longest is the one a reviewer most
	// needs to see. Decided requests stay newest-first, because that half is an
	// archive somebody scrolls back through.
	query := `SELECT a.id,a.requester_id,u.username,a.reviewer_id,a.resource_type,a.resource_id,a.action,a.reason,a.payload,a.status,a.decided_at,a.created_at FROM approvals a JOIN users u ON u.id=a.requester_id` +
		where + ` ORDER BY CASE WHEN a.status='pending' THEN 0 ELSE 1 END,
		CASE WHEN a.status='pending' THEN a.created_at END ASC NULLS LAST,
		a.created_at DESC LIMIT ` + strconv.Itoa(approvalsShown)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return list, err
	}
	defer rows.Close()
	waiting := 0
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.RequesterID, &a.RequesterName, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt); err != nil {
			return list, err
		}
		if a.Status == "pending" {
			waiting++
		}
		list.Items = append(list.Items, a)
	}
	list.Hidden = max(0, list.Pending-waiting)
	return list, rows.Err()
}
func (s *Store) DecideApproval(ctx context.Context, id, reviewerID, decision string, admin bool) (Approval, error) {
	var a Approval
	err := s.pool.QueryRow(ctx, `UPDATE approvals SET status=$1,reviewer_id=$2,decided_at=now() WHERE id=$3 AND status='pending' AND ($4 OR reviewer_id=$2) RETURNING id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload,status,decided_at,created_at`, decision, reviewerID, id, admin).Scan(&a.ID, &a.RequesterID, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	return a, err
}
