package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RuntimeImage struct {
	ID          string    `json:"id"`
	RuntimeType string    `json:"runtimeType"`
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	Version     string    `json:"version"`
	Digest      string    `json:"digest"`
	SBOMURI     string    `json:"sbomUri"`
	Approved    bool      `json:"approved"`
	Deprecated  bool      `json:"deprecated"`
	CreatedAt   time.Time `json:"createdAt"`
}
type ModelEndpoint struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	BaseURL          string `json:"baseUrl"`
	DefaultModel     string `json:"defaultModel"`
	SecretConfigured bool   `json:"secretConfigured"`
	Enabled          bool   `json:"enabled"`
	// InputPricePerMTok and OutputPricePerMTok price this endpoint's tokens, per
	// million, in Currency. Zero means the endpoint is not priced, which the
	// usage report says rather than showing a confident zero.
	InputPricePerMTok  float64 `json:"inputPricePerMTok"`
	OutputPricePerMTok float64 `json:"outputPricePerMTok"`
	Currency           string  `json:"currency"`
	// What the last check of this endpoint found, and when. Kept rather than
	// asked on every read: a listing of several endpoints must not make several
	// outbound calls, and the last answer is worth seeing even when the endpoint
	// has since stopped replying.
	Health       string     `json:"health"`
	HealthDetail string     `json:"healthDetail,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}
type MCPServer struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Mode             string `json:"mode"`
	Transport        string `json:"transport"`
	Endpoint         string `json:"endpoint"`
	Image            string `json:"image"`
	Port             int    `json:"port"`
	RiskLevel        string `json:"riskLevel"`
	ApprovalRequired bool   `json:"approvalRequired"`
	Enabled          bool   `json:"enabled"`
	// AuthType selects how the runtime authenticates: none, bearer, header or
	// basic. AuthHeader names the header for auth_type=header.
	AuthType   string `json:"authType"`
	AuthHeader string `json:"authHeader"`
	// PerUserCredential routes the credential through each user's own keyring
	// instead of one shared platform credential.
	PerUserCredential    bool      `json:"perUserCredential"`
	CredentialConfigured bool      `json:"credentialConfigured"`
	CreatedAt            time.Time `json:"createdAt"`
}
type MCPBundle struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ServerIDs   []string  `json:"serverIds"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
}

type PolicyProfile struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Spec        map[string]any `json:"spec"`
	Enabled     bool           `json:"enabled"`
	CreatedAt   time.Time      `json:"createdAt"`
}

func policyTable(kind string) (string, error) {
	switch kind {
	case "security":
		return "security_profiles", nil
	case "network":
		return "network_profiles", nil
	default:
		return "", errors.New("unsupported policy profile kind")
	}
}

func (s *Store) PolicyProfiles(ctx context.Context, kind string) ([]PolicyProfile, error) {
	table, err := policyTable(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,name,description,spec,enabled,created_at FROM `+table+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PolicyProfile{}
	for rows.Next() {
		var item PolicyProfile
		var spec []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &spec, &item.Enabled, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(spec, &item.Spec); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertPolicyProfile(ctx context.Context, kind string, item PolicyProfile) (PolicyProfile, error) {
	table, err := policyTable(kind)
	if err != nil {
		return item, err
	}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Spec == nil {
		item.Spec = map[string]any{}
	}
	spec, err := json.Marshal(item.Spec)
	if err != nil {
		return item, err
	}
	var savedSpec []byte
	err = s.pool.QueryRow(ctx, `INSERT INTO `+table+`(id,name,description,spec,enabled) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,spec=excluded.spec,enabled=excluded.enabled,updated_at=now() RETURNING id,name,description,spec,enabled,created_at`, item.ID, item.Name, item.Description, spec, item.Enabled).Scan(&item.ID, &item.Name, &item.Description, &savedSpec, &item.Enabled, &item.CreatedAt)
	if err == nil {
		err = json.Unmarshal(savedSpec, &item.Spec)
	}
	return item, err
}

func (s *Store) PolicyProfileByID(ctx context.Context, kind, id string) (PolicyProfile, error) {
	table, err := policyTable(kind)
	if err != nil {
		return PolicyProfile{}, err
	}
	var item PolicyProfile
	var spec []byte
	err = s.pool.QueryRow(ctx, `SELECT id,name,description,spec,enabled,created_at FROM `+table+` WHERE id=$1 AND enabled`, id).Scan(&item.ID, &item.Name, &item.Description, &spec, &item.Enabled, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(spec, &item.Spec)
	}
	return item, err
}

func (s *Store) AllRuntimeProfiles(ctx context.Context) ([]RuntimeProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,description,cpu_millis,memory_mb,storage_gb,gpu_count,idle_timeout_seconds,enabled FROM runtime_profiles ORDER BY cpu_millis`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RuntimeProfile{}
	for rows.Next() {
		var item RuntimeProfile
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.CPUMillis, &item.MemoryMB, &item.StorageGB, &item.GPUCount, &item.IdleTimeoutSeconds, &item.Enabled); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertRuntimeProfile(ctx context.Context, item RuntimeProfile) (RuntimeProfile, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO runtime_profiles(id,name,description,cpu_millis,memory_mb,storage_gb,gpu_count,idle_timeout_seconds,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,cpu_millis=excluded.cpu_millis,memory_mb=excluded.memory_mb,storage_gb=excluded.storage_gb,gpu_count=excluded.gpu_count,idle_timeout_seconds=excluded.idle_timeout_seconds,enabled=excluded.enabled,updated_at=now() RETURNING id,name,description,cpu_millis,memory_mb,storage_gb,gpu_count,idle_timeout_seconds,enabled`, item.ID, item.Name, item.Description, item.CPUMillis, item.MemoryMB, item.StorageGB, item.GPUCount, item.IdleTimeoutSeconds, item.Enabled).Scan(&item.ID, &item.Name, &item.Description, &item.CPUMillis, &item.MemoryMB, &item.StorageGB, &item.GPUCount, &item.IdleTimeoutSeconds, &item.Enabled)
	return item, err
}
func (s *Store) RuntimeImages(ctx context.Context) ([]RuntimeImage, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,runtime_type,name,image,version,digest,sbom_uri,approved,deprecated,created_at FROM runtime_images ORDER BY runtime_type,version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RuntimeImage{}
	for rows.Next() {
		var item RuntimeImage
		if err := rows.Scan(&item.ID, &item.RuntimeType, &item.Name, &item.Image, &item.Version, &item.Digest, &item.SBOMURI, &item.Approved, &item.Deprecated, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) ApprovedRuntimeImage(ctx context.Context, runtimeType string) (RuntimeImage, error) {
	var item RuntimeImage
	err := s.pool.QueryRow(ctx, `SELECT id,runtime_type,name,image,version,digest,sbom_uri,approved,deprecated,created_at FROM runtime_images WHERE runtime_type=$1 AND approved AND NOT deprecated ORDER BY created_at DESC LIMIT 1`, runtimeType).Scan(&item.ID, &item.RuntimeType, &item.Name, &item.Image, &item.Version, &item.Digest, &item.SBOMURI, &item.Approved, &item.Deprecated, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeImage{}, ErrNotFound
	}
	return item, err
}

// RuntimeImageByID resolves a specific catalog entry. Agents pin the image they
// were created against so a later catalog change cannot silently move a running
// definition onto a different build.
func (s *Store) RuntimeImageByID(ctx context.Context, id string) (RuntimeImage, error) {
	var item RuntimeImage
	err := s.pool.QueryRow(ctx, `SELECT id,runtime_type,name,image,version,digest,sbom_uri,approved,deprecated,created_at FROM runtime_images WHERE id=$1`, id).Scan(&item.ID, &item.RuntimeType, &item.Name, &item.Image, &item.Version, &item.Digest, &item.SBOMURI, &item.Approved, &item.Deprecated, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeImage{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpsertRuntimeImage(ctx context.Context, item RuntimeImage) (RuntimeImage, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO runtime_images(id,runtime_type,name,image,version,digest,sbom_uri,approved,deprecated) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO UPDATE SET runtime_type=excluded.runtime_type,name=excluded.name,image=excluded.image,version=excluded.version,digest=excluded.digest,sbom_uri=excluded.sbom_uri,approved=excluded.approved,deprecated=excluded.deprecated RETURNING id,runtime_type,name,image,version,digest,sbom_uri,approved,deprecated,created_at`, item.ID, item.RuntimeType, item.Name, item.Image, item.Version, item.Digest, item.SBOMURI, item.Approved, item.Deprecated).Scan(&item.ID, &item.RuntimeType, &item.Name, &item.Image, &item.Version, &item.Digest, &item.SBOMURI, &item.Approved, &item.Deprecated, &item.CreatedAt)
	return item, err
}
func (s *Store) ModelEndpoints(ctx context.Context) ([]ModelEndpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,provider,base_url,default_model,secret_value IS NOT NULL,enabled,input_price_per_mtok,output_price_per_mtok,currency,health,health_detail,checked_at,created_at FROM model_endpoints ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelEndpoint{}
	for rows.Next() {
		var item ModelEndpoint
		if err := rows.Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.DefaultModel, &item.SecretConfigured, &item.Enabled, &item.InputPricePerMTok, &item.OutputPricePerMTok, &item.Currency, &item.Health, &item.HealthDetail, &item.CheckedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) EnabledModelEndpoints(ctx context.Context) ([]ModelEndpoint, error) {
	items, err := s.ModelEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ModelEndpoint, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			result = append(result, item)
		}
	}
	return result, nil
}
func (s *Store) ModelEndpointByID(ctx context.Context, id string) (ModelEndpoint, string, error) {
	var item ModelEndpoint
	var encrypted *string
	err := s.pool.QueryRow(ctx, `SELECT id,name,provider,base_url,default_model,secret_value,secret_value IS NOT NULL,enabled,created_at FROM model_endpoints WHERE id=$1 AND enabled`, id).Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.DefaultModel, &encrypted, &item.SecretConfigured, &item.Enabled, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, "", ErrNotFound
	}
	if err != nil {
		return item, "", err
	}
	if encrypted == nil {
		return item, "", nil
	}
	plain, err := s.cipher.Decrypt(*encrypted, "model-endpoint:"+item.ID)
	if err != nil {
		return item, "", nil
	}
	return item, string(plain), nil
}
func (s *Store) UpsertModelEndpoint(ctx context.Context, item ModelEndpoint, secret *string) (ModelEndpoint, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	var encrypted *string
	if secret != nil && strings.TrimSpace(*secret) != "" {
		value, err := s.cipher.Encrypt([]byte(*secret), "model-endpoint:"+item.ID)
		if err != nil {
			return item, err
		}
		encrypted = &value
	}
	if strings.TrimSpace(item.Currency) == "" {
		item.Currency = "KRW"
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO model_endpoints(id,name,provider,base_url,default_model,secret_value,enabled,input_price_per_mtok,output_price_per_mtok,currency)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,provider=excluded.provider,base_url=excluded.base_url,default_model=excluded.default_model,
			secret_value=COALESCE(excluded.secret_value,model_endpoints.secret_value),enabled=excluded.enabled,
			input_price_per_mtok=excluded.input_price_per_mtok,output_price_per_mtok=excluded.output_price_per_mtok,currency=excluded.currency,updated_at=now()
		RETURNING id,name,provider,base_url,default_model,secret_value IS NOT NULL,enabled,input_price_per_mtok,output_price_per_mtok,currency,created_at`,
		item.ID, item.Name, item.Provider, item.BaseURL, item.DefaultModel, encrypted, item.Enabled, item.InputPricePerMTok, item.OutputPricePerMTok, item.Currency).
		Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.DefaultModel, &item.SecretConfigured, &item.Enabled, &item.InputPricePerMTok, &item.OutputPricePerMTok, &item.Currency, &item.CreatedAt)
	return item, err
}
func (s *Store) MCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,description,mode,transport,endpoint,image,port,risk_level,approval_required,enabled,auth_type,auth_header,per_user_credential,EXISTS(SELECT 1 FROM mcp_credentials c WHERE c.server_id=mcp_servers.id AND c.owner_id IS NULL),created_at FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MCPServer{}
	for rows.Next() {
		var item MCPServer
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Mode, &item.Transport, &item.Endpoint, &item.Image, &item.Port, &item.RiskLevel, &item.ApprovalRequired, &item.Enabled, &item.AuthType, &item.AuthHeader, &item.PerUserCredential, &item.CredentialConfigured, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) EnabledMCPServers(ctx context.Context) ([]MCPServer, error) {
	items, err := s.MCPServers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]MCPServer, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			result = append(result, item)
		}
	}
	return result, nil
}

// MCPServerByID resolves one server, including how it authenticates.
func (s *Store) MCPServerByID(ctx context.Context, id string) (MCPServer, error) {
	var item MCPServer
	err := s.pool.QueryRow(ctx, `SELECT id,name,description,mode,transport,endpoint,image,port,risk_level,approval_required,enabled,auth_type,auth_header,per_user_credential,EXISTS(SELECT 1 FROM mcp_credentials c WHERE c.server_id=mcp_servers.id AND c.owner_id IS NULL),created_at FROM mcp_servers WHERE id=$1`, id).Scan(&item.ID, &item.Name, &item.Description, &item.Mode, &item.Transport, &item.Endpoint, &item.Image, &item.Port, &item.RiskLevel, &item.ApprovalRequired, &item.Enabled, &item.AuthType, &item.AuthHeader, &item.PerUserCredential, &item.CredentialConfigured, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPServer{}, ErrNotFound
	}
	return item, err
}

func (s *Store) UpsertMCPServer(ctx context.Context, item MCPServer) (MCPServer, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Transport == "" {
		item.Transport = "streamable-http"
	}
	if item.RiskLevel == "" {
		item.RiskLevel = "low"
	}
	if item.Port <= 0 {
		item.Port = 8000
	}
	if item.AuthType == "" {
		item.AuthType = "none"
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO mcp_servers(id,name,description,mode,transport,endpoint,image,port,risk_level,approval_required,enabled,auth_type,auth_header,per_user_credential) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,mode=excluded.mode,transport=excluded.transport,endpoint=excluded.endpoint,image=excluded.image,port=excluded.port,risk_level=excluded.risk_level,approval_required=excluded.approval_required,enabled=excluded.enabled,auth_type=excluded.auth_type,auth_header=excluded.auth_header,per_user_credential=excluded.per_user_credential,updated_at=now() RETURNING id,name,description,mode,transport,endpoint,image,port,risk_level,approval_required,enabled,auth_type,auth_header,per_user_credential,EXISTS(SELECT 1 FROM mcp_credentials c WHERE c.server_id=mcp_servers.id AND c.owner_id IS NULL),created_at`, item.ID, item.Name, item.Description, item.Mode, item.Transport, item.Endpoint, item.Image, item.Port, item.RiskLevel, item.ApprovalRequired, item.Enabled, item.AuthType, item.AuthHeader, item.PerUserCredential).Scan(&item.ID, &item.Name, &item.Description, &item.Mode, &item.Transport, &item.Endpoint, &item.Image, &item.Port, &item.RiskLevel, &item.ApprovalRequired, &item.Enabled, &item.AuthType, &item.AuthHeader, &item.PerUserCredential, &item.CredentialConfigured, &item.CreatedAt)
	return item, err
}
func (s *Store) MCPBundles(ctx context.Context, enabledOnly bool) ([]MCPBundle, error) {
	query := `SELECT id,name,description,server_ids,enabled,created_at FROM mcp_bundles`
	if enabledOnly {
		query += ` WHERE enabled`
	}
	query += ` ORDER BY name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MCPBundle{}
	for rows.Next() {
		var item MCPBundle
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.ServerIDs, &item.Enabled, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) UpsertMCPBundle(ctx context.Context, item MCPBundle) (MCPBundle, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO mcp_bundles(id,name,description,server_ids,enabled) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,server_ids=excluded.server_ids,enabled=excluded.enabled,updated_at=now() RETURNING id,name,description,server_ids,enabled,created_at`, item.ID, item.Name, item.Description, item.ServerIDs, item.Enabled).Scan(&item.ID, &item.Name, &item.Description, &item.ServerIDs, &item.Enabled, &item.CreatedAt)
	return item, err
}
func (s *Store) MCPServersForBundle(ctx context.Context, bundleID string) ([]MCPServer, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id,s.name,s.description,s.mode,s.transport,s.endpoint,s.image,s.port,s.risk_level,s.approval_required,s.enabled,s.auth_type,s.auth_header,s.per_user_credential,EXISTS(SELECT 1 FROM mcp_credentials c WHERE c.server_id=s.id AND c.owner_id IS NULL),s.created_at FROM mcp_servers s JOIN mcp_bundles b ON s.id=ANY(b.server_ids) WHERE b.id=$1 AND b.enabled AND s.enabled ORDER BY s.name`, bundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MCPServer{}
	for rows.Next() {
		var item MCPServer
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Mode, &item.Transport, &item.Endpoint, &item.Image, &item.Port, &item.RiskLevel, &item.ApprovalRequired, &item.Enabled, &item.AuthType, &item.AuthHeader, &item.PerUserCredential, &item.CredentialConfigured, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY display_name,username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []User{}
	for rows.Next() {
		item, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) UpdateUserGovernance(ctx context.Context, id, role, status string, managerID *string) (User, error) {
	if role != "user" && role != "manager" && role != "admin" {
		return User{}, errors.New("invalid role")
	}
	if status != "active" && status != "disabled" {
		return User{}, errors.New("invalid status")
	}
	return scanUser(s.pool.QueryRow(ctx, `UPDATE users SET role=$1,status=$2,manager_id=$3,updated_at=now() WHERE id=$4 RETURNING `+userColumns, role, status, managerID, id))
}

// RecordModelEndpointHealth keeps what a check found.
func (s *Store) RecordModelEndpointHealth(ctx context.Context, id, health, detail string) error {
	_, err := s.pool.Exec(ctx, `UPDATE model_endpoints SET health=$2, health_detail=$3, checked_at=now() WHERE id=$1`,
		id, health, detail)
	return err
}
