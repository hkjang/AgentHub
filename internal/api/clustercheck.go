package api

import (
	"net/http"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
)

// The Kubernetes settings, answered by Kubernetes.
//
// Saving the form proves the form was filled in. Whether the address answers,
// the token is accepted, the namespace exists, the CRD is installed and this
// account may do each thing the platform does were five separate questions with
// one shared answer: a runtime that failed to start, hours later, for somebody
// else.
func (s *Server) clusterCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	checker, ok := s.spawner.(appRuntime.Checker)
	if !ok {
		writeError(w, http.StatusConflict, "kubernetes_not_configured",
			"Kubernetes 연결이 구성되어 있지 않습니다. 시스템 설정 ▸ Kubernetes에서 먼저 설정해 주세요.")
		return
	}
	result, err := checker.CheckCluster(r.Context())
	if err != nil {
		// The cluster refused or could not be reached. That is the answer, not a
		// platform failure, so it is reported as one rather than as a 500.
		s.store.Audit(r.Context(), &u, "kubernetes.check", "cluster", result.Namespace, "unreachable", clientIP(r),
			map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusOK, map[string]any{
			"reachable": false, "detail": shortError(err.Error()), "namespace": result.Namespace,
		})
		return
	}
	missing := []string{}
	for _, permission := range result.Permissions {
		if !permission.Allowed {
			missing = append(missing, permission.What)
		}
	}
	s.store.Audit(r.Context(), &u, "kubernetes.check", "cluster", result.Namespace, verdictWord(len(missing) == 0), clientIP(r),
		map[string]any{"serverVersion": result.ServerVersion, "missing": missing})
	writeJSON(w, http.StatusOK, map[string]any{
		"reachable": true, "check": result, "missing": missing,
	})
}

func verdictWord(ok bool) string {
	if ok {
		return "success"
	}
	return "incomplete"
}
