package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// Operating the execution plane from the console.
//
// The overview screen could say a queue had no worker behind it, that tasks had
// exhausted their retries, that events had not been delivered — and then leave
// the operator to fix each one somewhere else, one row at a time, or with psql.
// These are the actions those findings ask for.

// adminExecution reports the operational state: the switch, the workers, and
// what is waiting to be recovered.
func (s *Server) adminExecution(w http.ResponseWriter, r *http.Request) {
	settings := s.operationsSettings(r)
	workers, err := s.store.Workers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	live, err := s.store.LiveWorkers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	events, err := s.store.DeadLetteredEvents(r.Context(), 50)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"paused":             settings.Paused,
		"reason":             settings.Reason,
		"pausedBy":           settings.PausedBy,
		"pausedAt":           settings.PausedAt,
		"retention":          settings.Retention,
		"workers":            workers,
		"liveWorkers":        live,
		"heartbeatSeconds":   int(store.WorkerHeartbeatInterval.Seconds()),
		"staleAfterSeconds":  int(store.WorkerStaleAfter.Seconds()),
		"deadLetteredEvents": events,
	})
}

// operationsSettings reads the switch, treating an unreadable one as "running".
func (s *Server) operationsSettings(r *http.Request) store.OperationsSettings {
	var settings store.OperationsSettings
	if err := s.store.Setting(r.Context(), store.OperationsSettingKey, &settings); err != nil {
		s.logger.Warn("operations settings are unreadable", "error", err)
	}
	return settings
}

// pauseExecution stops workers claiming new tasks, or lets them start again.
//
// Running tasks are left alone: stopping one mid-run would strand exactly the
// rows the caretaker exists to recover. Queueing also continues, because a pause
// is for an upgrade or an incident, and losing the work that arrived during one
// would be a worse outcome than running it late.
func (s *Server) pauseExecution(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Paused bool   `json:"paused"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings := s.operationsSettings(r)
	settings.Paused = input.Paused
	settings.Reason = strings.TrimSpace(input.Reason)
	if input.Paused {
		now := time.Now().UTC()
		settings.PausedBy, settings.PausedAt = u.Username, &now
	} else {
		settings.PausedBy, settings.PausedAt, settings.Reason = "", nil, ""
	}
	if err := settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), store.OperationsSettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	action := "execution.resume"
	if input.Paused {
		action = "execution.pause"
	}
	s.store.Audit(r.Context(), &u, action, "execution", "", "success", clientIP(r), map[string]any{"reason": settings.Reason})
	s.logger.Warn("execution switch changed", "paused", settings.Paused, "by", u.Username, "reason", settings.Reason)
	// Workers read the switch on their own poll, so it takes effect within a few
	// seconds rather than instantly; saying so avoids a second click.
	writeJSON(w, http.StatusOK, map[string]any{
		"paused": settings.Paused, "reason": settings.Reason,
		"pausedBy": settings.PausedBy, "pausedAt": settings.PausedAt,
		"appliesInSeconds": 5,
	})
}

// putRetention configures how long operational history is kept.
func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var policy store.RetentionPolicy
	if !decodeJSON(w, r, &policy) {
		return
	}
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_retention", err.Error())
		return
	}
	settings := s.operationsSettings(r)
	settings.Retention = policy
	if err := s.store.PutSetting(r.Context(), store.OperationsSettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "execution.retention", "execution", "", "success", clientIP(r), policy)
	writeJSON(w, http.StatusOK, policy)
}

// cleanupHistory removes history past the retention policy, or counts what it
// would remove.
func (s *Server) cleanupHistory(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		store.RetentionPolicy
		// DryRun counts without deleting. It is the default: an operator who has
		// just typed a number into a box should see what it means first.
		Apply bool `json:"apply"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	policy := input.RetentionPolicy
	if policy == (store.RetentionPolicy{}) {
		policy = s.operationsSettings(r).Retention
	}
	if policy == (store.RetentionPolicy{}) {
		writeError(w, http.StatusBadRequest, "no_retention", "보관 기간을 먼저 설정해 주세요.")
		return
	}
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_retention", err.Error())
		return
	}
	result, err := s.store.Cleanup(r.Context(), policy, !input.Apply)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if input.Apply {
		s.store.Audit(r.Context(), &u, "execution.cleanup", "execution", "", "success", clientIP(r),
			map[string]any{"policy": policy, "removed": result.Counts})
		s.logger.Warn("operational history removed", "by", u.Username, "removed", result.Counts)
	}
	writeJSON(w, http.StatusOK, result)
}

// reclaimTasks takes back work whose worker stopped responding.
//
// The caretaker does this on its own every half minute; the button exists because
// an operator who has just restarted a crashed worker should not have to wait and
// wonder whether the platform noticed.
func (s *Server) reclaimTasks(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	count, err := s.store.ReclaimStuckTasks(r.Context(), time.Minute)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if count > 0 {
		s.store.Audit(r.Context(), &u, "execution.reclaim", "task", "", "success", clientIP(r), map[string]any{"tasks": count})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reclaimed": count})
}

// requeueTasks puts finished tasks back on the queue in bulk.
func (s *Server) requeueTasks(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Status  string `json:"status"`
		AgentID string `json:"agentId"`
		OwnerID string `json:"ownerId"`
		// SinceHours bounds the recovery, so fixing today's outage does not also
		// restart last month's failures.
		SinceHours int `json:"sinceHours"`
		Limit      int `json:"limit"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	filter := store.RequeueFilter{
		Status: strings.TrimSpace(input.Status), AgentID: input.AgentID, OwnerID: input.OwnerID, Limit: input.Limit,
	}
	if filter.Status == "" {
		filter.Status = store.TaskDeadLetter
	}
	if input.SinceHours > 0 {
		since := time.Now().UTC().Add(-time.Duration(input.SinceHours) * time.Hour)
		filter.Since = &since
	}
	count, err := s.store.RequeueTasks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_requeue", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "execution.requeue", "task", "", "success", clientIP(r),
		map[string]any{"status": filter.Status, "agentId": filter.AgentID, "tasks": count})
	s.logger.Info("tasks requeued in bulk", "by", u.Username, "status", filter.Status, "tasks", count)
	writeJSON(w, http.StatusOK, map[string]any{"requeued": count})
}

// redeliverEvents puts undeliverable events back in the outbox.
func (s *Server) redeliverEvents(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	// The ledger records which subscriber already received which event, so a
	// redelivery does not repeat the deliveries that succeeded.
	count, err := s.store.RedeliverEvents(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "execution.redeliver", "event", chi.URLParam(r, "id"), "success", clientIP(r),
		map[string]any{"events": count})
	writeJSON(w, http.StatusOK, map[string]any{"redelivered": count})
}

// adminWorkers lists the worker processes.
func (s *Server) adminWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.store.Workers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	live, err := s.store.LiveWorkers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	capacity := 0
	for _, worker := range workers {
		if worker.Status == store.WorkerRunning && !worker.Stale {
			capacity += worker.MaxConcurrency
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": workers, "live": live, "capacity": capacity})
}
