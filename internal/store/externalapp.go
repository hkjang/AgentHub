package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ExternalApp is an application the platform drives but does not run: an address,
// a credential and enough about its API to call it correctly.
//
// It exists because some products are not one container. Dify is a dozen
// services, and a site that runs one wants its apps callable from a task, not a
// second copy of it inside a Pod.
type ExternalApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
	// AppKind decides which endpoint answers and where the result is: a workflow
	// returns outputs, a chat app returns an answer.
	AppKind     string `json:"appKind"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// SecretConfigured says whether a credential is stored, never what it is.
	SecretConfigured bool `json:"secretConfigured"`
}

// ExternalAppProviders and ExternalAppKinds are what the schema accepts.
var (
	ExternalAppProviders = []string{"dify"}
	ExternalAppKinds     = []string{"workflow", "chat"}
)

func (s *Store) ExternalApps(ctx context.Context) ([]ExternalApp, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,provider,base_url,app_kind,description,enabled,secret_value IS NOT NULL FROM external_apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ExternalApp{}
	for rows.Next() {
		var item ExternalApp
		if err := rows.Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.AppKind, &item.Description, &item.Enabled, &item.SecretConfigured); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ExternalAppByID resolves one entry together with its credential, which is what
// the execution plane needs and nothing else does.
func (s *Store) ExternalAppByID(ctx context.Context, id string) (ExternalApp, string, error) {
	var item ExternalApp
	var encrypted *string
	err := s.pool.QueryRow(ctx, `SELECT id,name,provider,base_url,app_kind,description,enabled,secret_value,secret_value IS NOT NULL FROM external_apps WHERE id=$1`, id).
		Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.AppKind, &item.Description, &item.Enabled, &encrypted, &item.SecretConfigured)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, "", ErrNotFound
	}
	if err != nil {
		return item, "", err
	}
	if encrypted == nil {
		return item, "", nil
	}
	plain, decryptErr := s.cipher.Decrypt(*encrypted, externalAppSecretContext(item.ID))
	if decryptErr != nil {
		// An unreadable credential is reported as absent rather than as garbage:
		// the caller then says "no credential", which is what the operator has to
		// fix either way.
		return item, "", nil
	}
	return item, string(plain), nil
}

// UpsertExternalApp writes one entry. An omitted secret keeps the stored one, so
// renaming an app does not silently drop its credential.
func (s *Store) UpsertExternalApp(ctx context.Context, item ExternalApp, secret *string) (ExternalApp, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	var encrypted *string
	if secret != nil && strings.TrimSpace(*secret) != "" {
		value, err := s.cipher.Encrypt([]byte(*secret), externalAppSecretContext(item.ID))
		if err != nil {
			return item, err
		}
		encrypted = &value
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO external_apps(id,name,provider,base_url,app_kind,description,enabled,secret_value)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,provider=excluded.provider,base_url=excluded.base_url,app_kind=excluded.app_kind,
			description=excluded.description,enabled=excluded.enabled,
			secret_value=COALESCE(excluded.secret_value,external_apps.secret_value),updated_at=now()
		RETURNING id,name,provider,base_url,app_kind,description,enabled,secret_value IS NOT NULL`,
		item.ID, item.Name, item.Provider, item.BaseURL, item.AppKind, item.Description, item.Enabled, encrypted).
		Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.AppKind, &item.Description, &item.Enabled, &item.SecretConfigured)
	return item, err
}

func (s *Store) DeleteExternalApp(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM external_apps WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// externalAppSecretContext binds a credential to the row it belongs to, so a
// copied ciphertext cannot be decrypted under another app's identity.
func externalAppSecretContext(id string) string { return "external-app:" + id }
