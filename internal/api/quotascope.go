package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/quota"
	"github.com/hkjang/AgentHub/internal/store"
)

// Departments and per-person quotas, administered.
//
// The platform-wide limits stay where they were, in the governance settings:
// they are the floor everybody gets. What these add is the two levels above it —
// a department's own capacity, and an exception for one person — and a way to see
// which level a limit actually came from, because "why can I only start two" is
// answered by the level rather than by the number.

func (s *Server) departments(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Departments(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveDepartment(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input store.Department
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Description) > 300 {
		writeError(w, http.StatusBadRequest, "invalid_department", "설명은 300자 이하여야 합니다.")
		return
	}
	if message := quotaComplaint(input.Quota.PerMember); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_quota", "구성원 기본 "+message)
		return
	}
	if message := quotaComplaint(input.Quota.Total); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_quota", "부서 총량 "+message)
		return
	}
	saved, err := s.store.SaveDepartment(r.Context(), input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "department.save", "department", saved.ID, "success", clientIP(r),
		map[string]any{"name": saved.Name, "perMember": saved.Quota.PerMember, "total": saved.Quota.Total})
	writeJSON(w, http.StatusOK, map[string]any{"department": saved})
}

func (s *Server) deleteDepartment(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteDepartment(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	// Its members keep working, on the platform's own limits — the same state a
	// deployment that never made a department is in.
	s.store.Audit(r.Context(), &u, "department.delete", "department", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) assignDepartment(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		DepartmentID string `json:"departmentId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	userID := chi.URLParam(r, "id")
	if err := s.store.SetUserDepartment(r.Context(), userID, strings.TrimSpace(input.DepartmentID)); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "user.department", "user", userID, "success", clientIP(r),
		map[string]any{"departmentId": input.DepartmentID})
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

func (s *Server) userQuotas(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.UserQuotas(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveUserQuota(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input store.UserQuota
	if !decodeJSON(w, r, &input) {
		return
	}
	input.OwnerID = chi.URLParam(r, "id")
	if len(input.Note) > 300 {
		writeError(w, http.StatusBadRequest, "invalid_quota", "메모는 300자 이하여야 합니다.")
		return
	}
	if message := quotaComplaint(input.Quota); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_quota", message)
		return
	}
	if err := s.store.SaveUserQuota(r.Context(), input, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "user.quota", "user", input.OwnerID, "success", clientIP(r),
		map[string]any{"quota": input.Quota, "note": input.Note})
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// effectiveQuota answers "what applies to me, and where did it come from".
// Anybody may ask about themselves; an administrator may ask about anyone.
func (s *Server) effectiveQuota(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	target := chi.URLParam(r, "id")
	if target == "" || target == "me" {
		target = u.ID
	}
	if target != u.ID && u.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "다른 사용자의 Quota는 관리자만 볼 수 있습니다.")
		return
	}
	resolved, err := s.store.ResolveQuota(r.Context(), target)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

// quotaComplaint refuses limits nobody could have meant. A negative limit is a
// typo, and an enormous one is usually a unit mistake — GB where MB was meant —
// which would silently remove the limit it was supposed to set. Zero is not a
// complaint: it is how a level says "inherit".
func quotaComplaint(limits quota.Limits) string {
	switch {
	case limits.MaxRuntimes < 0 || limits.MaxCPUMillis < 0 || limits.MaxMemoryMB < 0 ||
		limits.MaxStorageGB < 0 || limits.MaxRunningTasks < 0 || limits.TokenBudget < 0 || limits.CostBudget < 0:
		return "Quota는 0 이상이어야 합니다(0은 상위 설정을 따른다는 뜻입니다)."
	case limits.MaxRuntimes > 1000:
		return "Runtime 수 상한이 너무 큽니다(최대 1000)."
	case limits.MaxCPUMillis > 10_000_000:
		return "CPU 상한이 너무 큽니다. 단위는 millicore입니다."
	case limits.MaxMemoryMB > 100_000_000:
		return "Memory 상한이 너무 큽니다. 단위는 MB입니다."
	case limits.MaxStorageGB > 1_000_000:
		return "Storage 상한이 너무 큽니다. 단위는 GB입니다."
	}
	return ""
}
