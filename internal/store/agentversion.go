package store

import (
	"strconv"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hkjang/AgentHub/internal/korean"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent versions and promotion.
//
// The version counter went up on every save and nothing kept the definition it
// counted, so there was no way to see what changed, no way to return to the
// version that worked, and nothing between an edit made at 18:00 and the
// scheduled run that executed it at 02:00.
//
// A saved version is now a row. An agent may additionally require that the
// version the execution plane runs has been promoted, and a promotion requires an
// evaluation that passed against that exact version — or an administrator saying,
// in writing, that it may go anyway.

// AgentVersion is one saved definition.
type AgentVersion struct {
	AgentID           string          `json:"agentId"`
	Version           int             `json:"version"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	SystemPrompt      string          `json:"systemPrompt"`
	RuntimeProfileID  *string         `json:"runtimeProfileId,omitempty"`
	RuntimeImageID    *string         `json:"runtimeImageId,omitempty"`
	SecurityProfileID *string         `json:"securityProfileId,omitempty"`
	NetworkProfileID  *string         `json:"networkProfileId,omitempty"`
	ModelEndpointID   *string         `json:"modelEndpointId,omitempty"`
	MCPBundleID       *string         `json:"mcpBundleId,omitempty"`
	WorkspaceID       *string         `json:"workspaceId,omitempty"`
	Spec              json.RawMessage `json:"spec"`
	Note              string          `json:"note"`
	CreatedBy         *string         `json:"createdBy,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	// Promoted marks the version production runs on.
	Promoted bool `json:"promoted"`
	// Evaluation is the best evaluation recorded against this exact version, so
	// the list answers "may this be promoted?" without a second lookup.
	EvaluationStatus string `json:"evaluationStatus,omitempty"`
	EvaluationScore  int    `json:"evaluationScore,omitempty"`
}

// AgentRelease is an agent's promotion state.
type AgentRelease struct {
	PromotedVersion  *int       `json:"promotedVersion,omitempty"`
	PromotedAt       *time.Time `json:"promotedAt,omitempty"`
	PromotedBy       *string    `json:"promotedBy,omitempty"`
	PromotionNote    string     `json:"promotionNote,omitempty"`
	RequirePromotion bool       `json:"requirePromotion"`
	// Current is the version a save last produced, which is what runs unless the
	// gate says otherwise.
	Current int `json:"currentVersion"`
}

// RecordAgentVersion snapshots the definition as it now stands.
//
// Snapshotting is best effort by design: the definition is already saved, and
// failing the save because history could not be written would trade a working
// edit for a record of it.
func (s *Store) RecordAgentVersion(ctx context.Context, agent Agent, note, actorID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_versions
		(agent_id,version,name,description,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,system_prompt,spec,note,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (agent_id,version) DO NOTHING`,
		agent.ID, agent.Version, agent.Name, agent.Description, agent.RuntimeProfileID, agent.RuntimeImageID,
		agent.SecurityProfileID, agent.NetworkProfileID, agent.MCPBundleID, agent.ModelEndpointID, agent.WorkspaceID,
		agentSystemPromptFromSpec(agent.Spec), agent.Spec, note, nullText(actorID))
	return err
}

// agentSystemPromptFromSpec pulls the instruction out of the stored spec, so the
// version row carries it as a column rather than only inside the JSON.
func agentSystemPromptFromSpec(spec json.RawMessage) string {
	var decoded struct {
		SystemPrompt string `json:"systemPrompt"`
	}
	if len(spec) > 0 {
		_ = json.Unmarshal(spec, &decoded)
	}
	return decoded.SystemPrompt
}

// AgentVersions lists the saved definitions, newest first, with the evaluation
// each version earned.
func (s *Store) AgentVersions(ctx context.Context, agentID string, limit int) ([]AgentVersion, error) {
	limit = clampLimit(limit, 50, 200)
	rows, err := s.pool.Query(ctx, `SELECT v.agent_id, v.version, v.name, v.description, v.runtime_profile_id,
			v.runtime_image_id, v.model_endpoint_id, v.mcp_bundle_id, v.workspace_id, v.system_prompt, v.spec,
			v.note, v.created_by, v.created_at,
			(d.promoted_version = v.version) AS promoted,
			COALESCE((SELECT e.status FROM agent_evaluations e
				WHERE e.agent_id = v.agent_id AND e.agent_version = v.version
				ORDER BY e.score DESC, e.created_at DESC LIMIT 1), ''),
			COALESCE((SELECT e.score FROM agent_evaluations e
				WHERE e.agent_id = v.agent_id AND e.agent_version = v.version
				ORDER BY e.score DESC, e.created_at DESC LIMIT 1), 0)
		FROM agent_versions v JOIN agent_definitions d ON d.id = v.agent_id
		WHERE v.agent_id = $1 ORDER BY v.version DESC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentVersion{}
	for rows.Next() {
		var item AgentVersion
		var promoted *bool
		if err := rows.Scan(&item.AgentID, &item.Version, &item.Name, &item.Description, &item.RuntimeProfileID,
			&item.RuntimeImageID, &item.ModelEndpointID, &item.MCPBundleID, &item.WorkspaceID, &item.SystemPrompt,
			&item.Spec, &item.Note, &item.CreatedBy, &item.CreatedAt, &promoted,
			&item.EvaluationStatus, &item.EvaluationScore); err != nil {
			return nil, err
		}
		item.Promoted = promoted != nil && *promoted
		items = append(items, item)
	}
	return items, rows.Err()
}

// AgentVersionByNumber reads one saved definition.
func (s *Store) AgentVersionByNumber(ctx context.Context, agentID string, version int) (AgentVersion, error) {
	var item AgentVersion
	err := s.pool.QueryRow(ctx, `SELECT agent_id,version,name,description,runtime_profile_id,runtime_image_id,
			security_profile_id,network_profile_id,model_endpoint_id,mcp_bundle_id,workspace_id,system_prompt,spec,note,created_by,created_at
		FROM agent_versions WHERE agent_id=$1 AND version=$2`, agentID, version).
		Scan(&item.AgentID, &item.Version, &item.Name, &item.Description, &item.RuntimeProfileID, &item.RuntimeImageID,
			&item.SecurityProfileID, &item.NetworkProfileID, &item.ModelEndpointID, &item.MCPBundleID, &item.WorkspaceID, &item.SystemPrompt, &item.Spec,
			&item.Note, &item.CreatedBy, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentVersion{}, ErrNotFound
	}
	return item, err
}

// AgentReleaseState reads where an agent stands: what runs, what is approved, and
// whether approval is required at all.
func (s *Store) AgentReleaseState(ctx context.Context, agentID string) (AgentRelease, error) {
	var state AgentRelease
	err := s.pool.QueryRow(ctx, `SELECT version, promoted_version, promoted_at, promoted_by, promotion_note, require_promotion
		FROM agent_definitions WHERE id=$1`, agentID).
		Scan(&state.Current, &state.PromotedVersion, &state.PromotedAt, &state.PromotedBy, &state.PromotionNote, &state.RequirePromotion)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRelease{}, ErrNotFound
	}
	return state, err
}

// EvaluationPassed reports whether one version has an evaluation that passed.
func (s *Store) EvaluationPassed(ctx context.Context, agentID string, version int) (bool, int, error) {
	var score int
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(score), -1) FROM agent_evaluations
		WHERE agent_id=$1 AND agent_version=$2 AND status='passed'`, agentID, version).Scan(&score)
	if err != nil {
		return false, 0, err
	}
	return score >= 0, score, nil
}

// PromoteAgentVersion approves one version for production.
//
// force is the administrator override, and it is deliberately not silent: it is
// recorded in the note that the console and the audit log both show.
func (s *Store) PromoteAgentVersion(ctx context.Context, agentID string, version int, actorID, note string, force bool) (AgentRelease, error) {
	if _, err := s.AgentVersionByNumber(ctx, agentID, version); err != nil {
		return AgentRelease{}, err
	}
	if !force {
		passed, _, err := s.EvaluationPassed(ctx, agentID, version)
		if err != nil {
			return AgentRelease{}, err
		}
		if !passed {
			return AgentRelease{}, Conflict{Message: fmt.Sprintf("v%d에 통과한 사전검증 결과가 없어 승격할 수 없습니다", version)}
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_definitions
		SET promoted_version=$2, promoted_at=now(), promoted_by=$3, promotion_note=$4, updated_at=now()
		WHERE id=$1`, agentID, version, nullText(actorID), note)
	if err != nil {
		return AgentRelease{}, err
	}
	return s.AgentReleaseState(ctx, agentID)
}

// SetAgentPromotionGate turns the requirement on or off.
func (s *Store) SetAgentPromotionGate(ctx context.Context, agentID string, required bool) (AgentRelease, error) {
	if _, err := s.pool.Exec(ctx, `UPDATE agent_definitions SET require_promotion=$2, updated_at=now() WHERE id=$1`, agentID, required); err != nil {
		return AgentRelease{}, err
	}
	return s.AgentReleaseState(ctx, agentID)
}

// RestoreAgentVersion writes an old definition back as a new version.
//
// It is a new version rather than a rewind of the counter, because a run already
// recorded against v7 must keep meaning what it meant. A restore of a version that
// was promoted is promoted again on the spot: it is the definition production was
// already approved to run, and asking for a fresh evaluation of it would leave the
// broken version live while somebody waited.
func (s *Store) RestoreAgentVersion(ctx context.Context, agentID string, version int, actorID string) (Agent, error) {
	source, err := s.AgentVersionByNumber(ctx, agentID, version)
	if err != nil {
		return Agent{}, err
	}
	state, err := s.AgentReleaseState(ctx, agentID)
	if err != nil {
		return Agent{}, err
	}
	var agent Agent
	err = s.pool.QueryRow(ctx, `UPDATE agent_definitions
		SET name=$2,description=$3,runtime_profile_id=$4,runtime_image_id=$5,mcp_bundle_id=$6,model_endpoint_id=$7,
		    workspace_id=$8,system_prompt=$9,spec=$10,security_profile_id=$11,network_profile_id=$12,version=version+1,updated_at=now()
		WHERE id=$1
		RETURNING id,owner_id,name,description,runtime_type,runtime_profile_id,runtime_image_id,security_profile_id,network_profile_id,mcp_bundle_id,model_endpoint_id,workspace_id,version,spec,created_at,updated_at`,
		agentID, source.Name, source.Description, source.RuntimeProfileID, source.RuntimeImageID, source.MCPBundleID,
		source.ModelEndpointID, source.WorkspaceID, source.SystemPrompt, source.Spec, source.SecurityProfileID, source.NetworkProfileID).
		Scan(&agent.ID, &agent.OwnerID, &agent.Name, &agent.Description, &agent.RuntimeType, &agent.RuntimeProfileID,
			&agent.RuntimeImageID, &agent.SecurityProfileID, &agent.NetworkProfileID, &agent.MCPBundleID,
			&agent.ModelEndpointID, &agent.WorkspaceID, &agent.Version, &agent.Spec, &agent.CreatedAt, &agent.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	note := fmt.Sprintf("v%d 복원", version)
	if err := s.RecordAgentVersion(ctx, agent, note, actorID); err != nil {
		return agent, err
	}
	if state.PromotedVersion != nil && *state.PromotedVersion == version {
		if _, err := s.pool.Exec(ctx, `UPDATE agent_definitions
			SET promoted_version=$2, promoted_at=now(), promoted_by=$3, promotion_note=$4, updated_at=now()
			WHERE id=$1`, agentID, agent.Version, nullText(actorID), note+" · 이전에 승격된 정의"); err != nil {
			return agent, err
		}
	}
	return agent, nil
}

// PromotionBlock reports why the execution plane must not run this agent as it
// now stands, or "" when it may.
//
// The gate refuses rather than quietly running the promoted definition instead:
// the Runtime Pod, its tools and its workspace are all provisioned from the live
// definition, so serving an older prompt against a newer Pod would produce a run
// nobody could reason about. A refusal names the version that is approved, which
// is enough to either promote the new one or restore the old one.
func (s *Store) PromotionBlock(ctx context.Context, agentID string) (string, error) {
	state, err := s.AgentReleaseState(ctx, agentID)
	if err != nil {
		return "", err
	}
	return promotionBlock(state), nil
}

// promotionBlock is the decision itself, kept apart from the query so it can be
// read and tested as the rule it is.
func promotionBlock(state AgentRelease) string {
	if !state.RequirePromotion {
		return ""
	}
	if state.PromotedVersion == nil {
		return fmt.Sprintf("이 Agent는 운영 승격이 필요합니다. 아직 승격된 버전이 없습니다(현재 정의 v%d). 사전검증을 통과시킨 뒤 승격해 주세요.", state.Current)
	}
	if *state.PromotedVersion != state.Current {
		current, promoted := strconv.Itoa(state.Current), strconv.Itoa(*state.PromotedVersion)
		// v3은, not v3는: a digit's particle follows how the digit is said.
		return fmt.Sprintf("이 Agent는 운영 승격이 필요합니다. 현재 정의 v%s%s 승격되지 않았습니다(운영 승격: v%s). v%s%s 승격하거나 v%s%s 복원해 주세요.",
			current, korean.Topic(current), promoted,
			current, korean.Object(current), promoted, korean.Object(promoted))
	}
	return ""
}
