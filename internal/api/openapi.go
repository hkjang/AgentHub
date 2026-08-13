package api

import "net/http"

func (s *Server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.oai.openapi+json;version=3.1")
	paths := map[string]any{}
	add := func(path, method, summary, scope string) {
		operation := map[string]any{"summary": summary, "responses": map[string]any{"200": map[string]any{"description": "Success"}, "400": map[string]any{"description": "Invalid request"}, "401": map[string]any{"description": "Authentication required"}, "403": map[string]any{"description": "Insufficient scope"}}, "security": []map[string]any{{"bearerAuth": []string{scope}}, {"cookieAuth": []string{}}}}
		if method == "post" || method == "put" {
			operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		item, _ := paths[path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = operation
	}
	add("/api/v1/me", "get", "Current user and service version", "api:read")
	add("/api/v1/dashboard", "get", "User runtime summary", "api:read")
	add("/api/v1/templates", "get", "Published Agent templates", "api:read")
	add("/api/v1/agents", "get", "List Agent definitions", "api:read")
	add("/api/v1/agents", "post", "Create Agent definition", "agent:write")
	add("/api/v1/agents/{id}/spawn", "post", "Spawn AgentRuntime", "runtime:manage")
	add("/api/v1/runtimes", "get", "List Runtime instances", "api:read")
	add("/api/v1/runtimes/{id}/start", "post", "Start Runtime", "runtime:manage")
	add("/api/v1/runtimes/{id}/stop", "post", "Stop Runtime", "runtime:manage")
	add("/api/v1/runtimes/{id}/restart", "post", "Restart Runtime", "runtime:manage")
	add("/api/v1/runtimes/{id}/launch", "post", "Create one-time browser launch URL", "runtime:manage")
	add("/api/v1/runtimes/{id}/logs", "get", "Read Runtime logs", "api:read")
	add("/api/v1/workspaces", "get", "List persistent Workspaces", "api:read")
	add("/api/v1/workspaces", "post", "Create Workspace", "agent:write")
	add("/api/v1/workspace-snapshots", "get", "List VolumeSnapshots", "api:read")
	add("/api/v1/workspaces/{id}/snapshots", "post", "Create VolumeSnapshot", "agent:write")
	add("/api/v1/workspace-snapshots/{id}/restore", "post", "Restore Workspace", "agent:write")
	add("/api/v1/sessions", "get", "List Runtime sessions", "api:read")
	add("/api/v1/runtimes/{id}/sessions", "post", "Create Runtime session", "runtime:manage")
	add("/api/v1/workflows", "get", "List Multi-Agent workflows", "api:read")
	add("/api/v1/workflows", "post", "Create or update Workflow", "agent:write")
	add("/api/v1/workflows/{id}/validate", "post", "Validate Workflow DAG and guardrails", "agent:write")
	add("/api/v1/evaluation/test-sets", "get", "List Evaluation Test Sets", "api:read")
	add("/api/v1/evaluation/test-sets", "post", "Create Evaluation Test Set", "agent:write")
	add("/api/v1/agents/{id}/evaluate", "post", "Run Agent configuration preflight", "agent:write")
	add("/api/v1/mcp-servers", "get", "List enabled MCP Servers", "api:read")
	add("/api/v1/mcp-bundles", "get", "List enabled MCP Bundles", "api:read")
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "AgentHub Control Plane API", "version": s.version.Version, "description": "Offline Enterprise Agent Runtime control plane. Browser sessions require CSRF; API clients use scoped Bearer API keys."},
		"servers": []map[string]string{{"url": "/", "description": "Current AgentHub deployment"}},
		"paths":   paths,
		"components": map[string]any{"securitySchemes": map[string]any{
			"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "AgentHub API Key"},
			"cookieAuth": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie},
		}},
	})
}
