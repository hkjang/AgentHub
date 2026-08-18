package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/cryptox"
)

const testRuntimeID = "3f2b8c14-9d5e-4a71-8c2f-6b0d1e7a45c9"

// pathGatewayServer is a Server whose session gateway settings are already cached,
// so the gateway can be exercised without a database behind it.
func pathGatewayServer(t *testing.T, settings sessionGatewaySettings) *Server {
	t.Helper()
	cipher, err := cryptox.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{logger: slog.Default(), cipher: cipher}
	server.sessionSettings, server.sessionSettingsUntil = settings, time.Now().Add(time.Minute)
	return server
}

func TestRuntimeIDFromPathRequiresARuntimeIdentifier(t *testing.T) {
	runtimeID, rest, ok := runtimeIDFromPath("/" + testRuntimeID + "/assets/app.js")
	if !ok || runtimeID != testRuntimeID || rest != "assets/app.js" {
		t.Fatalf("unexpected split: %q %q %v", runtimeID, rest, ok)
	}
	if _, rest, ok = runtimeIDFromPath("/" + testRuntimeID); !ok || rest != "" {
		t.Fatalf("a bare runtime path was not recognised: rest=%q ok=%v", rest, ok)
	}
	// Portal routes and API paths must never be mistaken for a runtime prefix.
	for _, path := range []string{"/", "/agents", "/api/v1/agents", "/assets/index.js", "/healthz", "/sessions/3f2b8c14", "/3f2b8c14-9d5e-4a71-8c2f/x", "/urn:uuid:" + testRuntimeID + "/x", "/{" + testRuntimeID + "}/x"} {
		if _, _, ok := runtimeIDFromPath(path); ok {
			t.Fatalf("path %q was taken for a runtime session", path)
		}
	}
}

func TestRuntimeIDFromRefererOnlyTrustsThisOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://agenthub.test/assets/app.js", nil)
	request.Header.Set("Referer", "http://agenthub.test/"+testRuntimeID+"/")
	if runtimeID, ok := runtimeIDFromReferer(request); !ok || runtimeID != testRuntimeID {
		t.Fatalf("a runtime page referer was not recognised: %q %v", runtimeID, ok)
	}
	// A referer any other site can set says nothing about what the user has open.
	for _, referer := range []string{"", "http://evil.test/" + testRuntimeID + "/", "http://agenthub.test/agents", "not a url"} {
		request.Header.Set("Referer", referer)
		if _, ok := runtimeIDFromReferer(request); ok {
			t.Fatalf("referer %q was trusted", referer)
		}
	}
}

func TestPathGatewayIsOffWhenARuntimeBaseDomainExists(t *testing.T) {
	// With an origin per runtime the Portal's own paths stay the Portal's, so a
	// UUID-shaped route reaches the SPA rather than the gateway.
	server := pathGatewayServer(t, sessionGatewaySettings{Enabled: true, Scheme: "https", BaseDomain: "agents.company.local", SessionHours: 8})
	passed := false
	handler := server.runtimePathGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://agenthub.test/"+testRuntimeID+"/", nil))
	if !passed {
		t.Fatal("the path gateway took over while a runtime base domain was configured")
	}
}

func TestPathGatewayRefusesARuntimePathWithoutASession(t *testing.T) {
	server := pathGatewayServer(t, sessionGatewaySettings{})
	handler := server.runtimePathGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("an unauthenticated runtime request reached the Portal handler")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://agenthub.test/"+testRuntimeID+"/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestPathGatewayLeavesPortalRequestsAlone(t *testing.T) {
	server := pathGatewayServer(t, sessionGatewaySettings{})
	for _, target := range []string{"http://agenthub.test/", "http://agenthub.test/api/v1/me", "http://agenthub.test/assets/index.js"} {
		passed := false
		handler := server.runtimePathGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
		request := httptest.NewRequest(http.MethodGet, target, nil)
		// Even with an open runtime session, a request made from a Portal page is
		// the Portal's: only a runtime page's referer routes to a runtime.
		request.AddCookie(&http.Cookie{Name: runtimePathCookieName(testRuntimeID), Value: sessionCookieValue(t, server, "http://127.0.0.1:1")})
		request.Header.Set("Referer", "http://agenthub.test/agents")
		handler.ServeHTTP(httptest.NewRecorder(), request)
		if !passed {
			t.Fatalf("%s was routed to a runtime", target)
		}
	}
}

func TestPathGatewayProxiesUnderTheRuntimePrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/app.js" {
			t.Errorf("unexpected upstream path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Forwarded-Prefix") != "/"+testRuntimeID {
			t.Errorf("the runtime was not told its base path: %q", r.Header.Get("X-Forwarded-Prefix"))
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("a browser cookie leaked upstream: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	server := pathGatewayServer(t, sessionGatewaySettings{})
	handler := server.runtimePathGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a runtime request reached the Portal handler")
	}))

	// A static asset, so the request does not touch the runtime's idle timer —
	// which is the one thing here that would need a database.
	request := httptest.NewRequest(http.MethodGet, "http://agenthub.test/"+testRuntimeID+"/assets/app.js", nil)
	request.AddCookie(&http.Cookie{Name: runtimePathCookieName(testRuntimeID), Value: sessionCookieValue(t, server, upstream.URL)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if location := response.Header().Get("Location"); location != "/"+testRuntimeID+"/login" {
		t.Fatalf("a redirect lost the runtime prefix: %q", location)
	}

	// The same asset requested from the origin root — which is how a runtime UI
	// asks for it — has to reach the same runtime.
	rootRequest := httptest.NewRequest(http.MethodGet, "http://agenthub.test/assets/app.js", nil)
	rootRequest.AddCookie(&http.Cookie{Name: runtimePathCookieName(testRuntimeID), Value: sessionCookieValue(t, server, upstream.URL)})
	rootRequest.Header.Set("Referer", "http://agenthub.test/"+testRuntimeID+"/")
	rootResponse := httptest.NewRecorder()
	handler.ServeHTTP(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusFound {
		t.Fatalf("a root-relative runtime request was not proxied: %d", rootResponse.Code)
	}
}

func TestExpiredPathSessionIsRefused(t *testing.T) {
	server := pathGatewayServer(t, sessionGatewaySettings{})
	access := runtimeAccess{RuntimeID: testRuntimeID, UserID: "user-1", Endpoint: "http://127.0.0.1:1", Token: "t", RuntimeType: "opencode", ExpiresAt: time.Now().UTC().Add(-time.Minute)}
	raw, _ := json.Marshal(access)
	value, err := server.cipher.Encrypt(raw, runtimePathSessionContext)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://agenthub.test/"+testRuntimeID+"/", nil)
	request.AddCookie(&http.Cookie{Name: runtimePathCookieName(testRuntimeID), Value: value})
	if _, valid := server.pathRuntimeAccess(request, testRuntimeID); valid {
		t.Fatal("an expired session was accepted")
	}
}

func TestPathSessionCookieIsScopedToOneRuntime(t *testing.T) {
	other := "8a1c4d2e-7b39-4f60-9e12-5c3a0d8b7f21"
	if runtimePathCookieName(testRuntimeID) == runtimePathCookieName(other) {
		t.Fatal("two runtimes share a session cookie, so opening one would end the other")
	}
	for _, r := range runtimePathCookieName(testRuntimeID) {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
		if !ok {
			t.Fatalf("cookie name %q is not a valid token", runtimePathCookieName(testRuntimeID))
		}
	}
}

func TestLaunchModeFollowsTheRuntimeBaseDomain(t *testing.T) {
	if _, _, ok := (sessionGatewaySettings{Enabled: true, BaseDomain: "agents.company.local"}).hostMode(); !ok {
		t.Fatal("a configured base domain must keep using an origin per runtime")
	}
	for _, settings := range []sessionGatewaySettings{{}, {Enabled: true}, {Enabled: true, BaseDomain: "not a host"}, {BaseDomain: "agents.company.local"}} {
		if _, _, ok := settings.hostMode(); ok {
			t.Fatalf("host mode was claimed for %#v", settings)
		}
	}
}

// sessionCookieValue mints a valid path-mode session for one upstream endpoint.
func sessionCookieValue(t *testing.T, server *Server, endpoint string) string {
	t.Helper()
	access := runtimeAccess{RuntimeID: testRuntimeID, UserID: "user-1", Endpoint: endpoint, Token: "runtime-token", RuntimeType: "opencode", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	raw, _ := json.Marshal(access)
	value, err := server.cipher.Encrypt(raw, runtimePathSessionContext)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
