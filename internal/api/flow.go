package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Listing the flows a runtime holds, so choosing one is picking from a list.
//
// The alternative was asking somebody to paste a UUID out of the Langflow editor's
// address bar into the Goal form, which is the kind of step that is wrong once and
// then fails at three in the morning. The list comes from the runtime itself
// because the runtime is where flows live: the platform does not keep a copy, and
// a copy is exactly what would go stale.

// flowListLimit bounds the answer. A site with hundreds of flows gets the first
// page and a note, rather than the console rendering a select with no end.
const flowListLimit = 200

// flowListTimeout is short on purpose: this is a form being filled in.
const flowListTimeout = 15 * time.Second

type flowSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	EndpointName string `json:"endpointName,omitempty"`
	MCPEnabled   bool   `json:"mcpEnabled"`
}

// agentFlows lists the flows in the Agent's running runtime.
func (s *Server) agentFlows(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), user.ID, user.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !runtimetype.SupportsRunner(agent.RuntimeType, runtimetype.RunnerFlow) {
		writeError(w, http.StatusConflict, "flows_unsupported", runtimetype.Describe(agent.RuntimeType).Label+" 런타임에는 실행할 흐름이 없습니다.")
		return
	}
	instance, err := s.store.LatestRuntimeForAgent(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "runtime_not_running", "흐름 목록은 Runtime이 실행 중일 때 읽을 수 있습니다. Runtime을 먼저 시작해 주세요.")
		return
	}
	connection, err := s.spawner.Connection(r.Context(), appRuntime.Spec{Runtime: instance, Agent: agent})
	if err != nil {
		writeError(w, http.StatusConflict, "runtime_not_running", "흐름 목록을 읽으려면 Runtime이 Ready 상태여야 합니다: "+err.Error())
		return
	}
	items, truncated, err := fetchFlows(r.Context(), connection)
	if err != nil {
		writeError(w, http.StatusBadGateway, "flow_list_failed", "Runtime에서 흐름 목록을 가져오지 못했습니다: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

// fetchFlows asks the runtime for its flows.
//
// header_flows drops each flow's graph from the answer. Without it the same list
// is megabytes — the graphs are the bulk of it — and none of that is anything a
// picker needs.
func fetchFlows(ctx context.Context, connection appRuntime.Connection) ([]flowSummary, bool, error) {
	endpoint := strings.TrimSuffix(connection.Endpoint, "/") + "/api/v1/flows/?get_all=true&header_flows=true"
	requestCtx, cancel := context.WithTimeout(ctx, flowListTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("agenthub:"+connection.Token)))
	request.Header.Set("x-api-key", connection.Token)
	response, err := flowListClient.Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	if response.StatusCode >= 300 {
		return nil, false, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var flows []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		EndpointName string `json:"endpoint_name"`
		MCPEnabled   bool   `json:"mcp_enabled"`
	}
	if err := json.Unmarshal(payload, &flows); err != nil {
		return nil, false, err
	}
	items := make([]flowSummary, 0, len(flows))
	for _, flow := range flows {
		if flow.ID == "" {
			continue
		}
		if len(items) == flowListLimit {
			return items, true, nil
		}
		items = append(items, flowSummary{ID: flow.ID, Name: flow.Name, Description: flow.Description, EndpointName: flow.EndpointName, MCPEnabled: flow.MCPEnabled})
	}
	return items, false, nil
}

var flowListClient = &http.Client{Timeout: flowListTimeout + 5*time.Second}
