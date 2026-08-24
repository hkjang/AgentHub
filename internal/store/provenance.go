package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ProvenanceSettingKey is where the sink's address lives. Absent or empty means
// this deployment exports nothing, which is the ordinary case and must cost
// nothing.
const ProvenanceSettingKey = "provenance"

// ProvenanceSettings is where a deployment sends its account of what it decided.
//
// It is a plain HTTP endpoint rather than a client for one graph product. What
// this platform knows is the same whoever receives it, and the receivers differ:
// a knowledge graph, a warehouse, an archive somebody's auditor reads. Binding
// the export to one vendor's current API would put that vendor's version skew
// inside the control plane — and the first candidate for this already ships a
// README describing endpoints its published package does not serve.
type ProvenanceSettings struct {
	// Endpoint receives one POST per decision. Empty turns the export off.
	Endpoint string `json:"endpoint,omitempty"`
	// Header and Token are an optional credential, sent as one header.
	Header string `json:"header,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Configured reports whether anything should be sent at all.
func (p ProvenanceSettings) Configured() bool { return p.Endpoint != "" }

// DecisionRecord is what this platform observed, in the shape somebody else can
// read.
//
// Every field is the platform's own record rather than the agent's account of
// it: the outcome is the task's recorded status, the reasoning is the
// evaluator's verdict, and the agent's own claim of success is not a field. The
// two are different, and this platform has spent a long time learning to keep
// them apart.
type DecisionRecord struct {
	DecisionID string    `json:"decisionId"`
	OccurredAt time.Time `json:"occurredAt"`
	Category   string    `json:"category"`
	Scenario   string    `json:"scenario"`
	Reasoning  string    `json:"reasoning"`
	Outcome    string    `json:"outcome"`
	// Source says where the work came from: a person, a schedule, a webhook.
	// An auditor's first question about a decision is who asked for it.
	Source string `json:"source"`
	// Agent, AgentVersion and Runtime are what actually ran, not what is
	// configured now: a definition promoted since is a different agent.
	Agent        string `json:"agent"`
	AgentID      string `json:"agentId"`
	AgentVersion int    `json:"agentVersion"`
	Model        string `json:"model,omitempty"`
	RuntimeImage string `json:"runtimeImage,omitempty"`
	TaskID       string `json:"taskId"`
	RunID        string `json:"runId,omitempty"`
	OwnerID      string `json:"ownerId"`
	// ApprovalID is present when a person decided something on the way, which is
	// the edge an auditor follows first.
	ApprovalID string `json:"approvalId,omitempty"`
	SourceURL  string `json:"sourceUrl,omitempty"`
}

// DecisionForTask assembles the record from what was written down, reading no
// opinion from anywhere.
func (s *Store) DecisionForTask(ctx context.Context, taskID string) (DecisionRecord, error) {
	var record DecisionRecord
	var completion []byte
	var approval, runID, model, image *string
	// Everything about what ran is read from the run, and only what the run does
	// not record falls back to the definition. A definition is edited: the agent
	// that ran version 3 against one model is version 7 against another by the
	// time an auditor asks, and a record that reports today's configuration is a
	// record of something that never happened.
	//
	// The image comes from that version's own snapshot rather than the agent's
	// current pin, for the same reason.
	err := s.pool.QueryRow(ctx, `
		SELECT t.id, t.owner_id, t.title, t.status, COALESCE(t.last_error,''), t.source,
		       COALESCE(t.source_url,''), t.approval_id, t.updated_at,
		       a.id, a.name, COALESCE(r.agent_version, a.version),
		       r.id, r.completion,
		       COALESCE(NULLIF(r.model_name,''), m.name),
		       COALESCE(vi.version, i.version)
		FROM agent_tasks t
		JOIN agent_definitions a ON a.id = t.agent_id
		LEFT JOIN agent_runs r ON r.id = t.current_run_id
		LEFT JOIN model_endpoints m ON m.id = COALESCE(r.model_endpoint_id, a.model_endpoint_id)
		LEFT JOIN agent_versions v ON v.agent_id = a.id AND v.version = r.agent_version
		LEFT JOIN runtime_images vi ON vi.id = v.runtime_image_id
		LEFT JOIN runtime_images i ON i.id = a.runtime_image_id
		WHERE t.id = $1`, taskID).
		Scan(&record.TaskID, &record.OwnerID, &record.Scenario, &record.Outcome, &record.Reasoning,
			&record.Source, &record.SourceURL, &approval, &record.OccurredAt,
			&record.AgentID, &record.Agent, &record.AgentVersion,
			&runID, &completion, &model, &image)
	if err != nil {
		return DecisionRecord{}, err
	}
	record.DecisionID = "task:" + record.TaskID
	record.Category = record.Agent
	if approval != nil {
		record.ApprovalID = *approval
	}
	if runID != nil {
		record.RunID = *runID
	}
	if model != nil {
		record.Model = *model
	}
	if image != nil {
		record.RuntimeImage = *image
	}
	// The evaluator's judgement, when there is one: why this platform accepted or
	// refused the work, rather than the sentence the agent finished with.
	if len(completion) > 0 {
		var verdict struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(completion, &verdict); err == nil && verdict.Reason != "" {
			record.Reasoning = verdict.Reason
		}
	}
	return record, nil
}

// ProvenanceEndpoint reads where to send records, if anywhere.
func (s *Store) ProvenanceEndpoint(ctx context.Context) (ProvenanceSettings, error) {
	var settings ProvenanceSettings
	if err := s.Setting(ctx, ProvenanceSettingKey, &settings); err != nil && !errors.Is(err, ErrNotFound) {
		return ProvenanceSettings{}, err
	}
	return settings, nil
}
