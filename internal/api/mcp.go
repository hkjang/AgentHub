package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

const currentMCPVersion = "2026-07-28"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	protocolVersionMeta = "io.modelcontextprotocol/protocolVersion"
	serverInfoMeta      = "io.modelcontextprotocol/serverInfo"
)

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("MCP-Protocol-Version", currentMCPVersion)
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agenthub-mcp", scope="mcp:read"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "MCP API Key가 필요합니다.")
		return
	}
	user, scopes, err := s.store.UserAndScopesByAPIKey(r.Context(), strings.TrimSpace(value[7:]))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agenthub-mcp", error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, "invalid_token", "API Key가 유효하지 않습니다.")
		return
	}
	var request rpcRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32600, "Invalid Request", nil)
		return
	}
	protocol := r.Header.Get("MCP-Protocol-Version")
	modern := protocol == currentMCPVersion
	if protocol != "" && protocol != currentMCPVersion && protocol != "2025-11-25" {
		s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32022, "Unsupported protocol version", map[string]any{"supported": []string{currentMCPVersion, "2025-11-25"}})
		return
	}
	if modern {
		if r.Header.Get("Mcp-Method") != request.Method {
			s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32001, "Header mismatch: Mcp-Method must match the JSON-RPC method", nil)
			return
		}
		var envelope struct {
			Meta map[string]any `json:"_meta"`
			Name string         `json:"name"`
		}
		if len(request.Params) > 0 && string(request.Params) != "null" {
			if err := json.Unmarshal(request.Params, &envelope); err != nil {
				s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32602, "Invalid params", nil)
				return
			}
		}
		if bodyVersion, _ := envelope.Meta[protocolVersionMeta].(string); bodyVersion != currentMCPVersion {
			s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32001, "Header mismatch: protocol version metadata is missing or does not match", nil)
			return
		}
		if request.Method == "tools/call" && (envelope.Name == "" || strings.TrimSpace(r.Header.Get("Mcp-Name")) != envelope.Name) {
			s.rpcErrorStatus(w, http.StatusBadRequest, request.ID, -32001, "Header mismatch: Mcp-Name must match params.name", nil)
			return
		}
	}
	if request.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch request.Method {
	case "initialize":
		if modern {
			s.rpcErrorStatus(w, http.StatusNotFound, request.ID, -32601, "Method not found", nil)
			return
		}
		s.rpcResult(w, request.ID, map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "AgentHub", "version": s.version.Version}})
	case "server/discover":
		s.rpcResultModern(w, request.ID, map[string]any{"resultType": "complete", "supportedVersions": []string{currentMCPVersion, "2025-11-25"}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Use the AgentHub tools to inspect owned agents and workspaces or manage runtime state."})
	case "tools/list":
		if !hasScope(scopes, "mcp:read") && !hasScope(scopes, "api:read") {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires mcp:read scope", nil, modern)
			return
		}
		result := map[string]any{"tools": mcpTools(scopes), "ttlMs": 30000, "cacheScope": "private"}
		if modern {
			s.rpcResultModern(w, request.ID, result)
		} else {
			s.rpcResult(w, request.ID, result)
		}
	case "tools/call":
		s.mcpCall(w, r, request, user, scopes, modern)
	default:
		status := http.StatusOK
		if modern {
			status = http.StatusNotFound
		}
		s.rpcErrorStatus(w, status, request.ID, -32601, "Method not found", nil)
	}
}

// refusalMessage reads the error the console path wrote, so the protocol caller
// is told the same thing a person would have been.
func refusalMessage(recorder *httptest.ResponseRecorder) string {
	var body errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return "작업을 등록하지 못했습니다."
}

func hasScope(scopes []string, scope string) bool {
	return slices.Contains(scopes, scope) || slices.Contains(scopes, "*")
}
func (s *Server) rpcResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}
func (s *Server) rpcError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
func (s *Server) rpcErrorStatus(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	writeJSON(w, status, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
func (s *Server) rpcResultModern(w http.ResponseWriter, id json.RawMessage, result map[string]any) {
	result["_meta"] = map[string]any{serverInfoMeta: map[string]string{"name": "AgentHub", "version": s.version.Version}}
	s.rpcResult(w, id, result)
}

func (s *Server) rpcErrorFor(w http.ResponseWriter, id json.RawMessage, code int, message string, data any, modern bool) {
	if modern {
		payload, ok := data.(map[string]any)
		if !ok {
			payload = map[string]any{}
			if data != nil {
				payload["detail"] = data
			}
		}
		payload["_meta"] = map[string]any{serverInfoMeta: map[string]string{"name": "AgentHub", "version": s.version.Version}}
		data = payload
	}
	s.rpcError(w, id, code, message, data)
}

// mcpTools is what this platform offers over the protocol, filtered to what the
// caller's key may actually call.
//
// Listing a tool a key will be refused for is not a security problem — the
// refusal still happens — but it is a lie to the agent reading the list, which
// will try, fail, and have no way to know the failure was structural. The
// platform's own gateway already filters the tool lists it forwards by policy;
// this is the same courtesy for its own.
func mcpTools(scopes []string) []map[string]any {
	readable := hasScope(scopes, ScopeMCP) || hasScope(scopes, ScopeRead)
	all := []struct {
		allowed bool
		tool    map[string]any
	}{
		{readable, map[string]any{"name": "agenthub_list_agents", "title": "List AgentHub agents", "description": "List the authenticated user's persistent Agent definitions and latest Runtime state.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true}}},
		{readable, map[string]any{"name": "agenthub_list_workspaces", "title": "List AgentHub workspaces", "description": "List persistent Workspaces owned by the authenticated user.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true}}},
		{hasScope(scopes, ScopeRuntime), map[string]any{"name": "agenthub_runtime_action", "title": "Manage an Agent Runtime", "description": "Start or stop a Runtime owned by the authenticated user. State-changing calls require runtime:manage scope.", "inputSchema": map[string]any{"type": "object", "required": []string{"runtimeId", "action"}, "properties": map[string]any{"runtimeId": map[string]any{"type": "string", "format": "uuid"}, "action": map[string]any{"type": "string", "enum": []string{"start", "stop"}}}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true}}},
		{hasScope(scopes, ScopeWrite), map[string]any{"name": "agenthub_queue_task", "title": "Give an agent a job", "description": "Queue a task for one of the authenticated user's agents. The same policy, promotion gate and quota rules apply as from the console. Requires agent:write scope.", "inputSchema": map[string]any{"type": "object", "required": []string{"agentId", "input"}, "properties": map[string]any{"agentId": map[string]any{"type": "string", "format": "uuid"}, "input": map[string]any{"type": "string", "description": "What the agent should do."}, "title": map[string]any{"type": "string"}, "priority": map[string]any{"type": "string", "enum": []string{"critical", "high", "normal", "low", "background"}}}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false}}},
		{readable, map[string]any{"name": "agenthub_task_status", "title": "Read how a task is going", "description": "Read one queued task: its status, attempts, why it is waiting, and what went wrong if it failed.", "inputSchema": map[string]any{"type": "object", "required": []string{"taskId"}, "properties": map[string]any{"taskId": map[string]any{"type": "string", "format": "uuid"}}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true}}},
	}
	tools := []map[string]any{}
	for _, item := range all {
		if item.allowed {
			tools = append(tools, item.tool)
		}
	}
	return tools
}

func (s *Server) mcpCall(w http.ResponseWriter, r *http.Request, request rpcRequest, user store.User, scopes []string, modern bool) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		s.rpcErrorFor(w, request.ID, -32602, "Invalid tool arguments", nil, modern)
		return
	}
	var value any
	var err error
	switch params.Name {
	case "agenthub_list_agents":
		if !hasScope(scopes, "mcp:read") && !hasScope(scopes, "api:read") {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires mcp:read scope", nil, modern)
			return
		}
		value, err = s.store.Agents(r.Context(), user.ID, false)
	case "agenthub_list_workspaces":
		if !hasScope(scopes, "mcp:read") && !hasScope(scopes, "api:read") {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires mcp:read scope", nil, modern)
			return
		}
		value, err = s.store.Workspaces(r.Context(), user.ID, false)
	case "agenthub_queue_task":
		if !hasScope(scopes, ScopeWrite) {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires agent:write scope", nil, modern)
			return
		}
		agentID, _ := params.Arguments["agentId"].(string)
		taskInput, _ := params.Arguments["input"].(string)
		title, _ := params.Arguments["title"].(string)
		priority, _ := params.Arguments["priority"].(string)
		if strings.TrimSpace(agentID) == "" || strings.TrimSpace(taskInput) == "" {
			s.rpcErrorFor(w, request.ID, -32602, "agentId and input are required", nil, modern)
			return
		}
		// Every rule the console's own path applies — the policy engine, the
		// promotion gate, the model binding, the owner's and their department's
		// budgets — applies here, by running that path rather than by restating
		// it. A second entry point with its own copy of the rules is a second set
		// of rules, and the copy is the one that goes stale.
		recorder := httptest.NewRecorder()
		task, enqueueErr := s.enqueueTask(recorder, r, user, agentID, title, taskInput, priority, "mcp", nil)
		if enqueueErr != nil {
			s.rpcErrorFor(w, request.ID, -32003, refusalMessage(recorder), nil, modern)
			return
		}
		value = map[string]any{"taskId": task.ID, "status": task.Status, "title": task.Title}
	case "agenthub_task_status":
		if !hasScope(scopes, ScopeMCP) && !hasScope(scopes, ScopeRead) {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires mcp:read scope", nil, modern)
			return
		}
		taskID, _ := params.Arguments["taskId"].(string)
		if strings.TrimSpace(taskID) == "" {
			s.rpcErrorFor(w, request.ID, -32602, "taskId is required", nil, modern)
			return
		}
		task, taskErr := s.store.AgentTaskByID(r.Context(), taskID, user.ID, user.Role == "admin")
		if taskErr != nil {
			s.rpcErrorFor(w, request.ID, -32004, "task not found", nil, modern)
			return
		}
		// Waiting and failing are different facts about a task, and an agent
		// polling this needs to tell them apart as much as a person does.
		value = map[string]any{
			"taskId": task.ID, "status": task.Status, "attempts": task.Attempts,
			"waitingReason": task.WaitingReason, "lastError": task.LastError,
			"runId": task.CurrentRunID, "updatedAt": task.UpdatedAt,
		}
	case "agenthub_runtime_action":
		if !hasScope(scopes, "runtime:manage") {
			s.rpcErrorFor(w, request.ID, -32001, "API Key requires runtime:manage scope", nil, modern)
			return
		}
		runtimeID, _ := params.Arguments["runtimeId"].(string)
		action, _ := params.Arguments["action"].(string)
		desired := map[string]string{"start": "running", "stop": "stopped"}[action]
		if runtimeID == "" || desired == "" {
			s.rpcErrorFor(w, request.ID, -32602, "runtimeId and a valid action are required", nil, modern)
			return
		}
		current, currentErr := s.store.RuntimeByID(r.Context(), runtimeID, user.ID, false)
		if currentErr != nil {
			err = currentErr
			break
		}
		agent, agentErr := s.store.AgentByID(r.Context(), current.AgentID, user.ID, false)
		if agentErr != nil {
			err = agentErr
			break
		}
		runtimeSpec, specErr := s.runtimeSpec(r, current, agent)
		if specErr != nil {
			err = specErr
			break
		}
		// Marked first, under the owner's quota — charged to the runtime's owner for
		// the same reason as the REST path, and held while the state is written so
		// two starts arriving together cannot both take the last one.
		if action == "start" && current.DesiredState != "running" {
			value, err = s.store.StartRuntimeWithinQuota(r.Context(), runtimeID, user.ID, runtimeSpec.Profile.ID, false)
		} else {
			value, err = s.store.UpdateRuntimeDesiredState(r.Context(), runtimeID, user.ID, desired, false)
		}
		if err != nil {
			break
		}
		if action == "start" {
			err = s.spawner.Start(r.Context(), runtimeSpec)
		} else {
			err = s.spawner.Stop(r.Context(), runtimeSpec)
		}
		if err != nil && !errors.Is(err, appRuntime.ErrNotConfigured) {
			break
		}
		err = nil
	default:
		s.rpcErrorFor(w, request.ID, -32602, "Unknown tool", map[string]any{"name": params.Name}, modern)
		return
	}
	if err != nil {
		result := map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}
		if modern {
			s.rpcResultModern(w, request.ID, result)
		} else {
			s.rpcResult(w, request.ID, result)
		}
		return
	}
	raw, _ := json.Marshal(value)
	s.store.Audit(r.Context(), &user, "mcp.tool_call", "mcp-tool", params.Name, "success", clientIP(r), map[string]any{"tool": params.Name})
	result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": value, "isError": false}
	if modern {
		s.rpcResultModern(w, request.ID, result)
	} else {
		s.rpcResult(w, request.ID, result)
	}
}
