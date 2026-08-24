package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/korean"
)

type PersonalSecret struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	KeyVersion int        `json:"keyVersion"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (s *Store) activeUserKey(ctx context.Context, userID string) ([]byte, int, error) {
	var encrypted string
	var version int
	err := s.pool.QueryRow(ctx, `SELECT encrypted_data_key,version FROM user_keyrings WHERE user_id=$1 AND active`, userID).Scan(&encrypted, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.EnsureUserKeyring(ctx, userID); err != nil {
			return nil, 0, err
		}
		err = s.pool.QueryRow(ctx, `SELECT encrypted_data_key,version FROM user_keyrings WHERE user_id=$1 AND active`, userID).Scan(&encrypted, &version)
	}
	if err != nil {
		return nil, 0, err
	}
	key, err := s.cipher.Decrypt(encrypted, fmt.Sprintf("user-key:%s:%d", userID, version))
	return key, version, err
}

func (s *Store) ListPersonalSecrets(ctx context.Context, userID string) ([]PersonalSecret, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,kind,key_version,last_used_at,created_at,updated_at FROM personal_secrets WHERE user_id=$1 ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PersonalSecret{}
	for rows.Next() {
		var item PersonalSecret
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.KeyVersion, &item.LastUsedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PutPersonalSecret(ctx context.Context, userID, name, kind, value string) (PersonalSecret, error) {
	key, version, err := s.activeUserKey(ctx, userID)
	if err != nil {
		return PersonalSecret{}, err
	}
	userCipher, err := cryptox.New(key)
	if err != nil {
		return PersonalSecret{}, err
	}
	id := uuid.NewString()
	encrypted, err := userCipher.Encrypt([]byte(value), fmt.Sprintf("personal-secret:%s:%s:%d", userID, id, version))
	if err != nil {
		return PersonalSecret{}, err
	}
	var item PersonalSecret
	err = s.pool.QueryRow(ctx, `INSERT INTO personal_secrets(id,user_id,name,kind,encrypted_value,key_version) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,name,kind,key_version,last_used_at,created_at,updated_at`, id, userID, name, kind, encrypted, version).Scan(
		&item.ID, &item.Name, &item.Kind, &item.KeyVersion, &item.LastUsedAt, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, conflictIfTaken(err, "같은 이름의 개인 시크릿이 이미 있습니다. 다른 이름을 쓰세요")
}

func (s *Store) DeletePersonalSecret(ctx context.Context, userID, id string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM personal_secrets WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RotatePersonalKey(ctx context.Context, userID string) (int, error) {
	oldKey, oldVersion, err := s.activeUserKey(ctx, userID)
	if err != nil {
		return 0, err
	}
	oldCipher, err := cryptox.New(oldKey)
	if err != nil {
		return 0, err
	}
	newKey, err := cryptox.RandomKey()
	if err != nil {
		return 0, err
	}
	newVersion := oldVersion + 1
	newCipher, _ := cryptox.New(newKey)
	wrapped, err := s.cipher.Encrypt(newKey, fmt.Sprintf("user-key:%s:%d", userID, newVersion))
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id,encrypted_value FROM personal_secrets WHERE user_id=$1 FOR UPDATE`, userID)
	if err != nil {
		return 0, err
	}
	type encryptedSecret struct{ id, value string }
	var values []encryptedSecret
	for rows.Next() {
		var item encryptedSecret
		if err := rows.Scan(&item.id, &item.value); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, item)
	}
	rows.Close()
	for _, item := range values {
		plain, err := oldCipher.Decrypt(item.value, fmt.Sprintf("personal-secret:%s:%s:%d", userID, item.id, oldVersion))
		if err != nil {
			return 0, fmt.Errorf("decrypt secret %s during rotation: %w", item.id, err)
		}
		reencrypted, err := newCipher.Encrypt(plain, fmt.Sprintf("personal-secret:%s:%s:%d", userID, item.id, newVersion))
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_secrets SET encrypted_value=$1,key_version=$2,updated_at=now() WHERE id=$3`, reencrypted, newVersion, item.id); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE user_keyrings SET active=false,retired_at=now() WHERE user_id=$1 AND active`, userID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_keyrings(id,user_id,version,encrypted_data_key,active) VALUES($1,$2,$3,$4,true)`, uuid.NewString(), userID, newVersion, wrapped); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return newVersion, nil
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func (s *Store) CreateAPIKey(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (APIKey, string, error) {
	random, err := cryptox.RandomToken(32)
	if err != nil {
		return APIKey{}, "", err
	}
	token := "ahk_" + random
	item := APIKey{ID: uuid.NewString(), Name: name, Prefix: token[:12], Scopes: scopes, ExpiresAt: expiresAt}
	err = s.pool.QueryRow(ctx, `INSERT INTO api_keys(id,user_id,name,prefix,token_hash,scopes,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`, item.ID, userID, item.Name, item.Prefix, cryptox.TokenHash(token), item.Scopes, expiresAt).Scan(&item.CreatedAt)
	return item, token, err
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,prefix,scopes,expires_at,last_used_at,created_at FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []APIKey{}
	for rows.Next() {
		var item APIKey
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &item.Scopes, &item.ExpiresAt, &item.LastUsedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, userID, id string) error {
	command, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserByAPIKey(ctx context.Context, token string) (User, error) {
	user, _, err := s.UserAndScopesByAPIKey(ctx, token)
	return user, err
}

func (s *Store) UserAndScopesByAPIKey(ctx context.Context, token string) (User, []string, error) {
	var user User
	var scopes []string
	err := s.pool.QueryRow(ctx, `SELECT `+prefixColumns("u", userColumns)+`,k.scopes FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.status='active'`, cryptox.TokenHash(token)).Scan(append(user.scanTargets(), &scopes)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, nil, ErrNotFound
	}
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE token_hash=$1`, cryptox.TokenHash(token))
	}
	return user, scopes, err
}

// RevealPersonalSecret decrypts one of a user's secrets. It exists so the
// control plane can hand a credential to a Runtime it is provisioning; every
// call is a disclosure, so the read is recorded through last_used_at and callers
// must never return the value to a browser.
func (s *Store) RevealPersonalSecret(ctx context.Context, userID, id string) (PersonalSecret, string, error) {
	var item PersonalSecret
	var encrypted string
	var version int
	err := s.pool.QueryRow(ctx, `SELECT id,name,kind,key_version,last_used_at,created_at,updated_at,encrypted_value,key_version FROM personal_secrets WHERE id=$1 AND user_id=$2`, id, userID).
		Scan(&item.ID, &item.Name, &item.Kind, &item.KeyVersion, &item.LastUsedAt, &item.CreatedAt, &item.UpdatedAt, &encrypted, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return PersonalSecret{}, "", ErrNotFound
	}
	if err != nil {
		return PersonalSecret{}, "", err
	}
	key, activeVersion, err := s.activeUserKey(ctx, userID)
	if err != nil {
		return PersonalSecret{}, "", err
	}
	if activeVersion != version {
		// The keyring was rotated without re-wrapping this row; rotation rewrites
		// every secret, so this means the row predates a failed rotation.
		//
		// A Conflict rather than a plain error, and a sentence rather than
		// arithmetic. Measured: attaching this secret to a workspace answered
		// `500 요청을 처리하지 못했습니다: secret "probe-secret" was encrypted with
		// key version 14 but the active version is 15` — an English report of this
		// platform's own key bookkeeping, delivered as though the request had
		// broken something. Nothing is broken and the person can fix it: saving
		// the secret again wraps it with the key in use.
		return PersonalSecret{}, "", Conflict{Message: fmt.Sprintf(
			"개인 Secret %q%s 읽지 못했습니다 — 예전 키로 암호화되어 있습니다. 이 Secret을 다시 저장하면 현재 키로 다시 암호화됩니다.",
			item.Name, korean.Object(item.Name))}
	}
	userCipher, err := cryptox.New(key)
	if err != nil {
		return PersonalSecret{}, "", err
	}
	plain, err := userCipher.Decrypt(encrypted, fmt.Sprintf("personal-secret:%s:%s:%d", userID, item.ID, version))
	if err != nil {
		return PersonalSecret{}, "", err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE personal_secrets SET last_used_at=now() WHERE id=$1`, id)
	return item, string(plain), nil
}
