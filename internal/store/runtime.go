package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RuntimeProfile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	CPUMillis          int    `json:"cpuMillis"`
	MemoryMB           int    `json:"memoryMb"`
	StorageGB          int    `json:"storageGb"`
	GPUCount           int    `json:"gpuCount"`
	IdleTimeoutSeconds int    `json:"idleTimeoutSeconds"`
	Enabled            bool   `json:"enabled"`
}

type Template struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	Description      string `json:"description"`
	Category         string `json:"category"`
	RuntimeType      string `json:"runtimeType"`
	RuntimeProfileID string `json:"runtimeProfileId"`
	Version          int    `json:"version"`
	Published        bool   `json:"published"`
}

type Workspace struct {
	ID               string    `json:"id"`
	OwnerID          string    `json:"ownerId"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	SizeGB           int       `json:"sizeGb"`
	RepositoryURL    string    `json:"repositoryUrl"`
	Branch           string    `json:"branch"`
	PVCName          string    `json:"pvcName"`
	SourceSnapshotID *string   `json:"sourceSnapshotId,omitempty"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type WorkspaceSnapshot struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	StorageRef  string    `json:"storageRef"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Agent struct {
	ID                string          `json:"id"`
	OwnerID           string          `json:"ownerId"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	RuntimeType       string          `json:"runtimeType"`
	RuntimeProfileID  *string         `json:"runtimeProfileId,omitempty"`
	RuntimeImageID    *string         `json:"runtimeImageId,omitempty"`
	SecurityProfileID *string         `json:"securityProfileId,omitempty"`
	NetworkProfileID  *string         `json:"networkProfileId,omitempty"`
	MCPBundleID       *string         `json:"mcpBundleId,omitempty"`
	ModelEndpointID   *string         `json:"modelEndpointId,omitempty"`
	WorkspaceID       *string         `json:"workspaceId,omitempty"`
	Version           int             `json:"version"`
	Spec              json.RawMessage `json:"spec"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	Runtime           *Runtime        `json:"runtime,omitempty"`
}

type Runtime struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agentId"`
	OwnerID        string     `json:"ownerId"`
	Status         string     `json:"status"`
	DesiredState   string     `json:"desiredState"`
	CRDName        string     `json:"crdName"`
	PodName        string     `json:"podName"`
	NodeName       string     `json:"nodeName"`
	Endpoint       string     `json:"endpoint"`
	RestartCount   int        `json:"restartCount"`
	FailureReason  string     `json:"failureReason"`
	LastActivityAt *time.Time `json:"lastActivityAt,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	StoppedAt      *time.Time `json:"stoppedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type Dashboard struct {
	Agents       int `json:"agents"`
	Running      int `json:"running"`
	Idle         int `json:"idle"`
	Failed       int `json:"failed"`
	Workspaces   int `json:"workspaces"`
	PendingTasks int `json:"pendingApprovals"`
}

type IdleRuntimeCandidate struct {
	RuntimeID string
	AgentID   string
	OwnerID   string
}

type governanceSettings struct {
	MaxRuntimesPerUser  int `json:"maxRuntimesPerUser"`
	MaxCPUMillisPerUser int `json:"maxCpuMillisPerUser"`
	MaxMemoryMBPerUser  int `json:"maxMemoryMbPerUser"`
	MaxStorageGBPerUser int `json:"maxStorageGbPerUser"`
}

func (s *Store) CheckRuntimeQuota(ctx context.Context, userID, profileID string) error {
	var policy governanceSettings
	if err := s.Setting(ctx, "governance", &policy); err != nil {
		return err
	}
	var count, cpu, memory int
	if err := s.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(p.cpu_millis),0),COALESCE(sum(p.memory_mb),0) FROM agent_runtimes r JOIN agent_definitions a ON a.id=r.agent_id LEFT JOIN runtime_profiles p ON p.id=a.runtime_profile_id WHERE r.owner_id=$1 AND r.desired_state='running'`, userID).Scan(&count, &cpu, &memory); err != nil {
		return err
	}
	var addCPU, addMemory int
	if err := s.pool.QueryRow(ctx, `SELECT cpu_millis,memory_mb FROM runtime_profiles WHERE id=$1 AND enabled`, profileID).Scan(&addCPU, &addMemory); err != nil {
		return err
	}
	if policy.MaxRuntimesPerUser > 0 && count+1 > policy.MaxRuntimesPerUser {
		return fmt.Errorf("사용자 Runtime Quota(%d개)를 초과합니다", policy.MaxRuntimesPerUser)
	}
	if policy.MaxCPUMillisPerUser > 0 && cpu+addCPU > policy.MaxCPUMillisPerUser {
		return fmt.Errorf("사용자 CPU Quota(%dm)를 초과합니다", policy.MaxCPUMillisPerUser)
	}
	if policy.MaxMemoryMBPerUser > 0 && memory+addMemory > policy.MaxMemoryMBPerUser {
		return fmt.Errorf("사용자 Memory Quota(%dMB)를 초과합니다", policy.MaxMemoryMBPerUser)
	}
	return nil
}

func (s *Store) CheckWorkspaceQuota(ctx context.Context, userID string, addGB int) error {
	var policy governanceSettings
	if err := s.Setting(ctx, "governance", &policy); err != nil {
		return err
	}
	var used int
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(size_gb),0) FROM workspaces WHERE owner_id=$1`, userID).Scan(&used); err != nil {
		return err
	}
	if policy.MaxStorageGBPerUser > 0 && used+addGB > policy.MaxStorageGBPerUser {
		return fmt.Errorf("사용자 Storage Quota(%dGB)를 초과합니다", policy.MaxStorageGBPerUser)
	}
	return nil
}

func (s *Store) RuntimeProfiles(ctx context.Context) ([]RuntimeProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,description,cpu_millis,memory_mb,storage_gb,gpu_count,idle_timeout_seconds,enabled FROM runtime_profiles WHERE enabled ORDER BY cpu_millis`)
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

func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,slug,description,category,runtime_type,COALESCE(runtime_profile_id,''),version,published FROM agent_templates WHERE published ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Template{}
	for rows.Next() {
		var item Template
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Description, &item.Category, &item.RuntimeType, &item.RuntimeProfileID, &item.Version, &item.Published); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SeedTemplates(ctx context.Context, adminID string) error {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_templates`).Scan(&count); err != nil || count > 0 {
		return err
	}
	data := []struct{ name, slug, description, category, runtime, profile, prompt string }{
		{"OpenCode Developer", "opencode-developer", "Secure persistent coding workspace with Git and MCP tools.", "Development", "opencode", "rp-developer", "You are a careful enterprise software engineer. Inspect, test, and explain every change."},
		{"Hermes Research", "hermes-research", "Long-running research agent with persistent memory.", "Research", "hermes", "rp-advanced", "Research the request using approved tools and cite the evidence used."},
		{"Qwen Paw Assistant", "qwen-paw", "Autonomous agentic AI assistant powered by Qwen Paw for complex workflows and reasoning.", "Automation", "qwenpaw", "rp-basic", "You are an intelligent agentic assistant powered by Qwen Paw. Plan, orchestrate tools, and solve enterprise problems step-by-step."},
		{"Qwen Code Engineer", "qwen-code", "High-performance coding agent specialized in full-stack architecture, refactoring, and code generation.", "Development", "qwencode", "rp-developer", "You are an expert software engineer powered by Qwen Coder. Produce production-ready, clean, secure, and tested code."},
		{"IT Operator", "it-operator", "Policy-controlled operations assistant with approval gates.", "Operations", "hermes", "rp-basic", "Assist with IT operations. Request approval before any state-changing action."},
	}
	for _, item := range data {
		_, err := s.pool.Exec(ctx, `INSERT INTO agent_templates(id,name,slug,description,category,runtime_type,runtime_profile_id,security_profile_id,network_profile_id,system_prompt,published,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'sp-restricted','np-restricted',$8,true,$9)`, uuid.NewString(), item.name, item.slug, item.description, item.category, item.runtime, item.profile, item.prompt, adminID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Workspaces(ctx context.Context, ownerID string, admin bool) ([]Workspace, error) {
	query := `SELECT id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,source_snapshot_id,status,created_at,updated_at FROM workspaces`
	args := []any{}
	if !admin {
		query += ` WHERE owner_id=$1`
		args = append(args, ownerID)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Workspace{}
	for rows.Next() {
		var item Workspace
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Type, &item.SizeGB, &item.RepositoryURL, &item.Branch, &item.PVCName, &item.SourceSnapshotID, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateWorkspace(ctx context.Context, ownerID string, item Workspace) (Workspace, error) {
	item.ID, item.OwnerID = uuid.NewString(), ownerID
	if item.Type == "" {
		item.Type = "empty"
	}
	if item.SizeGB == 0 {
		item.SizeGB = 10
	}
	item.PVCName = "workspace-" + item.ID[:8]
	err := s.pool.QueryRow(ctx, `INSERT INTO workspaces(id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ready') RETURNING status,created_at,updated_at`, item.ID, item.OwnerID, item.Name, item.Type, item.SizeGB, item.RepositoryURL, item.Branch, item.PVCName).Scan(&item.Status, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (s *Store) WorkspaceByID(ctx context.Context, id, ownerID string, admin bool) (Workspace, error) {
	query := `SELECT id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,source_snapshot_id,status,created_at,updated_at FROM workspaces WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	var item Workspace
	err := s.pool.QueryRow(ctx, query, args...).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Type, &item.SizeGB, &item.RepositoryURL, &item.Branch, &item.PVCName, &item.SourceSnapshotID, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return item, err
}

func (s *Store) WorkspaceSnapshots(ctx context.Context, ownerID string) ([]WorkspaceSnapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT sn.id,sn.workspace_id,sn.name,sn.status,sn.storage_ref,sn.size_bytes,sn.created_at FROM workspace_snapshots sn JOIN workspaces w ON w.id=sn.workspace_id WHERE w.owner_id=$1 ORDER BY sn.created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WorkspaceSnapshot{}
	for rows.Next() {
		var item WorkspaceSnapshot
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Name, &item.Status, &item.StorageRef, &item.SizeBytes, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateWorkspaceSnapshot(ctx context.Context, ownerID, workspaceID, name string) (WorkspaceSnapshot, Workspace, error) {
	workspace, err := s.WorkspaceByID(ctx, workspaceID, ownerID, false)
	if err != nil {
		return WorkspaceSnapshot{}, Workspace{}, err
	}
	id := uuid.NewString()
	item := WorkspaceSnapshot{ID: id, WorkspaceID: workspaceID, Name: name, Status: "pending", StorageRef: "snapshot-" + id[:8]}
	err = s.pool.QueryRow(ctx, `INSERT INTO workspace_snapshots(id,workspace_id,name,status,storage_ref,created_by) VALUES($1,$2,$3,'pending',$4,$5) RETURNING created_at`, item.ID, item.WorkspaceID, item.Name, item.StorageRef, ownerID).Scan(&item.CreatedAt)
	return item, workspace, err
}

func (s *Store) WorkspaceSnapshotByID(ctx context.Context, ownerID, id string) (WorkspaceSnapshot, Workspace, error) {
	var item WorkspaceSnapshot
	var workspaceID string
	err := s.pool.QueryRow(ctx, `SELECT sn.id,sn.workspace_id,sn.name,sn.status,sn.storage_ref,sn.size_bytes,sn.created_at,w.id FROM workspace_snapshots sn JOIN workspaces w ON w.id=sn.workspace_id WHERE sn.id=$1 AND w.owner_id=$2`, id, ownerID).Scan(&item.ID, &item.WorkspaceID, &item.Name, &item.Status, &item.StorageRef, &item.SizeBytes, &item.CreatedAt, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, Workspace{}, ErrNotFound
	}
	if err != nil {
		return item, Workspace{}, err
	}
	workspace, err := s.WorkspaceByID(ctx, workspaceID, ownerID, false)
	return item, workspace, err
}

func (s *Store) UpdateWorkspaceSnapshotStatus(ctx context.Context, id, status string, sizeBytes int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE workspace_snapshots SET status=$1,size_bytes=CASE WHEN $2>0 THEN $2 ELSE size_bytes END WHERE id=$3`, status, sizeBytes, id)
	return err
}

func (s *Store) RestoreWorkspaceSnapshot(ctx context.Context, ownerID, snapshotID, name string) (Workspace, error) {
	snapshot, source, err := s.WorkspaceSnapshotByID(ctx, ownerID, snapshotID)
	if err != nil {
		return Workspace{}, err
	}
	if snapshot.Status != "ready" {
		return Workspace{}, errors.New("snapshot is not ready to restore")
	}
	item := Workspace{ID: uuid.NewString(), OwnerID: ownerID, Name: name, Type: "snapshot", SizeGB: source.SizeGB, PVCName: "workspace-" + uuid.NewString()[:8], SourceSnapshotID: &snapshot.ID, Status: "ready"}
	err = s.pool.QueryRow(ctx, `INSERT INTO workspaces(id,owner_id,name,type,size_gb,pvc_name,source_snapshot_id,status) VALUES($1,$2,$3,'snapshot',$4,$5,$6,'ready') RETURNING created_at,updated_at`, item.ID, item.OwnerID, item.Name, item.SizeGB, item.PVCName, snapshot.ID).Scan(&item.CreatedAt, &item.UpdatedAt)
	return item, err
}

type RuntimeSession struct {
	ID          string          `json:"id"`
	RuntimeID   string          `json:"runtimeId"`
	AgentID     string          `json:"agentId"`
	AgentName   string          `json:"agentName"`
	RuntimeType string          `json:"runtimeType"`
	Title       string          `json:"title"`
	Status      string          `json:"status"`
	Trace       json.RawMessage `json:"trace"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func (s *Store) RuntimeSessions(ctx context.Context, ownerID string) ([]RuntimeSession, error) {
	rows, err := s.pool.Query(ctx, `SELECT rs.id,rs.runtime_id,a.id,a.name,a.runtime_type,rs.title,rs.status,rs.trace,rs.created_at,rs.updated_at FROM runtime_sessions rs JOIN agent_runtimes r ON r.id=rs.runtime_id JOIN agent_definitions a ON a.id=r.agent_id WHERE rs.owner_id=$1 ORDER BY rs.updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RuntimeSession{}
	for rows.Next() {
		var item RuntimeSession
		if err := rows.Scan(&item.ID, &item.RuntimeID, &item.AgentID, &item.AgentName, &item.RuntimeType, &item.Title, &item.Status, &item.Trace, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateRuntimeSession(ctx context.Context, ownerID, runtimeID, title string) (RuntimeSession, error) {
	var item RuntimeSession
	item.ID = uuid.NewString()
	err := s.pool.QueryRow(ctx, `INSERT INTO runtime_sessions(id,runtime_id,owner_id,title) SELECT $1,r.id,$2,$3 FROM agent_runtimes r WHERE r.id=$4 AND r.owner_id=$2 RETURNING id,runtime_id,title,status,trace,created_at,updated_at`, item.ID, ownerID, title, runtimeID).Scan(&item.ID, &item.RuntimeID, &item.Title, &item.Status, &item.Trace, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	err = s.pool.QueryRow(ctx, `SELECT a.id,a.name,a.runtime_type FROM agent_runtimes r JOIN agent_definitions a ON a.id=r.agent_id WHERE r.id=$1`, runtimeID).Scan(&item.AgentID, &item.AgentName, &item.RuntimeType)
	return item, err
}

func (s *Store) CloseRuntimeSession(ctx context.Context, ownerID, id string) error {
	result, err := s.pool.Exec(ctx, `UPDATE runtime_sessions SET status='closed',updated_at=now() WHERE id=$1 AND owner_id=$2`, id, ownerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Agents(ctx context.Context, ownerID string, admin bool) ([]Agent, error) {
	query := `SELECT a.id,a.owner_id,a.name,a.description,a.runtime_type,a.runtime_profile_id,a.runtime_image_id,a.security_profile_id,a.network_profile_id,a.mcp_bundle_id,a.model_endpoint_id,a.workspace_id,a.version,a.spec,a.created_at,a.updated_at,
 r.id,r.agent_id,r.owner_id,r.status,r.desired_state,r.crd_name,r.pod_name,r.node_name,r.endpoint,r.restart_count,r.failure_reason,r.last_activity_at,r.started_at,r.stopped_at,r.created_at,r.updated_at
 FROM agent_definitions a LEFT JOIN LATERAL (SELECT * FROM agent_runtimes ar WHERE ar.agent_id=a.id ORDER BY ar.created_at DESC LIMIT 1) r ON true`
	args := []any{}
	if !admin {
		query += ` WHERE a.owner_id=$1`
		args = append(args, ownerID)
	}
	query += ` ORDER BY a.updated_at DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Agent{}
	for rows.Next() {
		var a Agent
		var r Runtime
		var runtimeID, agentID, rOwnerID, status, desired, crd, pod, node, endpoint, failure *string
		var restart *int
		var rCreated, rUpdated *time.Time
		if err := rows.Scan(&a.ID, &a.OwnerID, &a.Name, &a.Description, &a.RuntimeType, &a.RuntimeProfileID, &a.RuntimeImageID, &a.SecurityProfileID, &a.NetworkProfileID, &a.MCPBundleID, &a.ModelEndpointID, &a.WorkspaceID, &a.Version, &a.Spec, &a.CreatedAt, &a.UpdatedAt,
			&runtimeID, &agentID, &rOwnerID, &status, &desired, &crd, &pod, &node, &endpoint, &restart, &failure, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &rCreated, &rUpdated); err != nil {
			return nil, err
		}
		if runtimeID != nil {
			r.ID = *runtimeID
			r.AgentID = *agentID
			r.OwnerID = *rOwnerID
			r.Status = *status
			r.DesiredState = *desired
			r.CRDName = *crd
			r.PodName = *pod
			r.NodeName = *node
			r.Endpoint = *endpoint
			r.RestartCount = *restart
			r.FailureReason = *failure
			r.CreatedAt = *rCreated
			r.UpdatedAt = *rUpdated
			a.Runtime = &r
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

type CreateAgentInput struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	RuntimeType       string `json:"runtimeType"`
	RuntimeProfileID  string `json:"runtimeProfileId"`
	WorkspaceID       string `json:"workspaceId"`
	TemplateID        string `json:"templateId"`
	SystemPrompt      string `json:"systemPrompt"`
	MCPBundleID       string `json:"mcpBundleId"`
	ModelEndpointID   string `json:"modelEndpointId"`
	SecurityProfileID string `json:"securityProfileId"`
	NetworkProfileID  string `json:"networkProfileId"`
}

func (s *Store) CreateAgent(ctx context.Context, ownerID string, input CreateAgentInput) (Agent, error) {
	if input.RuntimeType != "opencode" && input.RuntimeType != "hermes" && input.RuntimeType != "qwenpaw" && input.RuntimeType != "qwencode" && input.RuntimeType != "custom" {
		return Agent{}, errors.New("unsupported runtime type")
	}
	id := uuid.NewString()
	spec, _ := json.Marshal(map[string]any{"systemPrompt": input.SystemPrompt})
	if input.SecurityProfileID == "" {
		input.SecurityProfileID = "sp-restricted"
	}
	if input.NetworkProfileID == "" {
		input.NetworkProfileID = "np-restricted"
	}
	if input.TemplateID != "" {
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(security_profile_id,'sp-restricted'),COALESCE(network_profile_id,'np-restricted') FROM agent_templates WHERE id=$1 AND published`, input.TemplateID).Scan(&input.SecurityProfileID, &input.NetworkProfileID)
	}
	null := func(v string) any {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return v
	}
	var item Agent
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_definitions(id,owner_id,template_id,name,description,runtime_type,runtime_profile_id,workspace_id,mcp_bundle_id,model_endpoint_id,security_profile_id,network_profile_id,system_prompt,spec) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id,owner_id,name,description,runtime_type,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,version,spec,created_at,updated_at`, id, ownerID, null(input.TemplateID), input.Name, input.Description, input.RuntimeType, null(input.RuntimeProfileID), null(input.WorkspaceID), null(input.MCPBundleID), null(input.ModelEndpointID), input.SecurityProfileID, input.NetworkProfileID, input.SystemPrompt, spec).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.RuntimeType, &item.RuntimeProfileID, &item.RuntimeImageID, &item.SecurityProfileID, &item.NetworkProfileID, &item.MCPBundleID, &item.ModelEndpointID, &item.WorkspaceID, &item.Version, &item.Spec, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (s *Store) AgentByID(ctx context.Context, id, ownerID string, admin bool) (Agent, error) {
	query := `SELECT id,owner_id,name,description,runtime_type,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,version,spec,created_at,updated_at FROM agent_definitions WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	var a Agent
	err := s.pool.QueryRow(ctx, query, args...).Scan(&a.ID, &a.OwnerID, &a.Name, &a.Description, &a.RuntimeType, &a.RuntimeProfileID, &a.RuntimeImageID, &a.SecurityProfileID, &a.NetworkProfileID, &a.MCPBundleID, &a.ModelEndpointID, &a.WorkspaceID, &a.Version, &a.Spec, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return a, err
}

func (s *Store) RuntimeByID(ctx context.Context, id, ownerID string, admin bool) (Runtime, error) {
	query := `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,created_at,updated_at FROM agent_runtimes WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	var r Runtime
	err := s.pool.QueryRow(ctx, query, args...).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

func (s *Store) LatestRuntimeForAgent(ctx context.Context, agentID string) (Runtime, error) {
	var r Runtime
	err := s.pool.QueryRow(ctx, `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,created_at,updated_at FROM agent_runtimes WHERE agent_id=$1 AND desired_state<>'deleted' ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

func (s *Store) CreateRuntime(ctx context.Context, agent Agent, status string) (Runtime, error) {
	id := uuid.NewString()
	crd := "agent-" + strings.ToLower(strings.ReplaceAll(agent.OwnerID[:8]+"-"+agent.ID[:8], "_", "-"))
	var r Runtime
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_runtimes(id,agent_id,owner_id,status,desired_state,crd_name) VALUES($1,$2,$3,$4,'running',$5) RETURNING id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,created_at,updated_at`, id, agent.ID, agent.OwnerID, status, crd).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) UpdateRuntimeDesiredState(ctx context.Context, id, ownerID, state string, admin bool) (Runtime, error) {
	if state != "running" && state != "stopped" && state != "deleted" {
		return Runtime{}, fmt.Errorf("invalid desired state %q", state)
	}
	query := `UPDATE agent_runtimes SET desired_state=$1,status=CASE WHEN $1='running' THEN 'pending' WHEN $1='stopped' THEN 'stopping' ELSE 'deleting' END,updated_at=now() WHERE id=$2`
	args := []any{state, id}
	if !admin {
		query += ` AND owner_id=$3`
		args = append(args, ownerID)
	}
	query += ` RETURNING id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,created_at,updated_at`
	var r Runtime
	err := s.pool.QueryRow(ctx, query, args...).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

func (s *Store) UpdateRuntimeObserved(ctx context.Context, id, phase, podName, nodeName, endpoint string, restartCount int, failureReason string) error {
	status := strings.ToLower(phase)
	_, err := s.pool.Exec(ctx, `UPDATE agent_runtimes SET status=$1,pod_name=$2,node_name=$3,endpoint=$4,restart_count=$5,failure_reason=$6,
started_at=CASE WHEN $1 IN ('running','ready') THEN COALESCE(started_at,now()) ELSE started_at END,
stopped_at=CASE WHEN $1='stopped' THEN now() ELSE stopped_at END,updated_at=now() WHERE id=$7`, status, podName, nodeName, endpoint, restartCount, failureReason, id)
	return err
}

func (s *Store) TouchRuntime(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx, `UPDATE agent_runtimes SET last_activity_at=now(),updated_at=now() WHERE id=$1`, id)
}

func (s *Store) IdleRuntimeCandidates(ctx context.Context) ([]IdleRuntimeCandidate, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id,r.agent_id,r.owner_id
FROM agent_runtimes r
JOIN agent_definitions a ON a.id=r.agent_id
LEFT JOIN runtime_profiles p ON p.id=a.runtime_profile_id
WHERE r.desired_state='running'
  AND r.status IN ('running','ready','idle')
  AND COALESCE(p.idle_timeout_seconds,3600)>0
  AND COALESCE(r.last_activity_at,r.started_at,r.created_at) < now() - make_interval(secs => COALESCE(p.idle_timeout_seconds,3600))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []IdleRuntimeCandidate{}
	for rows.Next() {
		var item IdleRuntimeCandidate
		if err := rows.Scan(&item.RuntimeID, &item.AgentID, &item.OwnerID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context, userID, role string) (Dashboard, error) {
	filter := "WHERE owner_id=$1"
	args := []any{userID}
	if role == "admin" {
		filter = ""
		args = nil
	}
	var d Dashboard
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_definitions `+filter, args...).Scan(&d.Agents); err != nil {
		return d, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('ready','running')),count(*) FILTER (WHERE status='idle'),count(*) FILTER (WHERE status IN ('failed','crashed','spawn_failed','unhealthy')) FROM agent_runtimes `+filter, args...).Scan(&d.Running, &d.Idle, &d.Failed); err != nil {
		return d, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM workspaces `+filter, args...).Scan(&d.Workspaces); err != nil {
		return d, err
	}
	approvalFilter := "WHERE requester_id=$1 AND status='pending'"
	if role == "admin" {
		approvalFilter = "WHERE status='pending'"
		args = nil
	} else if role == "manager" {
		approvalFilter = "WHERE reviewer_id=$1 AND status='pending'"
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM approvals `+approvalFilter, args...).Scan(&d.PendingTasks); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Store) AuditEvents(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id,occurred_at,actor_name,action,resource_type,resource_id,outcome,ip_address,details FROM audit_events ORDER BY occurred_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var at time.Time
		var actor, action, rt, rid, outcome, ip string
		var details any
		if err := rows.Scan(&id, &at, &actor, &action, &rt, &rid, &outcome, &ip, &details); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "occurredAt": at, "actor": actor, "action": action, "resourceType": rt, "resourceId": rid, "outcome": outcome, "ipAddress": ip, "details": details})
	}
	return items, rows.Err()
}
