package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/agentserver"
	"github.com/hkjang/AgentHub/internal/store"
)

// Registering the agent servers this deployment may send work to.
//
// These are not runtimes the platform starts; they are capacity somebody else
// runs, on a machine this platform can reach. Registering one is therefore an
// administrator's act, and getting it wrong means work leaving for a machine
// nobody meant it to reach.

// agentServers lists what is registered.
func (s *Server) agentServers(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.AgentServers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// What each one is holding right now. Registered capacity an operator cannot
	// see used is a number they have no way to set well.
	load, err := s.store.AgentServerLoad(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{
			"id": item.ID, "name": item.Name, "baseUrl": item.BaseURL, "kind": item.Kind,
			"networkZone": item.NetworkZone, "capacity": item.Capacity, "enabled": item.Enabled,
			"health": item.Health, "healthDetail": item.HealthDetail, "checkedAt": item.CheckedAt,
			"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
			"running": load[item.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "kinds": store.AgentServerKinds})
}

// usableAgentServers is what a person writing a Goal may send work to.
//
// Names, networks and health — not addresses. Where a machine lives is an
// administrator's business, and a Goal only has to say which of them the work
// should go to.
func (s *Server) usableAgentServers(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.AgentServers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	usable := []map[string]any{}
	zones := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		usable = append(usable, map[string]any{
			"id": item.ID, "name": item.Name, "networkZone": item.NetworkZone,
			"health": item.Health, "kind": item.Kind,
		})
		if item.NetworkZone != "" && !seen[strings.ToLower(item.NetworkZone)] {
			seen[strings.ToLower(item.NetworkZone)] = true
			zones = append(zones, item.NetworkZone)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": usable, "zones": zones})
}

// saveAgentServer registers a server or updates one.
func (s *Server) saveAgentServer(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input store.AgentServer
	if !decodeJSON(w, r, &input) {
		return
	}
	if message := agentServerComplaint(input); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_agent_server", message)
		return
	}
	saved, err := s.store.SaveAgentServer(r.Context(), input, u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "agent_server.save", "agent_server", saved.ID, "success", clientIP(r),
		map[string]any{"name": saved.Name, "baseUrl": saved.BaseURL, "zone": saved.NetworkZone})
	writeJSON(w, http.StatusOK, saved)
}

// deleteAgentServer removes one.
func (s *Server) deleteAgentServer(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAgentServer(r.Context(), id); err != nil {
		writeDeleteError(w, err, "이 에이전트 서버를 지정한 Goal이 있어 삭제할 수 없습니다. 그 Goal의 실행 위치를 먼저 바꿔 주세요.")
		return
	}
	s.store.Audit(r.Context(), &u, "agent_server.delete", "agent_server", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// checkAgentServer asks a registered server whether it is working.
//
// It asks the server itself rather than reporting what was configured. A row
// that says healthy because somebody typed a URL is the class of claim this
// platform keeps removing — and the answer is kept, so an operator sees the last
// one even when the server has since stopped answering at all.
func (s *Server) checkAgentServer(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	server, err := s.store.AgentServerByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	health, detail := agentserver.Probe(r.Context(), server.BaseURL)
	if _, _, err := s.store.RecordAgentServerHealth(r.Context(), server.ID, health, detail); err != nil {
		s.logger.Warn("agent server health could not be recorded", "server", server.ID, "error", err)
	}
	s.store.Audit(r.Context(), &u, "agent_server.check", "agent_server", server.ID, health, clientIP(r),
		map[string]any{"detail": detail})
	server.Health, server.HealthDetail = health, detail
	now := time.Now().UTC()
	server.CheckedAt = &now
	writeJSON(w, http.StatusOK, server)
}

// agentServerComplaint refuses what cannot be a server before it is stored.
func agentServerComplaint(input store.AgentServer) string {
	if strings.TrimSpace(input.Name) == "" {
		return "이름을 입력해 주세요."
	}
	if len([]rune(input.Name)) > 80 {
		return "이름이 너무 깁니다."
	}
	parsed, err := url.Parse(strings.TrimSpace(input.BaseURL))
	if err != nil || parsed.Host == "" {
		return "주소를 확인해 주세요."
	}
	// Only the two schemes this platform speaks. A file: or a gopher: address
	// would be stored and then fail somewhere far from here.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "주소는 http 또는 https 여야 합니다."
	}
	if input.Capacity < 0 {
		return "동시 실행 수는 0 이상이어야 합니다."
	}
	if len([]rune(input.NetworkZone)) > 60 {
		return "네트워크 구역 이름이 너무 깁니다."
	}
	return ""
}
