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

func (s *Store) Notifications(ctx context.Context, userID string) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,type,title,message,resource_url,read_at,created_at FROM notifications WHERE user_id=$1 ORDER BY created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Notification{}
	for rows.Next() {
		var item Notification
		if err := rows.Scan(&item.ID, &item.Type, &item.Title, &item.Message, &item.ResourceURL, &item.ReadAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReadNotification(ctx context.Context, userID, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}
