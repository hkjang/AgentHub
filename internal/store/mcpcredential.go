package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MCPCredentialScope distinguishes the shared credential an administrator
// configures for a server from a user's personal credential for the same server.
const (
	MCPCredentialShared  = "shared"
	MCPCredentialPerUser = "user"
)

// PutMCPCredential stores an encrypted credential for an MCP server. An empty
// ownerID stores the shared platform credential.
func (s *Store) PutMCPCredential(ctx context.Context, serverID, ownerID, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("credential value is required")
	}
	encrypted, err := s.cipher.Encrypt([]byte(value), "mcp-credential:"+serverID)
	if err != nil {
		return err
	}
	// The partial unique indexes differ for shared and per-user rows, so the two
	// cases need their own conflict targets.
	if ownerID == "" {
		_, err = s.pool.Exec(ctx, `INSERT INTO mcp_credentials(id,server_id,owner_id,ciphertext) VALUES($1,$2,NULL,$3)
			ON CONFLICT (server_id) WHERE owner_id IS NULL DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=now()`, uuid.NewString(), serverID, encrypted)
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO mcp_credentials(id,server_id,owner_id,ciphertext) VALUES($1,$2,$3,$4)
		ON CONFLICT (server_id, owner_id) WHERE owner_id IS NOT NULL DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=now()`, uuid.NewString(), serverID, ownerID, encrypted)
	return err
}

func (s *Store) DeleteMCPCredential(ctx context.Context, serverID, ownerID string) error {
	var tag = "owner_id IS NULL"
	args := []any{serverID}
	if ownerID != "" {
		tag = "owner_id=$2"
		args = append(args, ownerID)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM mcp_credentials WHERE server_id=$1 AND `+tag, args...)
	return err
}

// MCPCredential returns the plaintext credential a given user should present to
// a server: their own when the server is configured for per-user credentials,
// otherwise the shared one. It reports ErrNotFound when nothing is configured so
// the caller can decide whether that is fatal.
func (s *Store) MCPCredential(ctx context.Context, server MCPServer, ownerID string) (string, error) {
	query := `SELECT ciphertext FROM mcp_credentials WHERE server_id=$1 AND owner_id IS NULL`
	args := []any{server.ID}
	if server.PerUserCredential {
		if ownerID == "" {
			return "", ErrNotFound
		}
		query = `SELECT ciphertext FROM mcp_credentials WHERE server_id=$1 AND owner_id=$2`
		args = append(args, ownerID)
	}
	var ciphertext string
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&ciphertext); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	plain, err := s.cipher.Decrypt(ciphertext, "mcp-credential:"+server.ID)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// MCPCredentialStatus reports, per server, whether the credential this user would
// present is configured. The UI needs this to tell an operator that an agent will
// reach a server unauthenticated.
func (s *Store) MCPCredentialStatus(ctx context.Context, ownerID string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT server_id, owner_id IS NULL FROM mcp_credentials WHERE owner_id IS NULL OR owner_id=$1`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shared, personal := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var serverID string
		var isShared bool
		if err := rows.Scan(&serverID, &isShared); err != nil {
			return nil, err
		}
		if isShared {
			shared[serverID] = true
		} else {
			personal[serverID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	servers, err := s.MCPServers(ctx)
	if err != nil {
		return nil, err
	}
	status := map[string]bool{}
	for _, server := range servers {
		if server.AuthType == "" || server.AuthType == "none" {
			continue
		}
		if server.PerUserCredential {
			status[server.ID] = personal[server.ID]
		} else {
			status[server.ID] = shared[server.ID]
		}
	}
	return status, nil
}
