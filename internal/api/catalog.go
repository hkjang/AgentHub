package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// The route catalog.
//
// An endpoint used to be described in three places that had no way of knowing
// about each other: chi registered the path, a middleware guessed the API-key
// scope from substrings of the URL, and a hand-written list produced the
// published OpenAPI description. The guess was the worst of the three — a read
// endpoint whose path happened to contain "/session" demanded runtime:manage,
// and any new write inherited agent:write without anyone deciding it should —
// and the OpenAPI list had drifted to about a fifth of the real surface.
//
// All three now come from this catalog. A route that is not listed here is not
// served, its scope is the one written next to it rather than one inferred from
// its spelling, and the API description is generated from the same lines. A test
// walks the router and fails if anything reaches it another way.

// API-key scopes. A browser session is not restricted by them; they exist so an
// API key can be given less authority than the person who created it.
const (
	// ScopeRead is every read. It is the default scope of a new key.
	ScopeRead = "api:read"
	// ScopeWrite creates and changes the things a user owns: agents, workflows,
	// tasks, workspaces, evaluation sets. It covers reads as well, because an
	// automation that writes almost always reads back what it wrote.
	ScopeWrite = "agent:write"
	// ScopeRuntime starts, stops and opens runtimes, and covers reads for the same
	// reason ScopeWrite does. Separate from ScopeWrite because it spends cluster
	// resources and opens interactive sessions.
	ScopeRuntime = "runtime:manage"
	// ScopeMCP is held by keys used by the MCP endpoint. It grants nothing on the
	// REST surface, which is why no route below carries it.
	ScopeMCP = "mcp:read"
	// ScopeBrowser marks routes an API key may never call, whatever it holds:
	// they read or rotate credentials, or configure the platform itself. A key
	// that could mint another key would make its own scopes meaningless.
	ScopeBrowser = "browser"
)

// APIKeyScopes are the scopes a key may be issued with.
var APIKeyScopes = []string{ScopeRead, ScopeMCP, ScopeRuntime, ScopeWrite}

// Roles a route can require. An empty role means any signed-in user.
const (
	roleManager = "manager"
	roleAdmin   = "admin"
)

// Route is one endpoint: how it is reached, who may reach it, and what it is
// called in the published description.
type Route struct {
	Method  string
	Pattern string
	// Scope is the API-key scope required, or ScopeBrowser for routes only a
	// browser session may call.
	Scope string
	// Role is the minimum role, if the endpoint needs more than a session.
	Role string
	// Tag groups the route in the OpenAPI description.
	Tag string
	// Summary is one line describing what the endpoint does.
	Summary string
	Handler http.HandlerFunc
}

// Path is the full path clients call.
func (r Route) Path() string { return "/api/v1" + r.Pattern }

func route(method, pattern, scope, tag, summary string, handler http.HandlerFunc) Route {
	return Route{Method: method, Pattern: pattern, Scope: scope, Tag: tag, Summary: summary, Handler: handler}
}

// The constructors below exist so the permission of a route is the first thing
// read on its line rather than a field buried in the middle of one.

// read is a GET any key may make.
func read(pattern, tag, summary string, handler http.HandlerFunc) Route {
	return route(http.MethodGet, pattern, ScopeRead, tag, summary, handler)
}

// write changes something the user owns.
func write(method, pattern, tag, summary string, handler http.HandlerFunc) Route {
	return route(method, pattern, ScopeWrite, tag, summary, handler)
}

// manage acts on a runtime's lifecycle or opens a session into one.
func manage(method, pattern, tag, summary string, handler http.HandlerFunc) Route {
	return route(method, pattern, ScopeRuntime, tag, summary, handler)
}

// browser is closed to API keys entirely.
func browser(method, pattern, tag, summary string, handler http.HandlerFunc) Route {
	return route(method, pattern, ScopeBrowser, tag, summary, handler)
}

// withRole raises the role a route needs without changing its scope. Reviewing
// approvals is the case that matters: deciding one is a write, but *reading* the
// queue is a read, and a reviewer with a read-only key should be able to see what
// is waiting for them.
func withRole(name string, item Route) Route {
	item.Role = name
	return item
}

// admin needs an administrator, and is closed to API keys: everything under
// /admin configures the platform for everybody on it.
func admin(method, pattern, tag, summary string, handler http.HandlerFunc) Route {
	item := route(method, pattern, ScopeBrowser, tag, summary, handler)
	item.Role = roleAdmin
	return item
}

// apiRoutes is every authenticated endpoint under /api/v1.
//
// The handful of routes that cannot be here — unauthenticated ones, and the two
// that authenticate as a runtime rather than as a person — are registered in
// server.go and listed in the catalog test, which fails if that set grows
// without being declared.
func (s *Server) apiRoutes() []Route {
	return []Route{
		// --- Platform ---
		// Logging out ends a browser session. A key does not have one, and the
		// route it would reach is not the credential it authenticated with.
		browser(http.MethodPost, "/auth/logout", "Platform", "End the browser session", s.logout),
		browser(http.MethodPost, "/auth/password", "Platform", "Change your own local password", s.changePassword),
		read("/dashboard", "Platform", "Runtime and task summary for the signed-in user", s.dashboard),
		read("/capabilities", "Platform", "Features enabled on this deployment", s.capabilities),
		read("/templates", "Platform", "Published Agent templates", s.templates),
		read("/runtime-profiles", "Platform", "Runtime profiles available to users", s.runtimeProfiles),
		read("/runtime-types", "Platform", "The runtime adapters this build supports, described", s.runtimeTypes),
		read("/models", "Platform", "Enabled model endpoints", s.models),
		read("/events", "Platform", "Recent execution events", s.events),
		read("/usage", "Platform", "Token and cost usage report", s.usage),
		read("/queue", "Platform", "Task queue depth and worker state", s.queue),
		read("/runtime-pool", "Platform", "Warm runtimes held by the pool", s.warmRuntimes),
		read("/notifications", "Platform", "Notifications for the signed-in user", s.notifications),
		read("/api-scopes", "Developer", "API key scopes and what each one reaches", s.apiScopes),
		write(http.MethodPost, "/notifications/{id}/read", "Platform", "Mark a notification read", s.readNotification),

		// --- Agents ---
		read("/agents", "Agents", "List Agent definitions", s.agents),
		write(http.MethodPost, "/agents", "Agents", "Create an Agent definition", s.createAgent),
		write(http.MethodPut, "/agents/{id}", "Agents", "Update an Agent definition", s.updateAgent),
		write(http.MethodDelete, "/agents/{id}", "Agents", "Delete an Agent definition and its runtime", s.deleteAgent),
		read("/agents/{id}/export", "Agents", "Export an Agent definition as YAML", s.exportAgent),
		write(http.MethodPost, "/agents/import", "Agents", "Import an Agent definition from YAML", s.importAgent),
		read("/agents/{id}/goal", "Agents", "Read an Agent's goal and automation settings", s.agentGoal),
		write(http.MethodPut, "/agents/{id}/goal", "Agents", "Save an Agent's goal and automation settings", s.saveAgentGoal),
		read("/agents/{id}/versions", "Agents", "List saved definitions and promotion state", s.agentVersions),
		write(http.MethodPost, "/agents/{id}/promote", "Agents", "Promote a version, or set the promotion gate", s.promoteAgentVersion),
		write(http.MethodPost, "/agents/{id}/versions/{version}/restore", "Agents", "Restore a previous definition as a new version", s.restoreAgentVersion),
		read("/agents/{id}/triggers", "Agents", "List an Agent's triggers", s.agentTriggers),
		write(http.MethodPost, "/agents/{id}/triggers", "Agents", "Create or update a trigger", s.saveAgentTrigger),
		write(http.MethodDelete, "/triggers/{id}", "Agents", "Delete a trigger", s.deleteAgentTrigger),
		write(http.MethodPost, "/agents/{id}/run", "Agents", "Queue a task for an Agent", s.runAgent),
		read("/agents/{id}/mcp-policies", "Agents", "Read an Agent's MCP tool policies", s.agentMCPPolicies),
		write(http.MethodPut, "/agents/{id}/mcp-policies", "Agents", "Save an Agent's MCP tool policy", s.saveAgentMCPPolicy),
		write(http.MethodDelete, "/mcp-policies/{id}", "Agents", "Delete an MCP tool policy", s.deleteAgentMCPPolicy),
		read("/agents/{id}/memories", "Agents", "Read an Agent's stored memories", s.agentMemories),
		read("/agents/{id}/flows", "Agents", "List the flows the Agent's runtime holds", s.agentFlows),
		read("/external-apps", "Platform", "Applications a Goal can send work to", s.externalApps),
		write(http.MethodDelete, "/memories/{id}", "Agents", "Delete a stored memory", s.deleteMemory),

		// --- Runtimes ---
		read("/runtimes", "Runtimes", "List Runtime instances", s.runtimes),
		read("/runtimes/{id}/logs", "Runtimes", "Read Runtime logs", s.runtimeLogs),
		read("/runtimes/{id}/config-report", "Runtimes", "What this runtime reported applying at start", s.runtimeConfigReport),
		manage(http.MethodPost, "/agents/{id}/spawn", "Runtimes", "Create the AgentRuntime for an Agent", s.spawnAgent),
		manage(http.MethodPost, "/runtimes/{id}/start", "Runtimes", "Start a Runtime", s.runtimeAction("running")),
		manage(http.MethodPost, "/runtimes/{id}/stop", "Runtimes", "Stop a Runtime", s.runtimeAction("stopped")),
		manage(http.MethodPost, "/runtimes/{id}/restart", "Runtimes", "Restart a Runtime", s.restartRuntime),
		manage(http.MethodPost, "/runtimes/{id}/launch", "Runtimes", "Create a one-time browser launch URL", s.launchRuntime),

		// --- Sessions ---
		read("/sessions", "Sessions", "List Runtime browser sessions", s.runtimeSessions),
		manage(http.MethodPost, "/runtimes/{id}/sessions", "Sessions", "Open a Runtime browser session", s.createRuntimeSession),
		manage(http.MethodPost, "/sessions/{id}/close", "Sessions", "Close a Runtime browser session", s.closeRuntimeSession),

		// --- Workspaces ---
		read("/workspaces", "Workspaces", "List persistent workspaces", s.workspaces),
		write(http.MethodPost, "/workspaces", "Workspaces", "Create a workspace", s.createWorkspace),
		write(http.MethodPut, "/workspaces/{id}", "Workspaces", "Update a workspace", s.updateWorkspace),
		write(http.MethodDelete, "/workspaces/{id}", "Workspaces", "Delete a workspace", s.deleteWorkspace),
		read("/workspace-snapshots", "Workspaces", "List workspace snapshots", s.workspaceSnapshots),
		write(http.MethodPost, "/workspaces/{id}/snapshots", "Workspaces", "Create a workspace snapshot", s.createWorkspaceSnapshot),
		write(http.MethodPost, "/workspace-snapshots/{id}/restore", "Workspaces", "Restore a workspace from a snapshot", s.restoreWorkspaceSnapshot),
		write(http.MethodDelete, "/workspace-snapshots/{id}", "Workspaces", "Delete a workspace snapshot", s.deleteWorkspaceSnapshot),

		// --- Tasks ---
		read("/tasks", "Tasks", "List queued and finished tasks", s.tasks),
		write(http.MethodPost, "/tasks", "Tasks", "Queue a task", s.createTask),
		read("/tasks/{id}", "Tasks", "Read one task", s.task),
		write(http.MethodPost, "/tasks/{id}/cancel", "Tasks", "Cancel a task", s.cancelTask),
		write(http.MethodPost, "/tasks/{id}/retry", "Tasks", "Retry a failed task", s.retryTask),
		write(http.MethodPost, "/tasks/{id}/resolve", "Tasks", "Close a task a person took over in the runtime", s.resolveTask),
		read("/tasks/{id}/checkpoint", "Tasks", "Read a task's checkpoint", s.taskCheckpoint),
		read("/runs", "Tasks", "List execution runs; filter by agentId, taskId, status, metering, days, q (trace id, failure text or agent name), and scope=all for administrators", s.runs),
		read("/runs/{id}", "Tasks", "Read one run with its steps", s.run),
		read("/artifacts", "Tasks", "List run artifacts", s.artifacts),
		read("/artifacts/{id}/content", "Tasks", "Download an artifact", s.artifactContent),

		// --- Workflows ---
		read("/workflows", "Workflows", "List multi-agent workflows", s.workflows),
		write(http.MethodPost, "/workflows", "Workflows", "Create or update a workflow", s.saveWorkflow),
		write(http.MethodDelete, "/workflows/{id}", "Workflows", "Delete a workflow", s.deleteWorkflow),
		write(http.MethodPost, "/workflows/{id}/validate", "Workflows", "Validate a workflow's graph and guardrails", s.validateWorkflowDefinition),
		write(http.MethodPost, "/workflows/{id}/run", "Workflows", "Run a workflow", s.runWorkflow),
		read("/workflow-runs", "Workflows", "List workflow runs", s.workflowRuns),

		// --- Evaluation ---
		read("/evaluation/test-sets", "Evaluation", "List evaluation test sets", s.evaluationTestSets),
		write(http.MethodPost, "/evaluation/test-sets", "Evaluation", "Create or update an evaluation test set", s.saveEvaluationTestSet),
		write(http.MethodDelete, "/evaluation/test-sets/{id}", "Evaluation", "Delete an evaluation test set", s.deleteEvaluationTestSet),
		read("/evaluations", "Evaluation", "List evaluation results", s.agentEvaluations),
		write(http.MethodPost, "/agents/{id}/evaluate", "Evaluation", "Run a configuration preflight against an Agent", s.evaluateAgent),

		// --- MCP ---
		read("/mcp-servers", "MCP", "List enabled MCP servers", s.mcpServers),
		read("/mcp-bundles", "MCP", "List enabled MCP bundles", s.mcpBundles),
		// Credentials are the one thing a bundle does not carry, so writing one is
		// closed to keys for the same reason the personal vault is.
		browser(http.MethodPut, "/mcp-servers/{id}/credential", "MCP", "Store the caller's credential for an MCP server", s.putMCPCredential(false)),
		browser(http.MethodDelete, "/mcp-servers/{id}/credential", "MCP", "Delete the caller's credential for an MCP server", s.deleteMCPCredential(false)),

		// --- Approvals ---
		withRole(roleManager, read("/approvals", "Approvals", "List approvals awaiting this reviewer", s.reviewerApprovals)),
		withRole(roleManager, write(http.MethodPost, "/approvals/{id}/approve", "Approvals", "Approve a request", s.decideApproval("approved"))),
		withRole(roleManager, write(http.MethodPost, "/approvals/{id}/reject", "Approvals", "Reject a request", s.decideApproval("rejected"))),

		// --- Developer ---
		browser(http.MethodGet, "/secrets", "Developer", "List the caller's personal secrets", s.personalSecrets),
		browser(http.MethodPost, "/secrets", "Developer", "Store a personal secret", s.createPersonalSecret),
		browser(http.MethodDelete, "/secrets/{id}", "Developer", "Delete a personal secret", s.deletePersonalSecret),
		browser(http.MethodPost, "/keys/rotate", "Developer", "Rotate the caller's envelope key", s.rotatePersonalKey),
		browser(http.MethodGet, "/api-keys", "Developer", "List the caller's API keys", s.apiKeys),
		browser(http.MethodPost, "/api-keys", "Developer", "Issue an API key", s.createAPIKey),
		browser(http.MethodDelete, "/api-keys/{id}", "Developer", "Revoke an API key", s.revokeAPIKey),

		// --- Administration ---
		admin(http.MethodGet, "/admin/settings", "Administration", "Read platform settings", s.adminSettings),
		admin(http.MethodPut, "/admin/settings/{key}", "Administration", "Save one platform setting", s.putAdminSetting),
		admin(http.MethodGet, "/admin/overview", "Administration", "Platform health, spend and backlog for one window", s.adminOverview),
		admin(http.MethodGet, "/admin/usage", "Administration", "Token spend broken down by user, agent and model", s.adminSpend),
		admin(http.MethodGet, "/admin/usage/export", "Administration", "Download the spend breakdown as CSV", s.adminSpendExport),
		admin(http.MethodGet, "/admin/policy", "Administration", "Read the platform policy", s.adminPolicy),
		admin(http.MethodPut, "/admin/policy", "Administration", "Replace the platform policy", s.putPolicy),
		admin(http.MethodPost, "/admin/policy/simulate", "Administration", "Evaluate one request against the policy without changing anything", s.simulatePolicy),
		admin(http.MethodGet, "/admin/runtime-settings", "Administration", "Read the per-runtime settings overlays", s.runtimeSettings),
		admin(http.MethodPut, "/admin/runtime-settings", "Administration", "Replace the per-runtime settings overlays", s.putRuntimeSettings),
		admin(http.MethodGet, "/admin/runtime-settings/status", "Administration", "Which runtimes are running the current settings", s.runtimeConfigStatus),
		admin(http.MethodGet, "/admin/dlp", "Administration", "Read the content scanner settings", s.adminDLP),
		admin(http.MethodPut, "/admin/dlp", "Administration", "Configure what is scanned and what happens when it is found", s.putDLP),
		admin(http.MethodPost, "/admin/dlp/scan", "Administration", "Scan a pasted sample without storing it", s.scanSample),
		admin(http.MethodGet, "/admin/execution", "Administration", "Execution plane state: the pause switch, workers and undelivered events", s.adminExecution),
		admin(http.MethodPost, "/admin/execution/pause", "Administration", "Pause or resume task execution", s.pauseExecution),
		admin(http.MethodPut, "/admin/execution/retention", "Administration", "Set how long operational history is kept", s.putRetention),
		admin(http.MethodPost, "/admin/execution/cleanup", "Administration", "Remove history past its retention, or count what would go", s.cleanupHistory),
		admin(http.MethodPost, "/admin/readiness", "Administration", "Ask every dependency this deployment has whether it is working", s.readiness),
		admin(http.MethodPost, "/admin/authentication/check", "Administration", "Ask the identity provider whether single sign-on is configured to work", s.authenticationCheck),
		admin(http.MethodPost, "/admin/kubernetes/check", "Administration", "Ask the cluster whether it answers, holds the namespace and CRD, and permits what the platform does", s.clusterCheck),
		admin(http.MethodPost, "/admin/mcp-servers/{id}/check", "Administration", "Ask an MCP server whether it answers, and what tools it offers", s.mcpServerCheck),
		admin(http.MethodPost, "/admin/models/{id}/check", "Administration", "Ask a model endpoint whether it is reachable and serving the model named on it", s.modelCheck),
		admin(http.MethodPost, "/admin/network-check", "Administration", "Ask a running runtime whether this cluster actually enforces its egress policy", s.networkCheck),
		admin(http.MethodPost, "/admin/execution/reclaim", "Administration", "Take back tasks whose worker stopped responding", s.reclaimTasks),
		admin(http.MethodPost, "/admin/execution/requeue", "Administration", "Put finished tasks back on the queue in bulk", s.requeueTasks),
		admin(http.MethodPost, "/admin/execution/events/redeliver", "Administration", "Redeliver every undeliverable event", s.redeliverEvents),
		admin(http.MethodPost, "/admin/execution/events/{id}/redeliver", "Administration", "Redeliver one undeliverable event", s.redeliverEvents),
		admin(http.MethodGet, "/admin/workers", "Administration", "List worker processes and their capacity", s.adminWorkers),
		admin(http.MethodGet, "/admin/logs", "Administration", "Read the platform log buffer", s.adminLogs),
		admin(http.MethodGet, "/admin/audit", "Administration", "Search the audit trail", s.adminAudit),
		admin(http.MethodGet, "/admin/audit/export", "Administration", "Download the filtered audit trail as CSV", s.adminAuditExport),
		admin(http.MethodGet, "/admin/approvals", "Administration", "List every approval", s.adminApprovals),
		admin(http.MethodPost, "/admin/approvals/{id}/approve", "Administration", "Approve a request as an administrator", s.decideApproval("approved")),
		admin(http.MethodPost, "/admin/approvals/{id}/reject", "Administration", "Reject a request as an administrator", s.decideApproval("rejected")),
		admin(http.MethodGet, "/admin/runtime-profiles", "Administration", "List runtime profiles", s.adminRuntimeProfiles),
		admin(http.MethodPost, "/admin/runtime-profiles", "Administration", "Create or update a runtime profile", s.saveRuntimeProfile),
		admin(http.MethodDelete, "/admin/runtime-profiles/{id}", "Administration", "Delete a runtime profile", s.deleteAdminResource("runtime-profiles")),
		admin(http.MethodGet, "/admin/runtime-images", "Administration", "List runtime images", s.adminRuntimeImages),
		admin(http.MethodPost, "/admin/runtime-images", "Administration", "Create or update a runtime image", s.saveRuntimeImage),
		admin(http.MethodDelete, "/admin/runtime-images/{id}", "Administration", "Delete a runtime image", s.deleteAdminResource("runtime-images")),
		admin(http.MethodGet, "/admin/external-apps", "Administration", "List applications the platform drives but does not run", s.adminExternalApps),
		admin(http.MethodPost, "/admin/external-apps", "Administration", "Create or update an external application", s.saveExternalApp),
		admin(http.MethodDelete, "/admin/external-apps/{id}", "Administration", "Delete an external application", s.deleteExternalApp),
		admin(http.MethodGet, "/admin/models", "Administration", "List model endpoints", s.adminModels),
		admin(http.MethodPost, "/admin/models", "Administration", "Create or update a model endpoint", s.saveModel),
		admin(http.MethodDelete, "/admin/models/{id}", "Administration", "Delete a model endpoint", s.deleteAdminResource("models")),
		admin(http.MethodGet, "/admin/mcp-servers", "Administration", "List MCP servers", s.adminMCPServers),
		admin(http.MethodPost, "/admin/mcp-servers", "Administration", "Create or update an MCP server", s.saveMCPServer),
		admin(http.MethodDelete, "/admin/mcp-servers/{id}", "Administration", "Delete an MCP server", s.deleteAdminResource("mcp-servers")),
		admin(http.MethodPut, "/admin/mcp-servers/{id}/credential", "Administration", "Store the shared credential for an MCP server", s.putMCPCredential(true)),
		admin(http.MethodDelete, "/admin/mcp-servers/{id}/credential", "Administration", "Delete the shared credential for an MCP server", s.deleteMCPCredential(true)),
		admin(http.MethodGet, "/admin/mcp-bundles", "Administration", "List MCP bundles", s.adminMCPBundles),
		admin(http.MethodPost, "/admin/mcp-bundles", "Administration", "Create or update an MCP bundle", s.saveMCPBundle),
		admin(http.MethodDelete, "/admin/mcp-bundles/{id}", "Administration", "Delete an MCP bundle", s.deleteAdminResource("mcp-bundles")),
		// Departments and per-person quotas. The platform-wide limits stay in the
		// governance settings; these are the two levels above it.
		admin(http.MethodGet, "/admin/departments", "Administration", "List departments and their quotas", s.departments),
		admin(http.MethodPost, "/admin/departments", "Administration", "Create or update a department quota", s.saveDepartment),
		admin(http.MethodDelete, "/admin/departments/{id}", "Administration", "Delete a department", s.deleteDepartment),
		admin(http.MethodPost, "/admin/users/{id}/department", "Administration", "Assign a user to a department", s.assignDepartment),
		admin(http.MethodGet, "/admin/user-quotas", "Administration", "List per-user quota overrides", s.userQuotas),
		admin(http.MethodPost, "/admin/users/{id}/quota", "Administration", "Set one user's quota override", s.saveUserQuota),
		admin(http.MethodGet, "/admin/users/{id}/quota", "Administration", "What quota applies to a user, and from where", s.effectiveQuota),
		// Anybody may ask what applies to them.
		read("/quota", "Administration", "What quota applies to me, and from where", s.effectiveQuota),
		admin(http.MethodGet, "/admin/security-profiles", "Administration", "List security profiles", s.adminPolicyProfiles("security")),
		admin(http.MethodPost, "/admin/security-profiles", "Administration", "Create or update a security profile", s.savePolicyProfile("security")),
		admin(http.MethodDelete, "/admin/security-profiles/{id}", "Administration", "Delete a security profile", s.deleteAdminResource("security-profiles")),
		admin(http.MethodGet, "/admin/network-profiles", "Administration", "List network profiles", s.adminPolicyProfiles("network")),
		admin(http.MethodPost, "/admin/network-profiles", "Administration", "Create or update a network profile", s.savePolicyProfile("network")),
		admin(http.MethodDelete, "/admin/network-profiles/{id}", "Administration", "Delete a network profile", s.deleteAdminResource("network-profiles")),
		admin(http.MethodGet, "/admin/users", "Administration", "List users", s.adminUsers),
		admin(http.MethodPut, "/admin/users/{id}", "Administration", "Update a user's role, status or manager", s.updateUser),
	}
}

// register mounts the catalog and wraps each handler in its own authorisation.
//
// The check is per route rather than a middleware over the group because a
// middleware only knows the URL, and a URL is a bad description of what an
// endpoint does — which is how a read of the session list came to demand the
// scope for starting runtimes.
func (s *Server) register(r chi.Router, routes []Route) {
	for _, item := range routes {
		r.Method(item.Method, item.Pattern, s.authorize(item, item.Handler))
	}
}

// authorize applies the route's role and scope.
func (s *Server) authorize(item Route, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromContext(r.Context())
		switch item.Role {
		case roleAdmin:
			if !ok || u.Role != roleAdmin {
				writeError(w, http.StatusForbidden, "forbidden", "관리자 권한이 필요합니다.")
				return
			}
		case roleManager:
			if !ok || (u.Role != roleManager && u.Role != roleAdmin) {
				writeError(w, http.StatusForbidden, "forbidden", "팀장 또는 관리자 권한이 필요합니다.")
				return
			}
		}
		if scopes, isKey := r.Context().Value(apiScopesContextKey).([]string); isKey {
			if item.Scope == ScopeBrowser {
				writeError(w, http.StatusForbidden, "api_key_forbidden", "이 작업은 브라우저 세션으로만 수행할 수 있습니다.")
				return
			}
			if !scopeSatisfies(scopes, item.Scope) {
				writeError(w, http.StatusForbidden, "insufficient_scope", "API Key에 "+item.Scope+" scope가 필요합니다.")
				return
			}
		}
		handler(w, r)
	}
}

// scopeSatisfies reports whether the scopes a key holds cover what a route
// requires.
//
// Writing implies reading. A key that may create an agent but not list agents is
// not a smaller permission, it is an unusable one: the automation that creates
// something almost always has to read it back, and the only way to discover the
// gap was to issue the key and watch it fail. Nothing is widened in the other
// direction — a read key still cannot write, and neither reaches a browser-only
// route.
func scopeSatisfies(held []string, required string) bool {
	if hasScope(held, required) {
		return true
	}
	if required != ScopeRead {
		return false
	}
	return hasScope(held, ScopeWrite) || hasScope(held, ScopeRuntime)
}

// openAPIDocument describes the whole surface, generated from the catalog so it
// cannot describe an endpoint that is not served or miss one that is.
func (s *Server) openAPIDocument() map[string]any {
	paths := map[string]any{}
	tags := []map[string]string{}
	seenTag := map[string]bool{}
	for _, item := range s.apiRoutes() {
		if !seenTag[item.Tag] {
			seenTag[item.Tag] = true
			tags = append(tags, map[string]string{"name": item.Tag})
		}
		responses := map[string]any{
			"200": map[string]any{"description": "Success"},
			"400": map[string]any{"description": "Invalid request"},
			"401": map[string]any{"description": "Authentication required"},
			"403": map[string]any{"description": "Insufficient permission"},
		}
		operation := map[string]any{
			"summary":   item.Summary,
			"tags":      []string{item.Tag},
			"responses": responses,
		}
		// A browser-only route is documented as exactly that, rather than as one
		// some scope can reach.
		if item.Scope == ScopeBrowser {
			operation["security"] = []map[string]any{{"cookieAuth": []string{}}}
			operation["description"] = "브라우저 세션 전용입니다. API Key로는 호출할 수 없습니다."
		} else {
			operation["security"] = []map[string]any{{"bearerAuth": []string{item.Scope}}, {"cookieAuth": []string{}}}
		}
		if item.Role != "" {
			role := "관리자 권한이 필요합니다."
			if item.Role == roleManager {
				role = "팀장 또는 관리자 권한이 필요합니다."
			}
			if existing, _ := operation["description"].(string); existing != "" {
				role = existing + " " + role
			}
			operation["description"] = role
		}
		if parameters := pathParameters(item.Pattern); len(parameters) > 0 {
			operation["parameters"] = parameters
		}
		if item.Method == http.MethodPost || item.Method == http.MethodPut {
			operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		entry, _ := paths[item.Path()].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			paths[item.Path()] = entry
		}
		entry[strings.ToLower(item.Method)] = operation
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "AgentHub Control Plane API",
			"version":     s.version.Version,
			"description": "Offline Enterprise Agent Runtime control plane. Browser sessions require CSRF; API clients use scoped Bearer API keys. Routes marked as browser-only refuse API keys outright.",
		},
		"servers": []map[string]string{{"url": "/", "description": "Current AgentHub deployment"}},
		"tags":    tags,
		"paths":   paths,
		"components": map[string]any{"securitySchemes": map[string]any{
			"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "AgentHub API Key"},
			"cookieAuth": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie},
		}},
	}
}

// pathParameters turns chi's {id} placeholders into OpenAPI parameters, so a
// generated client knows an id belongs in the path rather than in a query.
func pathParameters(pattern string) []map[string]any {
	parameters := []map[string]any{}
	for _, segment := range strings.Split(pattern, "/") {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		name := strings.Trim(segment, "{}")
		parameters = append(parameters, map[string]any{
			"name": name, "in": "path", "required": true,
			"schema": map[string]any{"type": "string"},
		})
	}
	return parameters
}

func (s *Server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.oai.openapi+json;version=3.1")
	writeJSON(w, http.StatusOK, s.openAPIDocument())
}

// ScopeReach is what one API-key scope can call, derived from the catalog.
//
// Choosing a scope used to mean guessing: the console offered four names with a
// few words each, and the only way to find out that a write-only key cannot read
// anything was to issue one and watch it fail.
type ScopeReach struct {
	Scope string `json:"scope"`
	// Routes is how many endpoints the scope reaches.
	Routes int `json:"routes"`
	// Examples are a few of them, for recognising the scope rather than listing it.
	Examples []string `json:"examples"`
}

func (s *Server) scopeReach() []ScopeReach {
	counts := map[string]*ScopeReach{}
	for _, scope := range APIKeyScopes {
		counts[scope] = &ScopeReach{Scope: scope, Examples: []string{}}
	}
	for _, item := range s.apiRoutes() {
		for scope, reach := range counts {
			if item.Scope == ScopeBrowser || !scopeSatisfies([]string{scope}, item.Scope) {
				continue
			}
			reach.Routes++
			// Examples describe what the scope is for, so they come from the routes
			// that require it rather than the reads it also happens to cover.
			if item.Scope == scope && len(reach.Examples) < 4 {
				reach.Examples = append(reach.Examples, item.Method+" "+item.Path())
			}
		}
	}
	// The MCP scope reaches no REST route at all; saying so is the point.
	items := make([]ScopeReach, 0, len(APIKeyScopes))
	for _, scope := range APIKeyScopes {
		items = append(items, *counts[scope])
	}
	return items
}

func (s *Server) apiScopes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.scopeReach()})
}
