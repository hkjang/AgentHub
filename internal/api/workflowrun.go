package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	dlpguard "github.com/hkjang/AgentHub/internal/guard"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// maxWorkflowStepOutputRunes bounds what one step contributes downstream. Without
// it a verbose agent early in a graph can push every later prompt past the
// model's context window.
const maxWorkflowStepOutputRunes = 8000

// runWorkflow executes the saved graph and stores the trace. The run is
// synchronous: the guardrails already bound how long it can take, and a caller
// that gets the trace back in the response has nothing to poll for.
func (s *Server) runWorkflow(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.WorkflowByID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !item.Enabled {
		writeError(w, http.StatusConflict, "workflow_disabled", "비활성화된 Workflow는 실행할 수 없습니다.")
		return
	}
	var input struct {
		Input string `json:"input"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}

	// Re-validate: the definition, the agents it names and the model bindings can
	// all have changed since the workflow was saved.
	definition, err := s.checkWorkflow(r.Context(), u.ID, item)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_workflow_graph", err.Error())
		return
	}
	steps, err := s.resolveWorkflowSteps(r.Context(), u.ID, definition)
	if err != nil {
		writeError(w, http.StatusBadRequest, "workflow_not_runnable", err.Error())
		return
	}

	run, err := s.store.CreateWorkflowRun(r.Context(), item.ID, u.ID, map[string]any{"input": input.Input, "mode": item.Mode})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	guard := workflow.Guardrails{
		MaxDepth:       item.MaxDepth,
		MaxAgentCalls:  item.MaxAgentCalls,
		MaxParallel:    item.MaxParallelAgents,
		MaxDuration:    time.Duration(item.MaxDurationSeconds) * time.Second,
		MaxOutputRunes: maxWorkflowStepOutputRunes,
	}

	traceID := middleware.GetReqID(r.Context())
	runCtx := workflow.WithTraceID(r.Context(), traceID)
	completion := workflow.NewModelCompletion().WithInspector(dlpguard.NewModel(s.store, s.logger))
	result, runErr := workflow.New(completion).Run(runCtx, item.Mode, steps, guard, input.Input)
	status := result.Status
	if runErr != nil {
		status = "failed"
		result = workflow.Result{Mode: item.Mode, Status: status, Output: runErr.Error()}
	}
	stored, storeErr := s.store.FinishWorkflowRun(r.Context(), run.ID, status, result)
	if storeErr != nil {
		s.logger.Error("workflow run could not be recorded", "run", run.ID, "error", storeErr)
	}
	// One structured line per run carries the whole shape of it: which graph, how
	// long, how many calls and how many tokens it cost.
	s.logger.Info("workflow run finished",
		"traceId", traceID, "runId", run.ID, "workflow", item.ID, "mode", item.Mode,
		"status", status, "agentCalls", result.AgentCall, "durationMs", result.DurationMs, "totalTokens", result.TotalTokens)
	for _, step := range result.Steps {
		s.logger.Info("workflow step finished",
			"traceId", traceID, "runId", run.ID, "step", step.ID, "agent", step.AgentID,
			"status", step.Status, "durationMs", step.DurationMs, "totalTokens", step.TotalTokens, "error", step.Error)
	}
	s.store.Audit(r.Context(), &u, "workflow.run", "workflow", item.ID, status, clientIP(r), map[string]any{"runId": run.ID, "agentCalls": result.AgentCall, "durationMs": result.DurationMs, "totalTokens": result.TotalTokens, "traceId": traceID})

	response := map[string]any{"runId": run.ID, "status": status, "result": result, "traceId": traceID}
	if stored.FinishedAt != nil {
		response["finishedAt"] = stored.FinishedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) workflowRuns(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.WorkflowRuns(r.Context(), u.ID, r.URL.Query().Get("workflowId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// resolveWorkflowSteps binds each graph node to the agent definition it names,
// resolving the system prompt and model endpoint the step will run against.
func (s *Server) resolveWorkflowSteps(ctx context.Context, ownerID string, definition workflowDefinition) ([]workflow.Step, error) {
	models := map[string]struct {
		baseURL string
		name    string
		key     string
	}{}
	steps := make([]workflow.Step, 0, len(definition.Steps))
	for _, step := range definition.Steps {
		agent, err := s.store.AgentByID(ctx, step.AgentID, ownerID, false)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, errors.New("Workflow가 참조하는 Agent를 찾을 수 없습니다: " + step.AgentID)
			}
			return nil, err
		}
		resolved := workflow.Step{
			ID:           step.ID,
			AgentID:      agent.ID,
			AgentName:    agent.Name,
			DependsOn:    step.DependsOn,
			SystemPrompt: agentSystemPrompt(agent),
		}
		if agent.ModelEndpointID == nil || *agent.ModelEndpointID == "" {
			return nil, errors.New(agent.Name + " Agent에 Model Endpoint가 연결되어 있지 않습니다.")
		}
		binding, cached := models[*agent.ModelEndpointID]
		if !cached {
			endpoint, key, modelErr := s.store.ModelEndpointByID(ctx, *agent.ModelEndpointID)
			if modelErr != nil {
				return nil, modelErr
			}
			binding.baseURL, binding.name, binding.key = endpoint.BaseURL, endpoint.DefaultModel, key
			models[*agent.ModelEndpointID] = binding
		}
		resolved.ModelBaseURL, resolved.ModelName, resolved.ModelAPIKey = binding.baseURL, binding.name, binding.key
		steps = append(steps, resolved)
	}
	return steps, nil
}

// agentSystemPrompt reads the instruction stored on the definition, falling back
// to a neutral one so a step without a prompt still behaves predictably.
func agentSystemPrompt(agent store.Agent) string {
	var spec struct {
		SystemPrompt string `json:"systemPrompt"`
	}
	if len(agent.Spec) > 0 {
		_ = json.Unmarshal(agent.Spec, &spec)
	}
	if strings.TrimSpace(spec.SystemPrompt) != "" {
		return spec.SystemPrompt
	}
	return "당신은 " + agent.Name + " 역할을 맡은 엔터프라이즈 에이전트입니다. 요청과 이전 단계 결과를 바탕으로 정확하게 답하세요."
}
