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

	"github.com/hkjang/AgentHub/internal/quota"
	"github.com/hkjang/AgentHub/internal/runtimetype"
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
	ID               string  `json:"id"`
	OwnerID          string  `json:"ownerId"`
	Name             string  `json:"name"`
	Type             string  `json:"type"`
	SizeGB           int     `json:"sizeGb"`
	RepositoryURL    string  `json:"repositoryUrl"`
	Branch           string  `json:"branch"`
	PVCName          string  `json:"pvcName"`
	SourceSnapshotID *string `json:"sourceSnapshotId,omitempty"`
	Status           string  `json:"status"`
	// GitCredentialSecretID names one of the owner's personal secrets. Kind picks
	// the authentication method and Username is only used for token auth.
	GitCredentialSecretID *string   `json:"gitCredentialSecretId,omitempty"`
	GitCredentialKind     string    `json:"gitCredentialKind"`
	GitCredentialUsername string    `json:"gitCredentialUsername"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
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
	// WarmUntil is set while the runtime warm pool holds this runtime. A person
	// taking the runtime over clears it, which is what keeps the pool from
	// stopping a workspace somebody is working in.
	WarmUntil *time.Time `json:"warmUntil,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
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
	MaxGPUsPerUser      int `json:"maxGpusPerUser"`
	// DefaultIdleTimeoutSeconds applies to a runtime whose agent has no profile.
	// Every profile carries its own, so this is the floor under the ones that do
	// not — and it was, until now, a field the console saved and nothing read.
	DefaultIdleTimeoutSeconds int `json:"defaultIdleTimeoutSeconds"`
}

// CheckRuntimeQuotaExcept is the same question asked about a runtime whose record
// already exists.
//
// The autonomous path creates the record before it can know the profile to check
// against, so by the time the limit is asked the row is already counted as held —
// and the check adds one for the runtime it is about to start, which is that same
// row. A person allowed one runtime was refused because of the runtime they were
// asking about, and the task waited forever behind itself.
func (s *Store) CheckRuntimeQuotaExcept(ctx context.Context, userID, profileID, exceptRuntimeID string) error {
	resolved, err := s.ResolveQuotaExcept(ctx, userID, exceptRuntimeID)
	if err != nil {
		return err
	}
	var addCPU, addMemory, addGPUs int
	if err := s.pool.QueryRow(ctx, `SELECT cpu_millis,memory_mb,gpu_count FROM runtime_profiles WHERE id=$1 AND enabled`, profileID).Scan(&addCPU, &addMemory, &addGPUs); err != nil {
		return err
	}
	if err := quota.CheckHeld(quota.ScopeUser, resolved.Effective, resolved.Held, addCPU, addMemory, addGPUs); err != nil {
		return err
	}
	if resolved.DepartmentID != "" {
		return quota.CheckHeld(quota.ScopeDepartment, resolved.DepartmentQ.Total, resolved.DepartmentHeld, addCPU, addMemory, addGPUs)
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

type starterTemplate struct {
	name, slug, description, category, runtime, profile, prompt string
}

var starterTemplates = []starterTemplate{
	{"OpenCode Developer", "opencode-developer", "Secure persistent coding workspace with Git and MCP tools.", "Development", runtimetype.OpenCode, "rp-developer", "You are a careful enterprise software engineer. Inspect, test, and explain every change."},
	{"Hermes Research", "hermes-research", "Long-running research agent with persistent memory.", "Research", runtimetype.Hermes, "rp-advanced", "Research the request using approved tools and cite the evidence used."},
	{"Qwen Paw Assistant", "qwen-paw", "Autonomous agentic AI assistant powered by Qwen Paw for complex workflows and reasoning.", "Automation", runtimetype.QwenPaw, "rp-basic", "You are an intelligent agentic assistant powered by Qwen Paw. Plan, orchestrate tools, and solve enterprise problems step-by-step."},
	{"IT Operator", "it-operator", "Policy-controlled operations assistant with approval gates.", "Operations", runtimetype.Hermes, "rp-basic", "Assist with IT operations. Request approval before any state-changing action."},
	{"Qwen Code Engineer", "qwen-code", "터미널 코딩 에이전트. 작업공간의 코드를 직접 고치고, 작업을 맡기면 무인으로도 같은 도구 루프를 사용합니다.", "Development", runtimetype.QwenCode, "rp-developer", "당신은 신중한 사내 소프트웨어 엔지니어입니다. 변경 전에 코드를 읽고, 테스트로 확인하고, 무엇을 왜 바꿨는지 남기세요."},
	{"Goose Agent", "goose-agent", "프로토콜로 대화하는 오픈소스 에이전트. 도구를 쓰기 전마다 플랫폼에 물어보므로, 무인 실행이 무엇을 바꿨는지 기록으로 남습니다.", "Development", runtimetype.Goose, "rp-developer", "당신은 신중한 사내 엔지니어입니다. 무엇을 하려는지 먼저 말하고, 바꾼 것과 그 이유를 남기세요."},
	{"HolmesGPT Investigator", "holmes-investigator", "장애를 조사하는 SRE 에이전트. 결론과 함께 그 근거로 조회한 내용을 실행 기록에 남깁니다.", "Operations", runtimetype.Holmes, "rp-advanced", "당신은 신중한 SRE입니다. 추측하지 말고 관측 데이터를 조회해 근거를 모으고, 근본 원인과 확인 방법을 함께 쓰세요."},
	{"BrowserCode Operator", "browsercode-operator", "진짜 브라우저를 직접 몰아 일하는 에이전트. 로그인이 필요한 사이트 조회나 웹 UI 확인처럼 사람이 브라우저로 하던 일을 맡깁니다.", "Automation", runtimetype.BrowserCode, "rp-advanced", "당신은 브라우저로 일합니다. 무엇을 열어 무엇을 확인했는지 남기고, 확인하지 못한 것을 확인한 것처럼 쓰지 마세요."},
	{"Jupyter Analyst", "jupyter-analyst", "노트북으로 데이터를 다루는 작업대. 같은 화면의 터미널에 Qwen Code 에이전트가 함께 있어, 지루한 부분은 맡길 수 있습니다.", "Analytics", runtimetype.Jupyter, "rp-advanced", "당신은 신중한 데이터 분석가입니다. 가정을 먼저 적고, 표와 그림으로 근거를 남기고, 결론과 한계를 함께 쓰세요."},
	{"Langflow Builder", "langflow-builder", "흐름을 그려서 만드는 시각적 빌더. 저장한 흐름을 자동 실행 백엔드로 그대로 사용할 수 있습니다.", "Automation", runtimetype.Langflow, "rp-basic", "당신은 흐름으로 업무를 자동화합니다. 입력과 출력을 명확히 하고, 실패했을 때 무엇이 잘못됐는지 남기세요."},
	{"Node-RED Wiring", "node-red-wiring", "노드를 선으로 이어 만드는 배선 도구. 이벤트를 받아 변환하고 다른 시스템을 호출하는 흐름을 계속 돌립니다.", "Automation", runtimetype.NodeRED, "rp-basic", "당신은 시스템과 시스템을 잇습니다. 입력과 출력의 형식을 분명히 하고, 오류 경로를 반드시 만드세요."},
	{"n8n Automation", "n8n-automation", "수백 가지 연동을 가진 업무 자동화. 메일·메신저·DB·HTTP를 트리거와 노드로 잇습니다.", "Automation", runtimetype.N8N, "rp-basic", "당신은 사내 업무를 연결해 자동화합니다. 실패했을 때 어디서 멈췄는지 알 수 있게 만드세요."},
	{"Open Code Review", "open-code-review", "코드 변경분을 파일·줄·심각도별로 검토하고 근거가 있는 finding을 남기는 전용 리뷰 엔진입니다.", "Development", runtimetype.OpenCodeReview, "rp-developer", "변경된 코드만 근거로 검토하고, 확실한 문제를 파일과 줄 번호와 함께 설명하세요. 코드를 직접 수정하지 마세요."},
	{"Orca Multi-Agent", "orca-multi-agent", "여러 코딩 에이전트를 격리된 git worktree에서 동시에 실행하고 결과를 비교하는 멀티 에이전트 패브릭입니다.", "Development", runtimetype.Orca, "rp-advanced", "작업을 독립적인 역할로 나누고 병렬로 검증하세요. 각 결과의 근거와 차이를 비교한 뒤 최종 결론을 남기세요."},
	{"Pi Coding Agent", "pi-coding-agent", "실행 중에도 방향 수정·후속 지시·중단이 가능한 대화형 코딩 에이전트입니다.", "Development", runtimetype.Pi, "rp-developer", "신중하게 코드를 읽고 작은 단위로 변경하세요. 진행 상황과 검증 결과를 계속 알려 주고, 새 지시가 오면 현재 계획을 조정하세요."},
	{"OpenHands Agent Server", "openhands-agent-server", "REST API 대화를 통해 코드를 수정하고 진행 사건과 사용량을 남기는 에이전트 서버입니다.", "Development", runtimetype.OpenHands, "rp-advanced", "작업을 단계별로 수행하고 각 결정과 실행 결과를 기록하세요. 완료 전에 변경사항을 테스트하고 확인하지 못한 내용은 분명히 밝히세요."},
}

// SeedTemplates publishes the starter templates, one slug at a time.
//
// It used to do nothing at all once the table had a single row, which meant a
// platform that gained a runtime never offered it where people actually start —
// the catalog. Adding a runtime and leaving it out of the catalog is the same as
// not adding it for anyone who does not already know it exists.
//
// Per-slug and conflict-free, so an upgrade gains the new templates while an
// administrator's edits to the existing ones survive, and a template somebody
// deliberately unpublished stays unpublished.
func (s *Store) SeedTemplates(ctx context.Context, adminID string) error {
	// One template the database refuses must not take the others with it. That is
	// how three runtimes went missing from the catalog at once: the first of them
	// was rejected and the loop returned, so the two behind it were never even
	// attempted. Every row is tried, and the failures are reported together.
	var failures []string
	for _, item := range starterTemplates {
		_, err := s.pool.Exec(ctx, `INSERT INTO agent_templates(id,name,slug,description,category,runtime_type,runtime_profile_id,security_profile_id,network_profile_id,system_prompt,published,created_by)
			VALUES($1,$2,$3,$4,$5,$6,$7,'sp-restricted','np-restricted',$8,true,$9)
			ON CONFLICT (slug) DO NOTHING`,
			uuid.NewString(), item.name, item.slug, item.description, item.category, item.runtime, item.profile, item.prompt, adminID)
		if err != nil {
			failures = append(failures, item.slug+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New("일부 템플릿을 게시하지 못했습니다 — " + strings.Join(failures, "; "))
	}
	return nil
}

func (s *Store) Workspaces(ctx context.Context, ownerID string, admin bool) ([]Workspace, error) {
	query := `SELECT id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,source_snapshot_id,status,git_credential_secret_id,git_credential_kind,git_credential_username,created_at,updated_at FROM workspaces`
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
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Type, &item.SizeGB, &item.RepositoryURL, &item.Branch, &item.PVCName, &item.SourceSnapshotID, &item.Status, &item.GitCredentialSecretID, &item.GitCredentialKind, &item.GitCredentialUsername, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CreateWorkspaceWithinQuota creates one while holding the owner's storage
// quota, so the check and the write are the same act.
//
// The API used to ask the quota and then create, which let two requests arriving
// together each see room for the last of it. The old pair is still here for
// callers that are not taking storage.
func (s *Store) CreateWorkspaceWithinQuota(ctx context.Context, ownerID string, item Workspace) (Workspace, error) {
	var created Workspace
	err := s.ClaimWorkspaceStorage(ctx, ownerID, item.SizeGB, func(tx pgx.Tx) error {
		var inner error
		created, inner = insertWorkspace(ctx, tx, ownerID, item)
		return inner
	})
	return created, err
}

func (s *Store) CreateWorkspace(ctx context.Context, ownerID string, item Workspace) (Workspace, error) {
	item.ID, item.OwnerID = uuid.NewString(), ownerID
	if item.Type == "" {
		item.Type = "empty"
	}
	if item.SizeGB == 0 {
		item.SizeGB = 10
	}
	return insertWorkspace(ctx, s.pool, ownerID, item)
}

// insertWorkspace writes the row, whether or not a quota lock is being held.
func insertWorkspace(ctx context.Context, db querier, ownerID string, item Workspace) (Workspace, error) {
	if item.ID == "" {
		item.ID, item.OwnerID = uuid.NewString(), ownerID
	}
	if item.Type == "" {
		item.Type = "empty"
	}
	if item.SizeGB == 0 {
		item.SizeGB = 10
	}
	item.PVCName = "workspace-" + item.ID[:8]
	if item.GitCredentialSecretID != nil && item.GitCredentialKind == "" {
		item.GitCredentialKind = "token"
	}
	err := db.QueryRow(ctx, `INSERT INTO workspaces(id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,status,git_credential_secret_id,git_credential_kind,git_credential_username) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ready',$9,$10,$11) RETURNING status,created_at,updated_at`, item.ID, item.OwnerID, item.Name, item.Type, item.SizeGB, item.RepositoryURL, item.Branch, item.PVCName, item.GitCredentialSecretID, item.GitCredentialKind, item.GitCredentialUsername).Scan(&item.Status, &item.CreatedAt, &item.UpdatedAt)
	err = conflictIfTaken(err, "같은 이름의 작업공간이 이미 있습니다. 다른 이름을 쓰세요")
	return item, err
}

func (s *Store) WorkspaceByID(ctx context.Context, id, ownerID string, admin bool) (Workspace, error) {
	query := `SELECT id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,source_snapshot_id,status,git_credential_secret_id,git_credential_kind,git_credential_username,created_at,updated_at FROM workspaces WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	var item Workspace
	err := s.pool.QueryRow(ctx, query, args...).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Type, &item.SizeGB, &item.RepositoryURL, &item.Branch, &item.PVCName, &item.SourceSnapshotID, &item.Status, &item.GitCredentialSecretID, &item.GitCredentialKind, &item.GitCredentialUsername, &item.CreatedAt, &item.UpdatedAt)
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
	// A restore takes storage like any other workspace, so it is claimed the same
	// way: the quota is held while the row is written rather than asked before it.
	err = s.ClaimWorkspaceStorage(ctx, ownerID, item.SizeGB, func(tx pgx.Tx) error {
		inner := tx.QueryRow(ctx, `INSERT INTO workspaces(id,owner_id,name,type,size_gb,pvc_name,source_snapshot_id,status) VALUES($1,$2,$3,'snapshot',$4,$5,$6,'ready') RETURNING created_at,updated_at`,
			item.ID, item.OwnerID, item.Name, item.SizeGB, item.PVCName, snapshot.ID).Scan(&item.CreatedAt, &item.UpdatedAt)
		return conflictIfTaken(inner, "같은 이름의 작업공간이 이미 있습니다. 다른 이름을 쓰세요")
	})
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
 r.id,r.agent_id,r.owner_id,r.status,r.desired_state,r.crd_name,r.pod_name,r.node_name,r.endpoint,r.restart_count,r.failure_reason,r.last_activity_at,r.started_at,r.stopped_at,r.warm_until,r.created_at,r.updated_at
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
			&runtimeID, &agentID, &rOwnerID, &status, &desired, &crd, &pod, &node, &endpoint, &restart, &failure, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.WarmUntil, &rCreated, &rUpdated); err != nil {
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
	// CustomCommand starts the agent process for runtime type 'custom'. Every
	// other runtime is started by its adapter, so this is ignored for them. It is
	// one argument per element, exactly like a Kubernetes container command, so
	// there is no shell quoting to get wrong.
	CustomCommand []string `json:"customCommand"`
	// CustomPort is the port a custom runtime serves on. Zero uses the default.
	CustomPort int `json:"customPort"`
	// RuntimeImageID pins the catalog entry this definition runs. Left empty on
	// create, the store pins whatever is currently approved so the definition
	// stays reproducible even after the catalog moves on.
	RuntimeImageID string `json:"runtimeImageId"`
}

// checkRuntimeImagePin refuses an image built for a different runtime.
//
// Every runtime used to boot from the same base image, so a mismatched pin was
// harmless and nothing checked it. That stopped being true when Langflow and
// Qwen Code arrived with images of their own: pinning one to an agent of the
// other starts a Pod whose command does not exist in it, and the symptom is a
// crash loop with nothing in the status explaining why. The answer is knowable
// at the moment somebody asks for it, so it is answered here.
func (s *Store) checkRuntimeImagePin(ctx context.Context, imageID, runtimeType string) error {
	image, err := s.RuntimeImageByID(ctx, imageID)
	if errors.Is(err, ErrNotFound) {
		return errors.New("선택한 Runtime 이미지를 찾을 수 없습니다")
	}
	if err != nil {
		return err
	}
	return runtimeImagePinMismatch(image, runtimeType)
}

// runtimeImagePinMismatch is the decision on its own, so it can be tested without
// a database.
func runtimeImagePinMismatch(image RuntimeImage, runtimeType string) error {
	if image.RuntimeType != runtimeType {
		return fmt.Errorf("이 이미지는 %s 런타임용이라 %s Agent에 지정할 수 없습니다", image.RuntimeType, runtimeType)
	}
	return nil
}

// nullText maps an empty optional identifier to SQL NULL so that the foreign
// keys on agent_definitions stay valid instead of pointing at an empty string.
func nullText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// maxCustomCommandParts bounds a custom start command. Anything longer is a
// pasted script, which belongs in the image rather than the definition.
const maxCustomCommandParts = 32

// normaliseCustomRuntime validates and cleans what a custom runtime needs to
// start. Rejecting it here rather than at spawn time is the difference between
// a clear error in the console and a Pod that quietly crash-loops.
func normaliseCustomRuntime(input *CreateAgentInput) error {
	command := make([]string, 0, len(input.CustomCommand))
	for _, part := range input.CustomCommand {
		if part = strings.TrimSpace(part); part != "" {
			command = append(command, part)
		}
	}
	if input.RuntimeType != runtimetype.Custom {
		input.CustomCommand, input.CustomPort = nil, 0
		return nil
	}
	if len(command) == 0 {
		return errors.New("custom 런타임은 시작 명령이 필요합니다")
	}
	if len(command) > maxCustomCommandParts {
		return fmt.Errorf("시작 명령은 최대 %d개까지 지정할 수 있습니다", maxCustomCommandParts)
	}
	if input.CustomPort < 0 || input.CustomPort > 65535 {
		return errors.New("포트는 1~65535 범위여야 합니다")
	}
	input.CustomCommand = command
	return nil
}

// agentSpecJSON renders the adapter-shaped extras that live on the definition
// rather than in a column of their own.
func agentSpecJSON(input CreateAgentInput) []byte {
	value := map[string]any{"systemPrompt": input.SystemPrompt}
	if len(input.CustomCommand) > 0 {
		value["customCommand"] = input.CustomCommand
	}
	if input.CustomPort > 0 {
		value["customPort"] = input.CustomPort
	}
	spec, _ := json.Marshal(value)
	return spec
}

// CustomRuntime reads back what a custom runtime needs to start. Anything that
// is not a custom runtime is started by its adapter and reports nothing here.
func (a Agent) CustomRuntime() ([]string, int) {
	if a.RuntimeType != runtimetype.Custom || len(a.Spec) == 0 {
		return nil, 0
	}
	var decoded struct {
		CustomCommand []string `json:"customCommand"`
		CustomPort    int      `json:"customPort"`
	}
	if err := json.Unmarshal(a.Spec, &decoded); err != nil {
		return nil, 0
	}
	return decoded.CustomCommand, decoded.CustomPort
}

func (s *Store) CreateAgent(ctx context.Context, ownerID string, input CreateAgentInput) (Agent, error) {
	if !runtimetype.IsSupported(input.RuntimeType) {
		return Agent{}, errors.New("unsupported runtime type")
	}
	enabled, err := s.RuntimeTypeEnabled(ctx, input.RuntimeType)
	if err != nil {
		return Agent{}, err
	}
	if !enabled {
		return Agent{}, RuntimeTypeDisabled{RuntimeType: input.RuntimeType}
	}
	if err := normaliseCustomRuntime(&input); err != nil {
		return Agent{}, err
	}
	id := uuid.NewString()
	spec := agentSpecJSON(input)
	if input.SecurityProfileID == "" {
		input.SecurityProfileID = "sp-restricted"
	}
	if input.NetworkProfileID == "" {
		input.NetworkProfileID = "np-restricted"
	}
	if input.TemplateID != "" {
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(security_profile_id,'sp-restricted'),COALESCE(network_profile_id,'np-restricted') FROM agent_templates WHERE id=$1 AND published`, input.TemplateID).Scan(&input.SecurityProfileID, &input.NetworkProfileID)
	}
	if strings.TrimSpace(input.ModelEndpointID) == "" {
		var defaultModelID string
		_ = s.pool.QueryRow(ctx, `SELECT id FROM model_endpoints WHERE enabled ORDER BY created_at ASC LIMIT 1`).Scan(&defaultModelID)
		if defaultModelID != "" {
			input.ModelEndpointID = defaultModelID
		}
	}
	if strings.TrimSpace(input.RuntimeImageID) == "" {
		if approved, imageErr := s.ApprovedRuntimeImage(ctx, input.RuntimeType); imageErr == nil {
			input.RuntimeImageID = approved.ID
		}
	} else if err := s.checkRuntimeImagePin(ctx, input.RuntimeImageID, input.RuntimeType); err != nil {
		return Agent{}, err
	}
	var item Agent
	err = s.pool.QueryRow(ctx, `INSERT INTO agent_definitions(id,owner_id,template_id,name,description,runtime_type,runtime_profile_id,workspace_id,mcp_bundle_id,model_endpoint_id,security_profile_id,network_profile_id,system_prompt,spec,runtime_image_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id,owner_id,name,description,runtime_type,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,version,spec,created_at,updated_at`, id, ownerID, nullText(input.TemplateID), input.Name, input.Description, input.RuntimeType, nullText(input.RuntimeProfileID), nullText(input.WorkspaceID), nullText(input.MCPBundleID), nullText(input.ModelEndpointID), input.SecurityProfileID, input.NetworkProfileID, input.SystemPrompt, spec, nullText(input.RuntimeImageID)).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.RuntimeType, &item.RuntimeProfileID, &item.RuntimeImageID, &item.SecurityProfileID, &item.NetworkProfileID, &item.MCPBundleID, &item.ModelEndpointID, &item.WorkspaceID, &item.Version, &item.Spec, &item.CreatedAt, &item.UpdatedAt)
	return item, conflictIfTaken(err, "같은 이름의 에이전트가 이미 있습니다. 다른 이름을 쓰세요")
}

// UpdateAgent rewrites an agent definition and bumps its version. The runtime
// type is immutable: the running Pod, its Service ports and its persisted home
// directory are all shaped by the adapter, so switching it would orphan them.
// Callers restart the Runtime to roll the new definition out.
func (s *Store) UpdateAgent(ctx context.Context, id, ownerID string, admin bool, input CreateAgentInput) (Agent, error) {
	current, err := s.AgentByID(ctx, id, ownerID, admin)
	if err != nil {
		return Agent{}, err
	}
	if input.RuntimeType != "" && input.RuntimeType != current.RuntimeType {
		return Agent{}, errors.New("runtime type cannot be changed after the agent is created")
	}
	if input.SecurityProfileID == "" {
		input.SecurityProfileID = "sp-restricted"
	}
	if input.NetworkProfileID == "" {
		input.NetworkProfileID = "np-restricted"
	}
	// The runtime type is immutable, so an update that omits it still has to be
	// validated against the type the definition already has.
	input.RuntimeType = current.RuntimeType
	if err := normaliseCustomRuntime(&input); err != nil {
		return Agent{}, err
	}
	if strings.TrimSpace(input.RuntimeImageID) != "" {
		if err := s.checkRuntimeImagePin(ctx, input.RuntimeImageID, current.RuntimeType); err != nil {
			return Agent{}, err
		}
	}
	spec := agentSpecJSON(input)
	var item Agent
	// An empty RuntimeImageID keeps the current pin rather than unpinning: callers
	// that only rename an agent must not silently move it onto a newer image.
	err = s.pool.QueryRow(ctx, `UPDATE agent_definitions SET name=$2,description=$3,runtime_profile_id=$4,workspace_id=$5,mcp_bundle_id=$6,model_endpoint_id=$7,security_profile_id=$8,network_profile_id=$9,system_prompt=$10,spec=$11,runtime_image_id=COALESCE($12,runtime_image_id),version=version+1,updated_at=now() WHERE id=$1 RETURNING id,owner_id,name,description,runtime_type,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,version,spec,created_at,updated_at`,
		id, input.Name, input.Description, nullText(input.RuntimeProfileID), nullText(input.WorkspaceID), nullText(input.MCPBundleID), nullText(input.ModelEndpointID), input.SecurityProfileID, input.NetworkProfileID, input.SystemPrompt, spec, nullText(input.RuntimeImageID)).
		Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.RuntimeType, &item.RuntimeProfileID, &item.RuntimeImageID, &item.SecurityProfileID, &item.NetworkProfileID, &item.MCPBundleID, &item.ModelEndpointID, &item.WorkspaceID, &item.Version, &item.Spec, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return item, err
}

// DeleteAgent removes the definition. agent_runtimes and runtime_sessions cascade
// from the definition, so the caller must delete the Kubernetes resources first —
// otherwise the CRD name is lost and the Pod is orphaned in the cluster.
// AgentWorkInFlight counts the work that would be destroyed with this agent, and
// says what kind it is.
//
// Deleting an agent cascades through twelve tables: its tasks, runs, transcripts,
// artifacts, memories, evaluations, versions and triggers all go with it. Most of
// that is history, and deleting history is what somebody deleting an agent is
// asking for. Work in flight is not history. A task running right now, one parked
// at an approval somebody is about to give, one handed to a person who is
// finishing it in the runtime — those disappear mid-sentence, and nobody is told.
func (s *Store) AgentWorkInFlight(ctx context.Context, agentID string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM agent_tasks
		WHERE agent_id=$1 AND status IN ('running','waiting_tool','waiting_approval','handoff')
		GROUP BY status`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) DeleteAgent(ctx context.Context, id, ownerID string, admin bool) error {
	query := `DELETE FROM agent_definitions WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
	query := `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at FROM agent_runtimes WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	var r Runtime
	err := s.pool.QueryRow(ctx, query, args...).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.WarmUntil, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

func (s *Store) LatestRuntimeForAgent(ctx context.Context, agentID string) (Runtime, error) {
	var r Runtime
	err := s.pool.QueryRow(ctx, `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at FROM agent_runtimes WHERE agent_id=$1 AND desired_state<>'deleted' ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.WarmUntil, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

// CreateRuntimeWithinQuota creates one while holding the owner's runtime quota,
// so the check and the write are the same act.
func (s *Store) CreateRuntimeWithinQuota(ctx context.Context, agent Agent, status, profileID string) (Runtime, error) {
	var created Runtime
	err := s.ClaimRuntimeCapacity(ctx, agent.OwnerID, profileID, "", func(tx pgx.Tx) error {
		var inner error
		created, inner = insertRuntime(ctx, tx, agent, status)
		return inner
	})
	return created, err
}

// StartRuntimeWithinQuota flips an existing runtime to running under the same
// lock. Starting a stopped runtime takes capacity exactly as creating one does,
// and it was the path with no claim at all.
func (s *Store) StartRuntimeWithinQuota(ctx context.Context, id, ownerID, profileID string, admin bool) (Runtime, error) {
	var started Runtime
	err := s.ClaimRuntimeCapacity(ctx, ownerID, profileID, id, func(tx pgx.Tx) error {
		var inner error
		started, inner = updateDesiredState(ctx, tx, id, ownerID, "running", admin)
		return inner
	})
	return started, err
}

func (s *Store) CreateRuntime(ctx context.Context, agent Agent, status string) (Runtime, error) {
	return insertRuntime(ctx, s.pool, agent, status)
}

func insertRuntime(ctx context.Context, db querier, agent Agent, status string) (Runtime, error) {
	id := uuid.NewString()
	crd := "agent-" + strings.ToLower(strings.ReplaceAll(agent.OwnerID[:8]+"-"+agent.ID[:8], "_", "-"))
	var r Runtime
	err := db.QueryRow(ctx, `INSERT INTO agent_runtimes(id,agent_id,owner_id,status,desired_state,crd_name) VALUES($1,$2,$3,$4,'running',$5) RETURNING id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at`, id, agent.ID, agent.OwnerID, status, crd).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.WarmUntil, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) UpdateRuntimeDesiredState(ctx context.Context, id, ownerID, state string, admin bool) (Runtime, error) {
	return updateDesiredState(ctx, s.pool, id, ownerID, state, admin)
}

// updateDesiredState writes the state, whether or not a quota lock is being held.
func updateDesiredState(ctx context.Context, db querier, id, ownerID, state string, admin bool) (Runtime, error) {
	if state != "running" && state != "stopped" && state != "deleted" {
		return Runtime{}, fmt.Errorf("invalid desired state %q", state)
	}
	// Deleting takes the gateway token with it. The Pod and the Secret that held
	// it are going; leaving the hash behind leaves a working credential for
	// something that no longer exists.
	query := `UPDATE agent_runtimes SET desired_state=$1,status=CASE WHEN $1='running' THEN 'pending' WHEN $1='stopped' THEN 'stopping' ELSE 'deleting' END,
		gateway_token_hash=CASE WHEN $1='deleted' THEN NULL ELSE gateway_token_hash END,updated_at=now() WHERE id=$2`
	args := []any{state, id}
	if !admin {
		query += ` AND owner_id=$3`
		args = append(args, ownerID)
	}
	query += ` RETURNING id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at`
	var r Runtime
	err := db.QueryRow(ctx, query, args...).Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName, &r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt, &r.StoppedAt, &r.WarmUntil, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return r, err
}

// runtimeFailureStatuses are the observed states an operator would want an agent
// to react to.
var runtimeFailureStatuses = map[string]bool{"failed": true, "crashed": true, "spawn_failed": true, "unhealthy": true}

func (s *Store) UpdateRuntimeObserved(ctx context.Context, id, phase, podName, nodeName, endpoint string, restartCount int, failureReason string) error {
	status := strings.ToLower(phase)
	// An observation that saw nothing new is not a change, and writing one moves
	// updated_at — which is how long this runtime has been in the state it is in.
	// Every screen that lists runtimes observes them, so a Pod stuck starting was
	// re-stamped every few seconds by somebody watching it, and the row that
	// reports a runtime half-started for ten minutes could not fire while anybody
	// was looking.
	//
	// Measured against the platform's own query: a runtime forty minutes into
	// starting reported as nothing wrong when observed a moment earlier, and as
	// stuck when not.
	var same bool
	if err := s.pool.QueryRow(ctx, `SELECT status=$2 AND pod_name=$3 AND node_name=$4 AND endpoint=$5
		AND restart_count=$6 AND failure_reason=$7 FROM agent_runtimes WHERE id=$1`,
		id, status, podName, nodeName, endpoint, restartCount, failureReason).Scan(&same); err == nil && same {
		return nil
	}
	var previous, ownerID, agentID string
	err := s.pool.QueryRow(ctx, `UPDATE agent_runtimes r SET status=$1,pod_name=$2,node_name=$3,endpoint=$4,restart_count=$5,failure_reason=$6,
started_at=CASE WHEN $1 IN ('running','ready') THEN COALESCE(started_at,now()) ELSE started_at END,
stopped_at=CASE WHEN $1='stopped' THEN now() ELSE stopped_at END,updated_at=now()
FROM (SELECT id, status FROM agent_runtimes WHERE id=$7) old
WHERE r.id=old.id RETURNING old.status, r.owner_id, r.agent_id`, status, podName, nodeName, endpoint, restartCount, failureReason, id).
		Scan(&previous, &ownerID, &agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Only the transition is an event. The operator re-observes continuously, so
	// publishing on every pass would wake a subscribed agent every few seconds.
	if runtimeFailureStatuses[status] && previous != status {
		payload, _ := json.Marshal(map[string]any{
			"agentId": agentID, "status": status, "reason": failureReason, "restartCount": restartCount, "podName": podName,
		})
		if publishErr := s.PublishEvent(ctx, PlatformEvent{
			Type: EventRuntimeFailed, OwnerID: ownerID, SubjectType: "runtime", SubjectID: id, Payload: payload,
		}); publishErr != nil {
			return publishErr
		}
	}
	return nil
}

// ForgetMissingRuntime records that a runtime's object is no longer in the
// cluster.
//
// Observing a vanished runtime as stopped is not enough on its own: the row goes
// on saying somebody wants it running, so every screen that asks "what was asked
// for and never arrived" keeps answering with a runtime that does not exist and
// that nothing will ever start. Deleting the object by hand is the usual answer
// to a runtime that will not start, so this is a state the platform meets often.
func (s *Store) ForgetMissingRuntime(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runtimes
		SET status='stopped', desired_state='stopped', pod_name='', endpoint='',
		    stopped_at=COALESCE(stopped_at, now()), updated_at=now()
		WHERE id=$1`, id)
	return err
}

// TouchRuntimeSessions marks a runtime's open sessions as still in use.
//
// Whether somebody is at a keyboard is read from runtime_sessions.updated_at —
// "an open one on its own means little; one touched in the last few minutes is a
// person at a keyboard" — and that row was stamped when the session opened and
// never again. The evidence expired fifteen minutes into every session, so after
// that the platform could not tell somebody working from somebody who had walked
// away: the idle sweeper culls a runtime being used, and the confirmation before
// stopping one never appears.
//
// Only person traffic calls this. A task running in the runtime refreshes the
// runtime's own activity, and must not make an abandoned session look attended.
func (s *Store) TouchRuntimeSessions(ctx context.Context, runtimeID string) {
	_, _ = s.pool.Exec(ctx, `UPDATE runtime_sessions SET updated_at=now() WHERE runtime_id=$1 AND status='active'`, runtimeID)
}

func (s *Store) TouchRuntime(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx, `UPDATE agent_runtimes SET last_activity_at=now(),updated_at=now() WHERE id=$1`, id)
}

// RuntimeBusy reports why a runtime must not be stopped right now, or "".
//
// Two things can be going on inside a runtime that the process about to stop it
// knows nothing about. Another of the agent's tasks may be running in it — an
// agent allowed more than one concurrent run reuses the runtime the first task
// started, and the first task to finish was stopping the Pod under the others.
// And a person may be working in it: whoever started it, a terminal or chat
// session opened afterwards is somebody's window closing without warning.
//
// exceptTask is the task doing the asking, which is finishing and does not count
// as a reason to keep its own runtime.
func (s *Store) RuntimeBusy(ctx context.Context, runtimeID, agentID, exceptTask string) (string, error) {
	var running int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_tasks
		WHERE agent_id=$1 AND id<>$2 AND status IN ('running','waiting_tool','handoff')`, agentID, exceptTask).Scan(&running); err != nil {
		return "", err
	}
	if running > 0 {
		return "다른 작업이 이 Runtime에서 실행 중입니다", nil
	}
	// A session row is only closed when somebody closes it, so an open one on its
	// own means little; one touched in the last few minutes is a person at a
	// keyboard.
	var sessions int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM runtime_sessions
		WHERE runtime_id=$1 AND status='active' AND updated_at > now() - interval '15 minutes'`, runtimeID).Scan(&sessions); err != nil {
		return "", err
	}
	if sessions > 0 {
		return "사람이 이 Runtime에서 작업 중입니다", nil
	}
	return "", nil
}

func (s *Store) IdleRuntimeCandidates(ctx context.Context) ([]IdleRuntimeCandidate, error) {
	// The fallback for a runtime whose agent has no profile. It came from a
	// constant here while the console offered a field for exactly this and saved
	// it into a setting nobody read.
	fallback := 3600
	var governance governanceSettings
	if err := s.Setting(ctx, "governance", &governance); err == nil && governance.DefaultIdleTimeoutSeconds > 0 {
		fallback = governance.DefaultIdleTimeoutSeconds
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,r.agent_id,r.owner_id
FROM agent_runtimes r
JOIN agent_definitions a ON a.id=r.agent_id
LEFT JOIN runtime_profiles p ON p.id=a.runtime_profile_id
WHERE r.desired_state='running'
  AND r.status IN ('running','ready','idle')
  AND COALESCE(p.idle_timeout_seconds,$1)>0
  AND COALESCE(r.last_activity_at,r.started_at,r.created_at) < now() - make_interval(secs => COALESCE(p.idle_timeout_seconds,$1))
  -- A runtime with work in it is not idle, whatever the timestamp says. The
  -- execution plane keeps last_activity_at fresh now, but a run that hangs
  -- between steps would still age past the timeout, and stopping the Pod under a
  -- running task turns a stall into a crash with no explanation in it. A handed
  -- over task is here too: it is waiting for a person to open that runtime, which
  -- is the one thing culling it guarantees they cannot do.
  AND NOT EXISTS (
    SELECT 1 FROM agent_tasks t
    WHERE t.agent_id = r.agent_id
      AND t.status IN ('running','waiting_tool','handoff')
  )`, fallback)
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

// ProvisionedRuntimes lists the runtimes whose objects carry a copy of the
// platform-wide runtime environment, so a change to it can be pushed to them.
//
// Stopped runtimes are skipped: their object is written from the current
// settings the next time they start, and syncing them would be work with no
// effect. Everything else — running, starting, failed, pending — has an object
// in the cluster holding a copy that is now out of date.
func (s *Store) ProvisionedRuntimes(ctx context.Context) ([]Runtime, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at
		FROM agent_runtimes
		WHERE crd_name <> '' AND NOT (status = 'stopped' AND desired_state = 'stopped')
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Runtime{}
	for rows.Next() {
		var r Runtime
		if err := rows.Scan(&r.ID, &r.AgentID, &r.OwnerID, &r.Status, &r.DesiredState, &r.CRDName, &r.PodName,
			&r.NodeName, &r.Endpoint, &r.RestartCount, &r.FailureReason, &r.LastActivityAt, &r.StartedAt,
			&r.StoppedAt, &r.WarmUntil, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// StuckRuntime is a runtime somebody asked for that never arrived, or that
// keeps dying after it does.
type StuckRuntime struct {
	ID            string
	AgentID       string
	AgentName     string
	CRDName       string
	Status        string
	FailureReason string
	Restarts      int
	Since         time.Time
}

// RuntimesStuckStarting lists runtimes that were asked to run and have not
// become ready for longer than the given window.
//
// A Pod that cannot pull its image retries for ever, which is right — a registry
// comes back — but it means a runtime can sit half-started for an hour with the
// reason written on its own row and nobody looking at that row. Every other
// dependency this deployment has is asked about in one place; this is the one
// that was missing from it.
//
// Runtimes nobody asked to run are not stuck, and neither are the ones that are
// ready: the question is only about work somebody is waiting for.
func (s *Store) RuntimesStuckStarting(ctx context.Context, window time.Duration, limit int) ([]StuckRuntime, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.agent_id, a.name, r.crd_name, r.status, r.failure_reason, r.restart_count,
		       GREATEST(r.updated_at, r.created_at)
		FROM agent_runtimes r
		JOIN agent_definitions a ON a.id = r.agent_id
		WHERE r.desired_state = 'running'
		  AND r.status NOT IN ('running', 'ready')
		  AND GREATEST(r.updated_at, r.created_at) < now() - $1::interval
		ORDER BY GREATEST(r.updated_at, r.created_at)
		LIMIT $2`, window.String(), clampLimit(limit, 10, 50))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StuckRuntime{}
	for rows.Next() {
		var item StuckRuntime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.AgentName, &item.CRDName, &item.Status, &item.FailureReason, &item.Restarts, &item.Since); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RuntimesRestarting lists runtimes whose container keeps dying.
//
// A crash loop hides behind a healthy word: the runtime's status is running,
// because Kubernetes keeps starting it again, and the only sign is a number on
// the agent's page that nobody is asked to judge. Forty restarts and one restart
// look the same there.
func (s *Store) RuntimesRestarting(ctx context.Context, atLeast, limit int) ([]StuckRuntime, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.agent_id, a.name, r.status, r.failure_reason, r.restart_count,
		       GREATEST(r.updated_at, r.created_at)
		FROM agent_runtimes r
		JOIN agent_definitions a ON a.id = r.agent_id
		WHERE r.desired_state = 'running' AND r.restart_count >= $1
		ORDER BY r.restart_count DESC
		LIMIT $2`, atLeast, clampLimit(limit, 10, 50))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StuckRuntime{}
	for rows.Next() {
		var item StuckRuntime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.AgentName, &item.Status, &item.FailureReason,
			&item.Restarts, &item.Since); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
