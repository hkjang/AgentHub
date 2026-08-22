package api

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// One person's work must not be visible to another.
//
// Every list this platform serves is scoped by owner, and every one of them is
// scoped by a WHERE clause somebody remembered to write. That is the kind of rule
// which holds until a route is added in a hurry — and the failure is silent,
// because the data looks perfectly ordinary to whoever it is shown to.
//
// So this reads the API as a second, ordinary person and looks for the first
// one's things: their agent, their task, the name of their personal secret. It
// also checks that the administrator's own screens refuse them, because a list
// that is empty for the wrong reason is not isolation either.
//
//	AGENTHUB_TEST_URL=… AGENTHUB_TEST_USER=… AGENTHUB_TEST_PASSWORD=… \
//	AGENTHUB_TEST_OTHER_USER=… go test ./internal/api/ -run OnePersonsWork -v
func TestOnePersonsWorkIsNotVisibleToAnother(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	other := os.Getenv("AGENTHUB_TEST_OTHER_USER")
	if base == "" || other == "" {
		t.Skip("set AGENTHUB_TEST_URL and AGENTHUB_TEST_OTHER_USER to run this against a live deployment")
	}
	owner := login(t, base)

	// Something of the owner's that is unmistakable when it appears somewhere it
	// should not.
	marker := "ISOLATION-9d2b7f1c"
	agentID := owner.firstAgentID(t)
	if agentID == "" {
		t.Skip("the owner has no agent to look for")
	}
	owner.post(t, "/api/v1/secrets", map[string]any{
		"name": marker + "-secret", "kind": "token", "value": "value-does-not-matter",
	})
	defer func() {
		for _, item := range owner.list(t, "/api/v1/secrets") {
			if name, _ := item["name"].(string); strings.Contains(name, marker) {
				owner.delete("/api/v1/secrets/" + str(item["id"]))
			}
		}
	}()
	// Everything the owner can see, as the strings that identify it.
	ownersThings := map[string]string{"agent id": agentID, "secret name": marker + "-secret"}
	for _, item := range owner.list(t, "/api/v1/tasks") {
		if id := str(item["id"]); id != "" {
			ownersThings["task id"] = id
			break
		}
	}

	stranger := loginAs(t, base, other, os.Getenv("AGENTHUB_TEST_PASSWORD"))

	// What an ordinary person may read.
	readable := []string{
		"/api/v1/dashboard", "/api/v1/agents", "/api/v1/tasks", "/api/v1/runs", "/api/v1/artifacts",
		"/api/v1/workspaces", "/api/v1/workspace-snapshots", "/api/v1/runtimes", "/api/v1/sessions",
		"/api/v1/secrets", "/api/v1/api-keys", "/api/v1/workflows", "/api/v1/workflow-runs",
		"/api/v1/events", "/api/v1/usage", "/api/v1/queue", "/api/v1/notifications", "/api/v1/approvals",
		"/api/v1/evaluations", "/api/v1/evaluation/test-sets", "/api/v1/quota", "/api/v1/mcp-servers",
		"/api/v1/agents/" + agentID, "/api/v1/agents/" + agentID + "/goal",
		"/api/v1/agents/" + agentID + "/memories", "/api/v1/agents/" + agentID + "/triggers",
		"/api/v1/agents/" + agentID + "/export", "/api/v1/agents/" + agentID + "/versions",
	}
	read := 0
	for _, path := range readable {
		body, status := stranger.get(path)
		if status >= 400 {
			continue // refused outright, which is isolation working
		}
		read++
		for what, value := range ownersThings {
			if strings.Contains(body, value) {
				t.Errorf("%s showed a stranger the owner's %s", path, what)
			}
		}
	}
	if read < 15 {
		t.Fatalf("only %d route(s) answered a stranger; this check is not reading the API", read)
	}

	// And the administrator's screens must refuse an ordinary person, or every
	// scoped list above is beside the point.
	for _, path := range []string{
		"/api/v1/admin/users", "/api/v1/admin/models", "/api/v1/admin/settings",
		"/api/v1/admin/overview", "/api/v1/admin/audit", "/api/v1/admin/quotas",
	} {
		if body, status := stranger.get(path); status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("%s answered an ordinary person with %d: %s", path, status, first(body, 120))
		}
	}
	// Only when it is true. The first version of this line said "none visible"
	// unconditionally, so a run that had just reported a leak signed off with a
	// sentence saying there wasn't one.
	if !t.Failed() {
		t.Logf("%d routes read as a stranger, %d of the owner's things looked for, none visible", read, len(ownersThings))
	}
}

func str(value any) string {
	text, _ := value.(string)
	return text
}
