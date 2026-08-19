package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// Routes that are reached without the catalog, each for a reason.
//
// This is the list the walk below allows through. It exists so that adding an
// endpoint outside the catalog — and with it a permission nobody declared — has
// to be a deliberate edit to a test rather than a line that slips into a router.
var uncatalogued = map[string]string{
	"GET /api/v1/version":                             "read before login, so the console can report the build it is talking to",
	"GET /api/v1/auth/methods":                        "read before login, to decide which login form to show",
	"POST /api/v1/auth/login":                         "the login itself",
	"GET /api/v1/auth/oidc/start":                     "the OIDC redirect",
	"GET /api/v1/auth/oidc/callback":                  "the OIDC callback",
	"POST /api/v1/triggers/{id}/webhook":              "external systems have no session; the handler verifies an HMAC over the raw body",
	"POST /api/v1/runtime-gateway/tool-approvals":     "the in-Pod MCP gateway authenticates with its runtime token",
	"GET /api/v1/runtime-gateway/tool-approvals/{id}": "the same gateway polling for the decision",
	"POST /api/v1/runtime-gateway/dlp-events":         "the same gateway reporting what its content scanner found",
	"POST /api/v1/runtime-gateway/config-report":      "a runtime initialiser reporting the configuration it wrote, authenticated by the runtime token",
	"GET /api/v1/me":                                  "identifies the caller, session or key, and grants nothing",
}

func walkAPI(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := chi.Walk((&Server{}).Handler().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		found[method+" "+strings.TrimSuffix(route, "/")] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	return found
}

// The point of the catalog is that it is the only way in. A route registered
// somewhere else would carry whatever permission its own line happened to give
// it, which is the arrangement the catalog replaced.
func TestEveryServedRouteIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, item := range (&Server{}).apiRoutes() {
		declared[item.Method+" "+item.Path()] = true
	}
	for served := range walkAPI(t) {
		if declared[served] {
			continue
		}
		if _, allowed := uncatalogued[served]; allowed {
			continue
		}
		t.Errorf("%s is served but not in the route catalog", served)
	}
}

// And the reverse: a catalog entry that reaches nothing documents an endpoint
// that does not exist.
func TestEveryDeclaredRouteIsServed(t *testing.T) {
	served := walkAPI(t)
	for _, item := range (&Server{}).apiRoutes() {
		if !served[item.Method+" "+item.Path()] {
			t.Errorf("%s %s is declared but not served", item.Method, item.Path())
		}
	}
}

func TestCatalogIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, item := range (&Server{}).apiRoutes() {
		key := item.Method + " " + item.Path()
		if seen[key] {
			t.Errorf("%s is declared twice", key)
		}
		seen[key] = true
		if item.Handler == nil {
			t.Errorf("%s has no handler", key)
		}
		if strings.TrimSpace(item.Summary) == "" || item.Tag == "" {
			t.Errorf("%s needs a summary and a tag to appear in the API description", key)
		}
		if !slices.Contains([]string{ScopeRead, ScopeWrite, ScopeRuntime, ScopeBrowser}, item.Scope) {
			t.Errorf("%s has scope %q, which no API key can hold", key, item.Scope)
		}
		// A read that demands a write scope is the mistake the old substring rule
		// made, and it is invisible until a read-only key fails in production.
		if item.Method == http.MethodGet && item.Scope != ScopeRead && item.Scope != ScopeBrowser {
			t.Errorf("%s is a read but requires %q", key, item.Scope)
		}
		// Everything under /admin configures the platform for everyone on it.
		if strings.HasPrefix(item.Pattern, "/admin/") && (item.Role != roleAdmin || item.Scope != ScopeBrowser) {
			t.Errorf("%s must be administrator-only and closed to API keys", key)
		}
		if item.Role != "" && item.Role != roleAdmin && item.Role != roleManager {
			t.Errorf("%s requires role %q, which does not exist", key, item.Role)
		}
	}
}

// Credentials are the one thing an API key must never reach: a key that could
// read the vault or mint another key would make its own scopes decorative.
func TestCredentialRoutesRefuseAPIKeys(t *testing.T) {
	mustBeBrowserOnly := []string{"/secrets", "/api-keys", "/keys/rotate", "/credential", "/admin/"}
	for _, item := range (&Server{}).apiRoutes() {
		for _, fragment := range mustBeBrowserOnly {
			if strings.Contains(item.Pattern, fragment) && item.Scope != ScopeBrowser {
				t.Errorf("%s %s touches %q and must be browser-only", item.Method, item.Path(), fragment)
			}
		}
	}
}

// The published description is generated from the catalog, so the check worth
// making is that it says the same thing the server enforces.
func TestOpenAPIDescribesEveryRoute(t *testing.T) {
	document := (&Server{}).openAPIDocument()
	paths, _ := document["paths"].(map[string]any)
	for _, item := range (&Server{}).apiRoutes() {
		entry, _ := paths[item.Path()].(map[string]any)
		operation, _ := entry[strings.ToLower(item.Method)].(map[string]any)
		if operation == nil {
			t.Fatalf("%s %s is missing from the API description", item.Method, item.Path())
		}
		security, _ := operation["security"].([]map[string]any)
		if item.Scope == ScopeBrowser {
			if len(security) != 1 {
				t.Errorf("%s %s is browser-only and must not offer a bearer scheme", item.Method, item.Path())
			}
			continue
		}
		scopes, _ := security[0]["bearerAuth"].([]string)
		if len(scopes) != 1 || scopes[0] != item.Scope {
			t.Errorf("%s %s is documented as %v but enforced as %q", item.Method, item.Path(), scopes, item.Scope)
		}
	}
}

// authorize is what actually refuses a caller, so its decisions are worth
// checking directly rather than through a route that happens to use them.
func TestAuthorize(t *testing.T) {
	cases := []struct {
		name   string
		route  Route
		role   string
		scopes []string
		status int
	}{
		{name: "a browser session is not limited by scopes", route: read("/x", "T", "s", nil), role: "user", status: http.StatusOK},
		{name: "a key with the scope passes", route: read("/x", "T", "s", nil), role: "user", scopes: []string{ScopeRead}, status: http.StatusOK},
		{name: "a read-only key cannot write", route: write(http.MethodPost, "/x", "T", "s", nil), role: "user", scopes: []string{ScopeRead}, status: http.StatusForbidden},
		{name: "a write key cannot start runtimes", route: manage(http.MethodPost, "/x", "T", "s", nil), role: "user", scopes: []string{ScopeWrite}, status: http.StatusForbidden},
		// Writing implies reading: a key that can create an agent but cannot list
		// agents is not a smaller permission, it is an unusable one.
		{name: "a write key may read", route: read("/x", "T", "s", nil), role: "user", scopes: []string{ScopeWrite}, status: http.StatusOK},
		{name: "a runtime key may read", route: read("/x", "T", "s", nil), role: "user", scopes: []string{ScopeRuntime}, status: http.StatusOK},
		{name: "an MCP key reaches no REST route", route: read("/x", "T", "s", nil), role: "user", scopes: []string{ScopeMCP}, status: http.StatusForbidden},
		{name: "a wildcard key passes", route: manage(http.MethodPost, "/x", "T", "s", nil), role: "user", scopes: []string{"*"}, status: http.StatusOK},
		{name: "no key reaches a browser-only route", route: browser(http.MethodGet, "/x", "T", "s", nil), role: "user", scopes: []string{"*"}, status: http.StatusForbidden},
		{name: "a browser session reaches it", route: browser(http.MethodGet, "/x", "T", "s", nil), role: "user", status: http.StatusOK},
		{name: "a user cannot decide approvals", route: withRole(roleManager, write(http.MethodPost, "/x", "T", "s", nil)), role: "user", status: http.StatusForbidden},
		{name: "a manager can", route: withRole(roleManager, write(http.MethodPost, "/x", "T", "s", nil)), role: "manager", status: http.StatusOK},
		{name: "an administrator can too", route: withRole(roleManager, write(http.MethodPost, "/x", "T", "s", nil)), role: "admin", status: http.StatusOK},
		{name: "a manager is not an administrator", route: admin(http.MethodGet, "/admin/x", "T", "s", nil), role: "manager", status: http.StatusForbidden},
		{name: "an administrator reaches admin routes", route: admin(http.MethodGet, "/admin/x", "T", "s", nil), role: "admin", status: http.StatusOK},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reached := false
			handler := (&Server{}).authorize(test.route, func(w http.ResponseWriter, _ *http.Request) { reached = true })
			request := httptest.NewRequest(test.route.Method, "/api/v1"+test.route.Pattern, nil)
			ctx := context.WithValue(request.Context(), userContextKey, store.User{ID: "u1", Role: test.role})
			if test.scopes != nil {
				ctx = context.WithValue(ctx, apiScopesContextKey, test.scopes)
			}
			recorder := httptest.NewRecorder()
			handler(recorder, request.WithContext(ctx))
			if test.status == http.StatusOK && !reached {
				t.Fatalf("the request must reach the handler; got HTTP %d %s", recorder.Code, recorder.Body.String())
			}
			if test.status != http.StatusOK && (reached || recorder.Code != test.status) {
				t.Fatalf("HTTP %d (reached=%v), want %d", recorder.Code, reached, test.status)
			}
		})
	}
}
