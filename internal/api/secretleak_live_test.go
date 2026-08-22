package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// A secret that goes in must not come back out.
//
// The platform holds several kinds — a person's own secrets, a model endpoint's
// API key, an MCP server's credential, a webhook signing secret, an external
// application's key, and the encrypted values behind some settings — and each is
// read back by something that needs it: the runtime spec that provisions a Pod,
// the gateway that attaches a credential, the handler that verifies a signature.
// None of them is meant to reach a browser, and the store says so in a comment.
//
// A comment is not a test. This puts a value nobody would type by accident into
// every place that takes one, reads every route a person can read, and looks for
// it. It is the check that would catch the next "test connection" endpoint that
// helpfully echoes back what it was given.
//
//	AGENTHUB_TEST_URL=http://127.0.0.1:18080 AGENTHUB_TEST_USER=… \
//	AGENTHUB_TEST_PASSWORD=… go test ./internal/api/ -run SecretsNeverComeBack -v
func TestSecretsNeverComeBackOutOfTheAPI(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to run this against a live deployment")
	}
	client := login(t, base)

	// One sentinel per kind, so a leak names which door it came out of.
	sentinels := map[string]string{}
	sentinel := func(kind string) string {
		value := "SENTINEL-" + strings.ToUpper(kind) + "-9c1f4a7e2b"
		sentinels[kind] = value
		return value
	}

	// A personal secret.
	client.post(t, "/api/v1/secrets", map[string]any{
		"name": "leakprobe-personal", "kind": "token", "value": sentinel("personal"),
	})
	// A model endpoint's API key. Enabled on purpose, and pointed at the discard
	// port so nothing can call it: a disabled endpoint is not resolved by the code
	// that reads keys, so planting one there exercises nothing. That is how this
	// check first passed against a deliberately leaking build.
	client.post(t, "/api/v1/admin/models", map[string]any{
		"id": "leakprobe-model", "name": "누출 확인 엔드포인트", "provider": "openai",
		"baseUrl": "http://127.0.0.1:9/v1", "defaultModel": "leakprobe", "enabled": true,
		"secret": sentinel("model"),
	})
	// A webhook trigger's signing secret, on whatever agent exists.
	if agentID := client.firstAgentID(t); agentID != "" {
		client.post(t, "/api/v1/agents/"+agentID+"/triggers", map[string]any{
			"name": "누출 확인", "type": "webhook", "secret": sentinel("webhook"),
			"taskTitle": "x", "taskInput": "y", "enabled": false,
		})
	}

	defer func() {
		client.delete("/api/v1/admin/models/leakprobe-model")
		for _, item := range client.list(t, "/api/v1/secrets") {
			if name, _ := item["name"].(string); name == "leakprobe-personal" {
				client.delete("/api/v1/secrets/" + fmt.Sprint(item["id"]))
			}
		}
		if agentID := client.firstAgentID(t); agentID != "" {
			for _, item := range client.list(t, "/api/v1/agents/"+agentID+"/triggers") {
				if name, _ := item["name"].(string); name == "누출 확인" {
					client.delete("/api/v1/triggers/" + fmt.Sprint(item["id"]))
				}
			}
		}
	}()

	// Every route a person can read, including the ones that need an id.
	paths := []string{
		"/api/v1/dashboard", "/api/v1/capabilities", "/api/v1/templates", "/api/v1/runtime-profiles",
		"/api/v1/runtime-types", "/api/v1/models", "/api/v1/events", "/api/v1/usage", "/api/v1/queue",
		"/api/v1/runtime-pool", "/api/v1/notifications", "/api/v1/api-scopes", "/api/v1/agents",
		"/api/v1/external-apps", "/api/v1/runtimes", "/api/v1/sessions", "/api/v1/workspaces",
		"/api/v1/workspace-snapshots", "/api/v1/tasks", "/api/v1/runs", "/api/v1/artifacts",
		"/api/v1/workflows", "/api/v1/workflow-runs", "/api/v1/evaluation/test-sets", "/api/v1/evaluations",
		"/api/v1/mcp-servers", "/api/v1/mcp-bundles", "/api/v1/approvals", "/api/v1/secrets",
		"/api/v1/api-keys", "/api/v1/quota", "/api/v1/me",
		"/api/v1/admin/models", "/api/v1/admin/mcp-servers", "/api/v1/admin/external-apps",
		"/api/v1/admin/settings", "/api/v1/admin/overview", "/api/v1/admin/users", "/api/v1/admin/audit",
	}
	if agentID := client.firstAgentID(t); agentID != "" {
		for _, suffix := range []string{"/export", "/goal", "/versions", "/triggers", "/mcp-policies", "/memories", "/flows"} {
			paths = append(paths, "/api/v1/agents/"+agentID+suffix)
		}
	}

	checked := 0
	for _, path := range paths {
		body, status := client.get(path)
		if status == http.StatusNotFound || status == http.StatusForbidden {
			continue
		}
		checked++
		for kind, value := range sentinels {
			if strings.Contains(body, value) {
				t.Errorf("%s returned the %s secret verbatim", path, kind)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d route(s) answered; this check is not reading the API", checked)
	}
	t.Logf("%d routes read, %d secret kinds planted, none came back", checked, len(sentinels))
}

type apiClient struct {
	base, cookie, csrf string
	http               *http.Client
}

func login(t *testing.T, base string) *apiClient {
	t.Helper()
	client := &apiClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
	payload, _ := json.Marshal(map[string]string{
		"username": os.Getenv("AGENTHUB_TEST_USER"), "password": os.Getenv("AGENTHUB_TEST_PASSWORD"),
	})
	request, _ := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", strings.NewReader(string(payload)))
	request.Header.Set("content-type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login returned %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		client.cookie += cookie.Name + "=" + cookie.Value + "; "
		if cookie.Name == "agenthub_csrf" {
			client.csrf = cookie.Value
		}
	}
	return client
}

func (c *apiClient) do(method, path string, body any) (string, int) {
	var reader io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		reader = strings.NewReader(string(payload))
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return "", 0
	}
	request.Header.Set("cookie", c.cookie)
	request.Header.Set("x-csrf-token", c.csrf)
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", 0
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return string(raw), response.StatusCode
}

func (c *apiClient) get(path string) (string, int) { return c.do(http.MethodGet, path, nil) }
func (c *apiClient) delete(path string)            { c.do(http.MethodDelete, path, nil) }

// post plants one secret, and fails the test if it could not.
//
// A sentinel that never reached the database proves nothing about whether it
// leaks, and a check that quietly plants two of three is a check that has stopped
// covering the third. This is how the model endpoint's key went unplanted on the
// first run: the field was named something else and the failure was a log line.
func (c *apiClient) post(t *testing.T, path string, body any) {
	t.Helper()
	raw, status := c.do(http.MethodPost, path, body)
	if status >= 400 {
		t.Fatalf("could not plant a secret at %s (%d): %s — an unplanted sentinel proves nothing", path, status, first(raw, 200))
	}
}

func (c *apiClient) list(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, _ := c.get(path)
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload.Items
}

func (c *apiClient) firstAgentID(t *testing.T) string {
	t.Helper()
	for _, item := range c.list(t, "/api/v1/agents") {
		if id, ok := item["id"].(string); ok {
			return id
		}
	}
	return ""
}

func first(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
