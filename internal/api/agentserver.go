package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
		writeStoreError(w, err)
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
	health, detail := probeAgentServer(r.Context(), server)
	if err := s.store.RecordAgentServerHealth(r.Context(), server.ID, health, detail); err != nil {
		s.logger.Warn("agent server health could not be recorded", "server", server.ID, "error", err)
	}
	s.store.Audit(r.Context(), &u, "agent_server.check", "agent_server", server.ID, health, clientIP(r),
		map[string]any{"detail": detail})
	server.Health, server.HealthDetail = health, detail
	now := time.Now().UTC()
	server.CheckedAt = &now
	writeJSON(w, http.StatusOK, server)
}

// probeAgentServer asks one server what it is.
//
// The check reads the server's own API description rather than a health path,
// because "something answered on this port" is not the same as "this is an agent
// server". A proxy, a parked domain or the wrong service will all answer 200 to
// a bare GET; only the right thing describes the endpoints this platform is
// going to call.
func probeAgentServer(ctx context.Context, server store.AgentServer) (string, string) {
	base := strings.TrimRight(strings.TrimSpace(server.BaseURL), "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/openapi.json", nil)
	if err != nil {
		return "unreachable", "주소를 해석하지 못했습니다: " + err.Error()
	}
	client := &http.Client{Timeout: 10 * time.Second}
	answer, err := client.Do(request)
	if err != nil {
		return "unreachable", agentServerReason(err)
	}
	defer answer.Body.Close()
	if answer.StatusCode == http.StatusUnauthorized || answer.StatusCode == http.StatusForbidden {
		return "refused", "서버가 이 배포의 자격 증명을 받아들이지 않았습니다."
	}
	if answer.StatusCode >= 300 {
		return "unreachable", "서버가 " + answer.Status + " 로 답했습니다."
	}
	var described struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(answer.Body).Decode(&described); err != nil {
		return "unreachable", "이 주소는 API 설명을 돌려주지 않습니다 — 에이전트 서버가 맞는지 확인해 주세요."
	}
	// The endpoints this platform will actually call. A server that answers but
	// cannot start a conversation is not usable, and finding that out at
	// registration is the whole point of checking.
	for _, path := range []string{"/api/conversations"} {
		if _, present := described.Paths[path]; !present {
			return "refused", "이 서버에는 " + path + " 가 없습니다 — 에이전트 서버가 맞는지 확인해 주세요."
		}
	}
	return "healthy", "대화를 시작할 수 있는 서버입니다."
}

// agentServerReason says what went wrong in words an operator can act on.
func agentServerReason(err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "no such host"):
		return "주소를 찾지 못했습니다."
	case strings.Contains(text, "connection refused"):
		return "연결이 거부됐습니다 — 서버가 떠 있는지 확인해 주세요."
	case strings.Contains(text, "certificate"):
		return "인증서를 확인하지 못했습니다."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timeout"):
		return "제때 답하지 않았습니다."
	}
	return "연결하지 못했습니다: " + trimForMessage(text)
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

func trimForMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		return value[:160] + "…"
	}
	return value
}
