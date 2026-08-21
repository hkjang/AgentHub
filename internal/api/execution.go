package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/execution"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/quota"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// maxWebhookBody bounds what an unauthenticated caller can push at the trigger
// endpoint before the signature has been verified.
const maxWebhookBody = 64 * 1024

// --- Goals ---

func (s *Server) agentGoal(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	goal, err := s.store.AgentGoalByID(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	mode, err := s.store.AgentExecutionMode(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": goal, "executionMode": mode})
}

func (s *Server) saveAgentGoal(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input struct {
		ExecutionMode string `json:"executionMode"`
		// Shadows the embedded field so an omitted flag can be told apart from an
		// explicit false: a client that does not know about resuming — an older
		// portal, a GitOps document written before it existed — must not silently
		// turn it off.
		ResumeFromCheckpoint *bool `json:"resumeFromCheckpoint"`
		// The name this setting had when it served one backend. It is still
		// accepted, because a GitOps document written last month says it and
		// dropping the value silently is worse than any name.
		LegacyApprovalMode string `json:"cliApprovalMode"`
		store.AgentGoal
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = input.LegacyApprovalMode
	}
	if input.ExecutionMode == "" {
		input.ExecutionMode = "interactive"
	}
	if !validExecutionMode(input.ExecutionMode) {
		writeError(w, http.StatusBadRequest, "invalid_execution_mode", "실행 모드를 확인해 주세요.")
		return
	}
	goal := input.AgentGoal
	goal.AgentID = agent.ID
	goal.ResumeFromCheckpoint = input.ResumeFromCheckpoint == nil || *input.ResumeFromCheckpoint
	if err := validateGoal(&goal); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_goal", err.Error())
		return
	}
	// Which runner a Goal may use depends on the agent, not on the Goal: only a
	// runtime that holds its own flows can run one.
	if err := validateRunner(&goal, agent.RuntimeType); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_runner", err.Error())
		return
	}
	if err := s.store.SetAgentExecutionMode(r.Context(), agent.ID, u.ID, u.Role == "admin", input.ExecutionMode); err != nil {
		writeStoreError(w, err)
		return
	}
	saved, err := s.store.PutAgentGoal(r.Context(), goal)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "agent.goal.update", "agent", agent.ID, "success", clientIP(r), map[string]any{"executionMode": input.ExecutionMode})
	writeJSON(w, http.StatusOK, map[string]any{"goal": saved, "executionMode": input.ExecutionMode})
}

// validateRunner decides whether this agent can be handed its work the way the
// Goal asks for.
//
// The check lives here rather than at execution time because the failure it
// prevents is a task that queues, starts a Pod and then fails: an agent whose
// runtime has no flow engine, or a flow-backed Goal with no flow chosen. Both are
// answerable while somebody is still looking at the form.
func validateRunner(goal *store.AgentGoal, runtimeType string) error {
	if goal.Runner == "" {
		goal.Runner = store.RunnerProse
	}
	if !contains([]string{store.RunnerProse, store.RunnerFlow, store.RunnerCLI, store.RunnerDify, store.RunnerACP, store.RunnerInvestigate}, goal.Runner) {
		return errors.New("실행 방식을 확인해 주세요")
	}
	// Kept whatever the runner is, so switching back and forth in the console does
	// not lose a flow or an approval mode somebody already picked.
	goal.FlowID = strings.TrimSpace(goal.FlowID)
	goal.FlowOutputComponent = strings.TrimSpace(goal.FlowOutputComponent)
	if goal.ApprovalMode == "" {
		goal.ApprovalMode = "default"
	}
	if !contains(execution.ApprovalModes, goal.ApprovalMode) {
		return errors.New("에이전트 승인 모드를 확인해 주세요")
	}
	goal.ExternalAppID = strings.TrimSpace(goal.ExternalAppID)
	goal.ExternalInputKey = strings.TrimSpace(goal.ExternalInputKey)
	if goal.Runner == store.RunnerProse {
		return nil
	}
	// An external application is the one backend that starts nothing here: the
	// work happens in a system the site already runs, so this Goal needs no
	// runtime and says nothing about the agent's runtime type.
	if goal.Runner == store.RunnerDify {
		if goal.ExternalAppID == "" {
			return errors.New("실행할 외부 앱을 선택해 주세요")
		}
		if len(goal.ExternalInputKey) > 100 {
			return errors.New("입력 변수 이름이 너무 깁니다")
		}
		return nil
	}
	descriptor := runtimetype.Describe(runtimeType)
	// The rest run inside the Pod. Without a runtime the task would queue, start,
	// find nothing and fail — after consuming a retry.
	if !goal.StartOnDemand {
		return errors.New("이 실행 방식은 Runtime 안에서 이루어지므로 '작업 시 Runtime 시작'을 켜 주세요")
	}
	// One question, asked of the descriptor: can this runtime be handed a task
	// this way. Adding a backend adds a name to the runtime's list rather than
	// another branch here.
	if !runtimetype.SupportsRunner(runtimeType, goal.Runner) {
		return fmt.Errorf("%s 런타임은 이 실행 방식을 지원하지 않습니다(지원: %s)", descriptor.Label, runnerNames(descriptor.Runners))
	}
	if goal.Runner == store.RunnerInvestigate {
		// The investigator reads by default and runs shell commands only in the
		// modes that were chosen deliberately, so the same conflict applies: a Goal
		// that wants a person to approve state-changing work cannot also hand the
		// agent a blanket yes.
		if goal.ApprovalRequired && approvalModeAllowsShell(goal.ApprovalMode) {
			return errors.New("사람 승인을 요구하는 목표에서는 승인 모드 auto·yolo 를 쓸 수 없습니다. 승인 모드를 낮추거나 승인 요구를 끄세요")
		}
		return nil
	}
	if goal.Runner == store.RunnerACP {
		// No conflict to refuse here any more. Under this backend the platform
		// answers the agent itself, so a Goal that asks for human approval gets it:
		// anything that is not read-only goes to a person, whatever the mode says.
		// The mode still decides everything else.
		return nil
	}
	if goal.Runner == store.RunnerCLI {
		// The headless runner cannot ask. It hands the approval mode to the agent
		// and reads the result, so a Goal that wants a person to approve
		// state-changing work cannot also tell the agent to approve everything
		// itself — nothing would be left to stop it.
		if goal.ApprovalRequired && goal.ApprovalMode == "yolo" {
			return errors.New("사람 승인을 요구하는 목표에서는 승인 모드 yolo 를 쓸 수 없습니다. 승인 모드를 낮추거나 승인 요구를 끄세요. (ACP 실행에서는 플랫폼이 대신 물어볼 수 있어 함께 쓸 수 있습니다)")
		}
		return nil
	}
	if goal.FlowID == "" {
		return errors.New("실행할 흐름을 선택해 주세요")
	}
	if len(goal.FlowID) > 200 || len(goal.FlowOutputComponent) > 200 {
		return errors.New("흐름 식별자가 너무 깁니다")
	}
	return nil
}

// approvalModeAllowsShell reports whether this mode lets an agent run shell
// commands without asking. It is named for the question rather than the two
// values so the answer stays in one place when a mode is added.
func approvalModeAllowsShell(mode string) bool {
	return mode == "auto" || mode == "yolo"
}

// runnerNames renders a runtime's supported backends for a refusal message, so
// the person reading it knows what they can choose instead.
func runnerNames(runners []string) string {
	if len(runners) == 0 {
		return "추론 루프"
	}
	return "추론 루프, " + strings.Join(runners, ", ")
}

func validExecutionMode(mode string) bool {
	switch mode {
	case "interactive", "task", "scheduled", "event", "service", "hybrid":
		return true
	}
	return false
}

// validateGoal fills defaults and rejects limits outside what the schema allows,
// so a bad value is reported rather than surfacing as a constraint violation.
func validateGoal(goal *store.AgentGoal) error {
	defaults := store.DefaultAgentGoal(goal.AgentID)
	if goal.MaxSteps == 0 {
		goal.MaxSteps = defaults.MaxSteps
	}
	if goal.MaxToolCalls == 0 {
		goal.MaxToolCalls = defaults.MaxToolCalls
	}
	if goal.MaxDurationSeconds == 0 {
		goal.MaxDurationSeconds = defaults.MaxDurationSeconds
	}
	if goal.CompletionStrategy == "" {
		goal.CompletionStrategy = defaults.CompletionStrategy
	}
	if goal.ConcurrencyPolicy == "" {
		goal.ConcurrencyPolicy = defaults.ConcurrencyPolicy
	}
	if goal.MaxConcurrentRuns == 0 {
		goal.MaxConcurrentRuns = defaults.MaxConcurrentRuns
	}
	if goal.PlannerMode == "" {
		goal.PlannerMode = defaults.PlannerMode
	}
	if goal.SuccessCriteria == nil {
		goal.SuccessCriteria = []string{}
	}
	if goal.FailureCriteria == nil {
		goal.FailureCriteria = []string{}
	}
	switch {
	case goal.MaxSteps < 1 || goal.MaxSteps > 100:
		return errors.New("최대 단계 수는 1~100이어야 합니다")
	case goal.MaxToolCalls < 1 || goal.MaxToolCalls > 1000:
		return errors.New("최대 도구 호출 수는 1~1000이어야 합니다")
	case goal.MaxDurationSeconds < 30 || goal.MaxDurationSeconds > 86400:
		return errors.New("최대 실행 시간은 30~86400초여야 합니다")
	case goal.MaxRetries < 0 || goal.MaxRetries > 10:
		return errors.New("재시도 횟수는 0~10이어야 합니다")
	case goal.MaxConcurrentRuns < 1 || goal.MaxConcurrentRuns > 20:
		return errors.New("동시 실행 수는 1~20이어야 합니다")
	}
	if !contains([]string{"agent", "rule", "judge", "composite"}, goal.CompletionStrategy) {
		return errors.New("완료 판정 방식을 확인해 주세요")
	}
	if !contains([]string{"reject", "queue", "parallel", "replace"}, goal.ConcurrencyPolicy) {
		return errors.New("중복 실행 정책을 확인해 주세요")
	}
	if !contains([]string{"none", "native", "platform", "hybrid"}, goal.PlannerMode) {
		return errors.New("Planner 모드를 확인해 주세요")
	}
	if goal.MaxDelegationDepth < 0 || goal.MaxDelegationDepth > 5 {
		return errors.New("위임 깊이는 0~5여야 합니다")
	}
	// The warm-up window is bounded because it holds a Pod for its whole length:
	// an hour of warm-up before a daily schedule is a runtime that is never off.
	if goal.WarmupSeconds < 0 || goal.WarmupSeconds > 1800 {
		return errors.New("사전 예열 시간은 0~1800초여야 합니다")
	}
	if goal.KeepWarmSeconds < 0 || goal.KeepWarmSeconds > 3600 {
		return errors.New("예열 유지 시간은 0~3600초여야 합니다")
	}
	// Keeping a runtime warm after a task only means anything if the task would
	// otherwise have stopped it.
	if goal.KeepWarmSeconds > 0 && !goal.StopAfterTask {
		return errors.New("예열 유지는 '작업 후 Runtime 중지'가 켜져 있을 때만 의미가 있습니다")
	}
	// A strategy that checks criteria is meaningless without any.
	if len(goal.SuccessCriteria) == 0 && (goal.CompletionStrategy == "rule" || goal.CompletionStrategy == "composite") {
		return errors.New("rule 또는 composite 판정을 사용하려면 완료 조건을 하나 이상 정의해야 합니다")
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// --- Tasks ---

// budgetRefusal reports the reason a task must not even be queued.
//
// A concurrency limit is not a refusal — queueing is what a queue is for — but a
// budget that is already spent would only be discovered by the worker minutes
// later, so the person who asked is told now.
func (s *Server) budgetRefusal(r *http.Request, ownerID, agentID string) string {
	policy, err := s.store.ExecutionPolicy(r.Context())
	if err != nil {
		s.logger.Warn("execution quota policy is unreadable; accepting the task", "error", err)
		return ""
	}
	goal, goalErr := s.store.AgentGoalByID(r.Context(), agentID)
	if goalErr != nil {
		goal = store.DefaultAgentGoal(agentID)
	}
	if policy.TokenBudgetPerUser == 0 && policy.CostBudgetPerUser == 0 && goal.TokenBudget == 0 {
		return ""
	}
	usage, err := s.store.ExecutionUsage(r.Context(), ownerID, agentID, "")
	if err != nil {
		s.logger.Warn("execution usage is unreadable; accepting the task", "error", err)
		return ""
	}
	// Only the budget rules are applied here: the running-task count is left to
	// the worker, which is where waiting for a slot belongs.
	usage.RunningTasks = 0
	if decision := quota.Evaluate(quota.Policy{TokenBudgetPerUser: policy.TokenBudgetPerUser, CostBudgetPerUser: policy.CostBudgetPerUser}, goal.TokenBudget, usage); !decision.Allowed && !decision.Wait {
		return decision.Reason
	}
	return ""
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		AgentID  string `json:"agentId"`
		Title    string `json:"title"`
		Input    string `json:"input"`
		Priority string `json:"priority"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := s.enqueueTask(w, r, u, input.AgentID, input.Title, input.Input, input.Priority, "manual", nil)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

// runAgent is the shortcut from an agent to a task, which is what the Agent
// detail screen's Run button uses.
func (s *Server) runAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Title    string `json:"title"`
		Input    string `json:"input"`
		Priority string `json:"priority"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := s.enqueueTask(w, r, u, chi.URLParam(r, "id"), input.Title, input.Input, input.Priority, "manual", nil)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

// enqueueTask validates the request and queues it. It writes the error response
// itself and returns the error so callers can simply stop.
func (s *Server) enqueueTask(w http.ResponseWriter, r *http.Request, u store.User, agentID, title, taskInput, priority, source string, triggerID *string) (store.AgentTask, error) {
	agent, err := s.store.AgentByID(r.Context(), agentID, u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return store.AgentTask{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = agent.Name + " 작업"
	}
	if len(title) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_task_title", "Task 제목은 200자 이하여야 합니다.")
		return store.AgentTask{}, errors.New("invalid title")
	}
	if priority != "" && !contains([]string{"critical", "high", "normal", "low", "background"}, priority) {
		writeError(w, http.StatusBadRequest, "invalid_priority", "우선순위를 확인해 주세요.")
		return store.AgentTask{}, errors.New("invalid priority")
	}
	// An agent with no model cannot run autonomously, and finding that out only
	// once a worker picks it up wastes a whole attempt.
	if agent.ModelEndpointID == nil || *agent.ModelEndpointID == "" {
		writeError(w, http.StatusConflict, "model_not_bound", "이 Agent에는 Model Endpoint가 연결되어 있지 않아 자동 실행할 수 없습니다.")
		return store.AgentTask{}, errors.New("model not bound")
	}
	// The platform policy decides before anything else does: a rule that says a
	// role may not run an agent is not something an agent's own settings can
	// answer for.
	if refusal := policyRefusal(s.decide(r, u, policy.Request{
		Action: policy.ActionTaskCreate, Agent: agent.Name, AgentID: agent.ID,
	})); refusal != "" {
		writeError(w, http.StatusForbidden, "policy_denied", refusal)
		return store.AgentTask{}, errors.New("policy denied")
	}
	// An agent behind a promotion gate is refused here for the same reason: the
	// answer will not change by waiting in the queue.
	if reason, err := s.store.PromotionBlock(r.Context(), agent.ID); err != nil {
		s.logger.Warn("promotion gate is unreadable; accepting the task", "agent", agent.ID, "error", err)
	} else if reason != "" {
		writeError(w, http.StatusConflict, "promotion_required", reason)
		return store.AgentTask{}, errors.New("promotion required")
	}
	// A budget that is already spent is refused here rather than minutes later by
	// a worker, so the person who asked hears it while they are still looking.
	if reason := s.budgetRefusal(r, agent.OwnerID, agent.ID); reason != "" {
		writeError(w, http.StatusTooManyRequests, "quota_exceeded", reason)
		return store.AgentTask{}, errors.New("quota exceeded")
	}
	task, err := s.store.CreateAgentTask(r.Context(), store.CreateTaskInput{
		AgentID: agent.ID, OwnerID: agent.OwnerID, Title: title, Input: taskInput,
		Priority: priority, Source: source, TriggerID: triggerID, CreatedBy: u.ID,
	})
	if err != nil {
		writeStoreError(w, err)
		return store.AgentTask{}, err
	}
	s.store.Audit(r.Context(), &u, "task.create", "task", task.ID, "success", clientIP(r), map[string]any{"agentId": agent.ID, "source": source})
	s.logger.Info("task queued", "task", task.ID, "agent", agent.ID, "source", source, "priority", task.Priority)
	return task, nil
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.AgentTasks(r.Context(), u.ID, r.URL.Query().Get("agentId"), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.AgentTaskByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	runs, err := s.store.AgentRuns(r.Context(), item.OwnerID, "", item.ID, 20)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": item, "runs": runs})
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.CancelAgentTask(r.Context(), id, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "task.cancel", "task", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// retryTask puts a task back on the queue. By default the attempt resumes from
// the steps earlier attempts completed; "fresh" retires them and starts over,
// which is the right choice when the earlier reasoning rested on something that
// has since been corrected.
func (s *Server) retryTask(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Fresh bool `json:"fresh"`
	}
	// The body is optional: an empty request means "resume", which is what every
	// caller written before this option existed meant.
	if r.ContentLength > 0 && !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.store.RequeueAgentTask(r.Context(), chi.URLParam(r, "id"), u.ID, input.Fresh)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "task_not_retryable", "실패했거나 취소된 Task만 다시 실행할 수 있습니다.")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "task.retry", "task", item.ID, "success", clientIP(r), map[string]any{"fresh": input.Fresh})
	writeJSON(w, http.StatusAccepted, item)
}

// taskCheckpoint reports how much completed work a retry of this task would
// resume from, so the operator chooses between continuing and starting over with
// the number in front of them.
func (s *Server) taskCheckpoint(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	task, err := s.store.AgentTaskByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agent, err := s.store.AgentByID(r.Context(), task.AgentID, task.OwnerID, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	goal, err := s.store.AgentGoalByID(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	checkpoint, err := s.store.TaskCheckpoint(r.Context(), task.ID, agent.Version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"steps": checkpoint.Steps, "lastRunId": checkpoint.LastRunID, "enabled": goal.ResumeFromCheckpoint,
	})
}

// --- Runs ---

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.AgentRuns(r.Context(), u.ID, r.URL.Query().Get("agentId"), r.URL.Query().Get("taskId"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// run returns the whole picture of one attempt: the run, its steps, its timeline
// and what it produced. The detail screen needs all four together.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.AgentRunByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	steps, err := s.store.RunSteps(r.Context(), item.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	events, err := s.store.RunEvents(r.Context(), item.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	artifacts, err := s.store.Artifacts(r.Context(), item.OwnerID, item.ID, 100)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	response := map[string]any{"run": item, "steps": steps, "events": events, "artifacts": artifacts}
	// A run only has a plan when the agent's planner mode produced one.
	if plan, planErr := s.store.PlanForRun(r.Context(), item.ID); planErr == nil {
		response["plan"] = plan
	} else if !errors.Is(planErr, store.ErrNotFound) {
		s.logger.Warn("plan could not be read", "run", item.ID, "error", planErr)
	}
	writeJSON(w, http.StatusOK, response)
}

// --- Artifacts ---

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.Artifacts(r.Context(), u.ID, r.URL.Query().Get("runId"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.ArtifactByID(r.Context(), chi.URLParam(r, "id"), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Agent-authored content is served as a download so a stored document can
	// never execute in the portal's origin.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(item.Name, `"`, "")+"\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(item.Content))
}

// --- Triggers ---

func (s *Server) agentTriggers(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.AgentTriggers(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveAgentTrigger(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input struct {
		store.AgentTrigger
		Secret string `json:"secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	trigger := input.AgentTrigger
	trigger.AgentID, trigger.OwnerID = agent.ID, agent.OwnerID
	trigger.Name = strings.TrimSpace(trigger.Name)
	if trigger.Name == "" || len(trigger.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_trigger_name", "Trigger 이름은 1~80자여야 합니다.")
		return
	}
	if !contains([]string{"manual", "cron", "webhook", "event"}, trigger.Type) {
		writeError(w, http.StatusBadRequest, "invalid_trigger_type", "Trigger 유형을 확인해 주세요.")
		return
	}
	if trigger.Type == "event" {
		// An unknown event type would leave a trigger that looks armed but can
		// never fire, so it is rejected at save time.
		if !store.IsPublishableEvent(trigger.EventType) {
			writeError(w, http.StatusBadRequest, "invalid_event_type",
				"이벤트 종류를 확인해 주세요. 사용 가능한 값: "+strings.Join(store.PublishableEvents, ", "))
			return
		}
		if len(trigger.EventFilter) > 0 {
			var filter map[string]any
			if err := json.Unmarshal(trigger.EventFilter, &filter); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_event_filter", "이벤트 필터는 JSON 객체여야 합니다.")
				return
			}
		}
	} else {
		trigger.EventType, trigger.EventFilter = "", nil
	}
	if trigger.Type == "cron" {
		// Validate here so a broken schedule is rejected at save time rather than
		// silently never firing.
		next, err := execution.NextFireAt(trigger.Schedule, trigger.Timezone, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_schedule", err.Error())
			return
		}
		if trigger.Enabled {
			trigger.NextFireAt = &next
		} else {
			trigger.NextFireAt = nil
		}
	} else {
		trigger.NextFireAt = nil
	}
	saved, err := s.store.PutAgentTrigger(r.Context(), trigger, input.Secret)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "trigger.save", "trigger", saved.ID, "success", clientIP(r), map[string]any{"agentId": agent.ID, "type": saved.Type})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteAgentTrigger(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAgentTrigger(r.Context(), id, u.ID); err != nil {
		writeDeleteError(w, err, "이 Trigger를 참조하는 리소스가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "trigger.delete", "trigger", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// triggerWebhook queues a task from an external system.
//
// This is the only unauthenticated route in the API, so it verifies an HMAC over
// the raw body before doing anything else and never reveals whether the trigger
// exists.
func (s *Server) triggerWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "요청 본문이 너무 큽니다.")
		return
	}
	secret, err := s.store.TriggerSecret(r.Context(), id)
	if err != nil || secret == "" {
		s.logger.Warn("webhook rejected", "trigger", id, "reason", "unknown trigger or no secret")
		writeError(w, http.StatusUnauthorized, "unauthorized", "서명을 확인할 수 없습니다.")
		return
	}
	if !validSignature(secret, body, r.Header.Get("X-AgentHub-Signature")) {
		s.logger.Warn("webhook rejected", "trigger", id, "reason", "signature mismatch")
		writeError(w, http.StatusUnauthorized, "unauthorized", "서명을 확인할 수 없습니다.")
		return
	}
	trigger, err := s.store.AgentTriggerByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "서명을 확인할 수 없습니다.")
		return
	}
	if !trigger.Enabled {
		writeError(w, http.StatusConflict, "trigger_disabled", "비활성화된 Trigger입니다.")
		return
	}
	title := trigger.TaskTitle
	if title == "" {
		title = trigger.Name
	}
	taskInput := trigger.TaskInput
	if len(body) > 0 {
		// The payload is appended rather than replacing the template, so the
		// trigger's own instruction is not lost.
		taskInput = strings.TrimSpace(taskInput + "\n\n# Webhook payload\n" + string(body))
	}
	task, err := s.store.CreateAgentTask(r.Context(), store.CreateTaskInput{
		AgentID: trigger.AgentID, OwnerID: trigger.OwnerID, Title: title, Input: taskInput,
		Priority: trigger.Priority, Source: "webhook", TriggerID: &trigger.ID, CreatedBy: trigger.OwnerID,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.logger.Info("webhook task queued", "trigger", trigger.ID, "task", task.ID, "agent", trigger.AgentID)
	writeJSON(w, http.StatusAccepted, map[string]any{"taskId": task.ID})
}

// validSignature checks an `sha256=<hex>` HMAC over the raw body, the convention
// GitHub and GitLab both use.
func validSignature(secret string, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	header = strings.TrimPrefix(header, "sha256=")
	provided, err := hex.DecodeString(header)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

// --- Memory ---

// agentMemories exposes what an agent remembers, which is the only way to see or
// correct it: memory lives in the platform, not in the Runtime filesystem.
func (s *Server) agentMemories(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items, err := s.store.Memories(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteMemory(r.Context(), id, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "agent.memory.delete", "memory", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// publishEvent records a platform event for the event dispatcher.
//
// Publishing is deliberately best-effort: the thing that produced the event has
// already happened, and failing the request because nothing could be told about
// it would be worse than a missed trigger.
func (s *Server) publishEvent(ctx context.Context, event store.PlatformEvent) {
	if err := s.store.PublishEvent(ctx, event); err != nil {
		s.logger.Warn("platform event could not be published", "type", event.Type, "subject", event.SubjectID, "error", err)
	}
}

func eventPayload(fields map[string]any) json.RawMessage {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// events lists what has happened recently, so an operator can see which event
// types are actually available to subscribe a trigger to.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.RecentEvents(r.Context(), u.ID, r.URL.Query().Get("type"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "types": store.PublishableEvents})
}

// maxUsageWindowDays bounds a report. A year of steps is a table scan nobody is
// waiting for, and the console never asks for more than a quarter.
const maxUsageWindowDays = 120

// usage reports token spend. A user sees their own agents; an admin may ask for
// the whole platform with ?scope=all, which is how the bill is reconciled.
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	from, to, windowErr := reportWindow(r, 30)
	if windowErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_window", windowErr.Error())
		return
	}

	// Scope defaults to the caller's own agents; only an admin may widen it.
	owner := u.ID
	if r.URL.Query().Get("scope") == "all" {
		if u.Role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "전체 사용량은 관리자만 조회할 수 있습니다.")
			return
		}
		owner = ""
	}
	report, err := s.store.Usage(r.Context(), owner, r.URL.Query().Get("agentId"), from, to)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Spend without the limit beside it is a number nobody can act on, so the
	// budget travels with the report — embedded, so every existing field stays
	// exactly where its readers expect it.
	writeJSON(w, http.StatusOK, struct {
		store.UsageReport
		Budget map[string]any `json:"budget,omitempty"`
	}{UsageReport: report, Budget: s.budgetStatus(r, u.ID)})
}

// budgetStatus is one user's standing against the configured limits. It is best
// effort: a report that cannot show a budget is still a useful report.
func (s *Server) budgetStatus(r *http.Request, ownerID string) map[string]any {
	policy, err := s.store.ExecutionPolicy(r.Context())
	if err != nil {
		return nil
	}
	if policy.TokenBudgetPerUser == 0 && policy.CostBudgetPerUser == 0 && policy.MaxRunningTasksPerUser == 0 {
		return nil
	}
	usage, err := s.store.ExecutionUsage(r.Context(), ownerID, "", "")
	if err != nil {
		return nil
	}
	return map[string]any{
		"windowDays":  int(store.QuotaWindow.Hours() / 24),
		"tokenBudget": policy.TokenBudgetPerUser,
		"tokensUsed":  usage.Tokens,
		"costBudget":  policy.CostBudgetPerUser,
		"costUsed":    usage.Cost,
		"currency":    usage.Currency,
		"maxRunning":  policy.MaxRunningTasksPerUser,
		"runningNow":  usage.RunningTasks,
	}
}

// warmRuntimes reports what the runtime warm pool is currently holding, so the
// pre-warming an operator configured is visible rather than inferred from Pod
// start times.
func (s *Server) warmRuntimes(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	owner := u.ID
	if r.URL.Query().Get("scope") == "all" && u.Role == "admin" {
		owner = ""
	}
	items, err := s.store.WarmRuntimes(r.Context(), owner)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// queue reports the task queue's depth and its breakdown by status.
//
// It is what tells an operator whether the execution plane is keeping up, and it
// is the same depth the workers scale their own concurrency on, so the console
// and the scaler cannot disagree about how backed up things are.
func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	owner := u.ID
	if r.URL.Query().Get("scope") == "all" && u.Role == "admin" {
		owner = ""
	}
	snapshot, err := s.store.Queue(r.Context(), owner)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
