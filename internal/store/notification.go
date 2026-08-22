package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Notification struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	ResourceURL string     `json:"resourceUrl"`
	ReadAt      *time.Time `json:"readAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (s *Store) CreateNotification(ctx context.Context, userID, kind, title, message, resourceURL string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notifications(id,user_id,type,title,message,resource_url) VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), userID, kind, title, message, resourceURL)
	return err
}

// Notifications returns what to show in the bell, and how many are unread
// altogether.
//
// Both halves were wrong in the same way. The list took the fifty most recent, so
// an unread notice older than fifty newer ones could not be reached at all — and
// the console counted the unread among those fifty and printed it as the unread
// count. A person with a busy platform saw "읽지 않음 12건" while ninety-nine sat
// unread, and the one telling them a task was waiting for their approval was not
// among the twelve.
//
// So unread comes first. The bell is a list of what somebody has not dealt with,
// not an archive: read notices are there to scroll back through, and they give way
// to anything still waiting. The count is a count of the table.
func (s *Store) Notifications(ctx context.Context, userID string) ([]Notification, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,type,title,message,resource_url,read_at,created_at
		FROM notifications WHERE user_id=$1
		ORDER BY CASE WHEN read_at IS NULL THEN 0 ELSE 1 END, created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Notification{}
	for rows.Next() {
		var item Notification
		if err := rows.Scan(&item.ID, &item.Type, &item.Title, &item.Message, &item.ResourceURL, &item.ReadAt, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&unread); err != nil {
		return nil, 0, err
	}
	return items, unread, nil
}

func (s *Store) ReadNotification(ctx context.Context, userID, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}
