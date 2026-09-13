package store

import (
	"context"
	"strconv"
	"time"
)

// Mail delivery statuses. queued waits for the sender; sending is claimed by
// one; sent and failed are final.
const (
	MailQueued  = "queued"
	MailSending = "sending"
	MailSent    = "sent"
	MailFailed  = "failed"
)

// MailDelivery is one attempt to reach one person by mail, kept whether or not
// it worked. There is no body column on purpose: the subject and the recipient
// answer "did it go out", and a body kept here would be a second place the
// contents of a notice could be read from.
type MailDelivery struct {
	ID           string     `json:"id"`
	Event        string     `json:"event"`
	UserID       string     `json:"userId,omitempty"`
	Recipient    string     `json:"recipient"`
	Subject      string     `json:"subject"`
	ResourceURL  string     `json:"resourceUrl,omitempty"`
	ActorID      string     `json:"actorId,omitempty"`
	Status       string     `json:"status"`
	Attempts     int        `json:"attempts"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	NextAttempt  *time.Time `json:"nextAttemptAt,omitempty"`
}

// MailSummary is the status breakdown over the whole table, so the screen can
// say "3 failed" even when the page shows only the newest fifty.
type MailSummary struct {
	Total  int            `json:"total"`
	Status map[string]int `json:"status"`
}

// MailPage is what the administrator's delivery list returns.
type MailPage struct {
	Items   []MailDelivery `json:"items"`
	Summary MailSummary    `json:"summary"`
}

const mailColumns = `id,event,coalesce(user_id,''),recipient,subject,resource_url,actor_id,status,attempts,error_message,created_at,updated_at,next_attempt_at`

func scanMailDelivery(row interface{ Scan(...any) error }) (MailDelivery, error) {
	var item MailDelivery
	err := row.Scan(&item.ID, &item.Event, &item.UserID, &item.Recipient, &item.Subject, &item.ResourceURL, &item.ActorID,
		&item.Status, &item.Attempts, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt, &item.NextAttempt)
	return item, err
}

// UserEmails is the one lookup the mailer borrows from the account table:
// which address belongs to each id. Disabled accounts and accounts with no
// address are absent, which the caller reads as "cannot be mailed".
func (s *Store) UserEmails(ctx context.Context, ids []string) (map[string]string, error) {
	addresses := map[string]string{}
	if len(ids) == 0 {
		return addresses, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, email FROM users WHERE id = ANY($1) AND status='active' AND email <> ''`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		addresses[id] = email
	}
	return addresses, rows.Err()
}

// QueueMail records deliveries as given. A queued one waits for the sender; the
// test button inserts its own as already sending so the sweep leaves it alone.
func (s *Store) QueueMail(ctx context.Context, items []MailDelivery) error {
	for _, item := range items {
		var userID any
		if item.UserID != "" {
			userID = item.UserID
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO mail_deliveries(id,event,user_id,recipient,subject,resource_url,actor_id,status,attempts,created_at,updated_at,next_attempt_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$10)`,
			item.ID, item.Event, userID, item.Recipient, item.Subject, item.ResourceURL, item.ActorID, item.Status, item.Attempts, item.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// ClaimMail takes what is due and marks it as this sender's, counting the
// attempt. SKIP LOCKED is what lets two control planes sweep at once without
// one person getting the same mail twice. A delivery left in sending for ten
// minutes belonged to a process that died mid-send and is taken back.
func (s *Store) ClaimMail(ctx context.Context, limit int) ([]MailDelivery, error) {
	rows, err := s.pool.Query(ctx, `UPDATE mail_deliveries SET status='sending', attempts=attempts+1, updated_at=now()
		WHERE id IN (
			SELECT id FROM mail_deliveries
			WHERE (status='queued' AND next_attempt_at <= now())
			   OR (status='sending' AND updated_at < now() - interval '10 minutes')
			ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+mailColumns, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MailDelivery{}
	for rows.Next() {
		item, err := scanMailDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SettleMail records how an attempt ended. A retry goes back to queued with the
// time it becomes due; the error of the failed attempt stays on the row either
// way, so "why has it not arrived" has an answer while it waits.
func (s *Store) SettleMail(ctx context.Context, ids []string, status, message string, retryAt *time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE mail_deliveries SET status=$2, error_message=$3, next_attempt_at=COALESCE($4, next_attempt_at), updated_at=now() WHERE id = ANY($1)`,
		ids, status, message, retryAt)
	return err
}

// ExpireMail gives up on deliveries that have waited past the window — a relay
// that was down for a day, or mail switched off with rows still queued — and
// says so on the row rather than leaving them queued for ever.
func (s *Store) ExpireMail(ctx context.Context, olderThan time.Duration, message string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE mail_deliveries SET status='failed', error_message=$2, updated_at=now()
		WHERE status IN ('queued','sending') AND created_at < $1`, time.Now().UTC().Add(-olderThan), message)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// MailDeliveries lists what was sent, newest first, with a status breakdown
// over the whole table.
func (s *Store) MailDeliveries(ctx context.Context, status string, limit int) (MailPage, error) {
	page := MailPage{Items: []MailDelivery{}, Summary: MailSummary{Status: map[string]int{}}}
	query := `SELECT ` + mailColumns + ` FROM mail_deliveries`
	args := []any{}
	if status != "" {
		args = append(args, status)
		query += ` WHERE status=$1`
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query+` ORDER BY created_at DESC, id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return MailPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanMailDelivery(rows)
		if err != nil {
			return MailPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return MailPage{}, err
	}
	counts, err := s.pool.Query(ctx, `SELECT status, count(*) FROM mail_deliveries GROUP BY 1`)
	if err != nil {
		return MailPage{}, err
	}
	defer counts.Close()
	for counts.Next() {
		var key string
		var count int
		if err := counts.Scan(&key, &count); err != nil {
			return MailPage{}, err
		}
		page.Summary.Status[key] = count
		page.Summary.Total += count
	}
	return page, counts.Err()
}
