package api

import (
	"context"
	"net/http"
	"time"

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
		s.rememberClusterCheck(r.Context(), clusterHealth{
			Reachable: false, Detail: shortError(err.Error()), Namespace: result.Namespace,
		}, u.ID)
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
	s.rememberClusterCheck(r.Context(), clusterHealth{
		Reachable: true, Namespace: result.Namespace, ServerVersion: result.ServerVersion, Missing: missing,
	}, u.ID)
	s.store.Audit(r.Context(), &u, "kubernetes.check", "cluster", result.Namespace, verdictWord(len(missing) == 0), clientIP(r),
		map[string]any{"serverVersion": result.ServerVersion, "missing": missing})
	writeJSON(w, http.StatusOK, map[string]any{
		"reachable": true, "check": result, "missing": missing,
	})
}

// clusterHealth is what the last check of the cluster found.
//
// Kept, because the platform tells people elsewhere what stands between them and
// a working runtime, and until now the only thing it could say about the cluster
// was whether somebody had switched the setting on. A flag that says enabled and
// a cluster that answers are different facts, and the difference is exactly what
// an operator is trying to find out.
type clusterHealth struct {
	Reachable     bool      `json:"reachable"`
	Detail        string    `json:"detail,omitempty"`
	Namespace     string    `json:"namespace,omitempty"`
	ServerVersion string    `json:"serverVersion,omitempty"`
	Missing       []string  `json:"missing,omitempty"`
	CheckedAt     time.Time `json:"checkedAt"`
}

// clusterHealthKey is where that answer lives. A settings row rather than a
// table: there is one cluster, and the value is replaced rather than
// accumulated.
const clusterHealthKey = "kubernetes_health"

func (s *Server) rememberClusterCheck(ctx context.Context, health clusterHealth, actor string) {
	health.CheckedAt = time.Now().UTC()
	if err := s.store.PutSetting(ctx, clusterHealthKey, health, nil, actor); err != nil {
		s.logger.Warn("the cluster check result could not be kept", "error", err)
	}
}

// clusterHealthNow is what the last check found, without asking the cluster
// again.
//
// So an administrator opening the page sees whether this deployment has ever
// been checked at all — the state the platform used to be silent about, and the
// one where somebody debugs a runtime that was never going to start.
func (s *Server) clusterHealthNow(w http.ResponseWriter, r *http.Request) {
	var health clusterHealth
	if err := s.store.Setting(r.Context(), clusterHealthKey, &health); err != nil {
		// Never checked is an answer, not an error.
		writeJSON(w, http.StatusOK, map[string]any{"checked": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checked": true, "health": health})
}

func verdictWord(ok bool) string {
	if ok {
		return "success"
	}
	return "incomplete"
}
