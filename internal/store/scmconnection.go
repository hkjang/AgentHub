package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SCMConnection is a credential for a forge, without the credential.
//
// The token is never returned. A caller that needs it asks for it by host, and
// what comes back on this struct is only enough to see that a connection exists
// and whether it has been working.
type SCMConnection struct {
	ID         string     `json:"id"`
	Host       string     `json:"host"`
	Kind       string     `json:"kind"`
	APIBase    string     `json:"apiBase,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	// LastError is why the last attempt to use this connection failed. A token
	// that has been revoked is otherwise indistinguishable from a review that
	// simply had nothing to say.
	LastError string    `json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SCMKinds are the forges the platform knows how to talk to.
var SCMKinds = []string{"github", "gitlab", "gitea", "bitbucket"}

func scmContext(ownerID, host string) string { return "scm-connection:" + ownerID + ":" + host }

// PutSCMConnection stores or replaces the credential for one host.
func (s *Store) PutSCMConnection(ctx context.Context, ownerID, host, kind, apiBase, token string) (SCMConnection, error) {
	host, kind = strings.ToLower(strings.TrimSpace(host)), strings.ToLower(strings.TrimSpace(kind))
	if host == "" || strings.TrimSpace(token) == "" {
		return SCMConnection{}, errors.New("host and token are required")
	}
	known := false
	for _, candidate := range SCMKinds {
		known = known || candidate == kind
	}
	if !known {
		return SCMConnection{}, errors.New("unknown forge kind")
	}
	encrypted, err := s.cipher.Encrypt([]byte(strings.TrimSpace(token)), scmContext(ownerID, host))
	if err != nil {
		return SCMConnection{}, err
	}
	var item SCMConnection
	err = s.pool.QueryRow(ctx, `INSERT INTO scm_connections(id,owner_id,host,kind,api_base,ciphertext)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (owner_id,host) DO UPDATE SET kind=excluded.kind,api_base=excluded.api_base,
			ciphertext=excluded.ciphertext,last_error='',updated_at=now()
		RETURNING id,host,kind,api_base,last_used_at,last_error,created_at,updated_at`,
		uuid.NewString(), ownerID, host, kind, strings.TrimSpace(apiBase), encrypted).
		Scan(&item.ID, &item.Host, &item.Kind, &item.APIBase, &item.LastUsedAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (s *Store) SCMConnections(ctx context.Context, ownerID string) ([]SCMConnection, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,host,kind,api_base,last_used_at,last_error,created_at,updated_at
		FROM scm_connections WHERE owner_id=$1 ORDER BY host`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SCMConnection{}
	for rows.Next() {
		var item SCMConnection
		if err := rows.Scan(&item.ID, &item.Host, &item.Kind, &item.APIBase, &item.LastUsedAt,
			&item.LastError, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteSCMConnection(ctx context.Context, ownerID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM scm_connections WHERE owner_id=$1 AND id=$2`, ownerID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SCMTokenFor returns the credential for a host, with the connection it came
// from. ErrNotFound means nobody has configured one, which is not a failure:
// posting back is something an owner opts into by storing a token.
func (s *Store) SCMTokenFor(ctx context.Context, ownerID, host string) (SCMConnection, string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	var item SCMConnection
	var ciphertext string
	err := s.pool.QueryRow(ctx, `SELECT id,host,kind,api_base,last_used_at,last_error,created_at,updated_at,ciphertext
		FROM scm_connections WHERE owner_id=$1 AND host=$2`, ownerID, host).
		Scan(&item.ID, &item.Host, &item.Kind, &item.APIBase, &item.LastUsedAt, &item.LastError,
			&item.CreatedAt, &item.UpdatedAt, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCMConnection{}, "", ErrNotFound
	}
	if err != nil {
		return SCMConnection{}, "", err
	}
	token, err := s.cipher.Decrypt(ciphertext, scmContext(ownerID, host))
	if err != nil {
		return SCMConnection{}, "", err
	}
	return item, string(token), nil
}

// RecordSCMUse keeps what happened the last time this connection was used.
//
// A revoked token and a review with nothing to say look identical from the
// outside — both post nothing — so the failure is written where the owner of the
// connection can see it.
func (s *Store) RecordSCMUse(ctx context.Context, id, failure string) error {
	_, err := s.pool.Exec(ctx, `UPDATE scm_connections SET last_used_at=now(),last_error=$2,updated_at=now() WHERE id=$1`,
		id, failure)
	return err
}
