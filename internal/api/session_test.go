package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
)

func TestRuntimeIDFromHost(t *testing.T) {
	settings := sessionGatewaySettings{Enabled: true, BaseDomain: "agents.company.local"}
	id, ok := runtimeIDFromHost("12345678-runtime.agents.company.local:443", settings)
	if !ok || id != "12345678-runtime" {
		t.Fatalf("unexpected match: id=%q ok=%v", id, ok)
	}
	for _, host := range []string{"agents.company.local", "nested.runtime.agents.company.local", "agents.company.local.example"} {
		if _, ok := runtimeIDFromHost(host, settings); ok {
			t.Fatalf("host %q must not match", host)
		}
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		input, host, port string
	}{
		{"agents.company.local", "agents.company.local", ""},
		{"localhost:8080", "localhost", "8080"},
	}
	for _, test := range tests {
		host, port, err := splitHostPort(test.input)
		if err != nil || host != test.host || port != test.port {
			t.Fatalf("splitHostPort(%q)=(%q,%q,%v)", test.input, host, port, err)
		}
	}
	for _, invalid := range []string{"", "https://agents.test", "bad_host", "localhost:99999", ".agents.test"} {
		if _, _, err := splitHostPort(invalid); err == nil {
			t.Fatalf("splitHostPort(%q) must fail", invalid)
		}
	}
}

func TestShouldTouchRuntime(t *testing.T) {
	if shouldTouchRuntime("/assets/app.js") || shouldTouchRuntime("/favicon.ico") {
		t.Fatal("static files must not update runtime activity")
	}
	if !shouldTouchRuntime("/session") || !shouldTouchRuntime("/api/chat") {
		t.Fatal("runtime API activity must update idle state")
	}
}

func TestRuntimeProxyInjectsRuntimeAuthAndStripsBrowserCookies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "opencode" || password != "runtime-token" {
			t.Errorf("unexpected upstream authorization: %q %q %v", username, password, ok)
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("browser cookie leaked upstream: %q", r.Header.Get("Cookie"))
		}
		if r.Header.Get("X-AgentHub-User") != "user-1" || r.URL.Path != "/global/health" {
			t.Errorf("unexpected proxy request: user=%q path=%q", r.Header.Get("X-AgentHub-User"), r.URL.Path)
		}
		w.Header().Set("Set-Cookie", "upstream=unsafe")
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	server := &Server{logger: slog.Default()}
	request := httptest.NewRequest(http.MethodGet, "http://agenthub.test/api/v1/runtimes/runtime-1/session/global/health", nil)
	request.Header.Set("Cookie", "agenthub_session=portal-secret")
	response := httptest.NewRecorder()
	server.serveRuntimeProxy(response, request, "runtime-1", "user-1", appRuntime.Connection{Endpoint: upstream.URL, Token: "runtime-token", RuntimeType: "opencode"}, "/global/health", "/api/v1/runtimes/runtime-1/session")

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/api/v1/runtimes/runtime-1/session/next" {
		t.Fatalf("unexpected proxy response: status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("upstream cookie leaked to browser: %q", response.Header().Get("Set-Cookie"))
	}
}
