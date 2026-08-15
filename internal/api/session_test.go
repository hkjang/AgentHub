package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestInjectRuntimeBrandingPlacesBarInsideBodyOnce(t *testing.T) {
	branded := injectRuntimeBranding(`<!doctype html><html><head><title>Qwen Paw</title></head><body class="app"><div id="root"></div></body></html>`)
	if !strings.Contains(branded, "agenthub-runtime-topbar") {
		t.Fatal("branding bar was not injected")
	}
	if strings.Index(branded, "agenthub-runtime-branding") > strings.Index(branded, "</head>") {
		t.Fatal("branding stylesheet must land inside <head>")
	}
	barIndex, bodyIndex := strings.Index(branded, `<div class="agenthub-runtime-topbar"`), strings.Index(branded, `<body class="app">`)
	if barIndex < bodyIndex || !strings.Contains(branded, `<div id="root">`) {
		t.Fatalf("branding bar is misplaced or clobbered the app root: %s", branded)
	}
	if again := injectRuntimeBranding(branded); again != branded {
		t.Fatal("branding must be idempotent")
	}
}

func TestBrandRuntimeResponseSkipsCompressedAndNonHTMLBodies(t *testing.T) {
	server := &Server{logger: slog.Default()}
	for name, response := range map[string]*http.Response{
		"compressed": {Header: http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(strings.NewReader("<html><body>x</body></html>"))},
		"json":       {Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))},
	} {
		if err := server.brandRuntimeResponse(response, "qwenpaw"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, _ := io.ReadAll(response.Body)
		if strings.Contains(string(body), "agenthub-runtime-topbar") {
			t.Fatalf("%s body must not be rewritten: %s", name, body)
		}
	}
}

func TestBrandRuntimeResponseRewritesQwenPawDocument(t *testing.T) {
	server := &Server{logger: slog.Default()}
	response := &http.Response{Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("<html><head></head><body></body></html>"))}
	if err := server.brandRuntimeResponse(response, "qwenpaw"); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "agenthub-runtime-topbar") {
		t.Fatalf("QwenPaw document was not branded: %s", body)
	}
	if response.Header.Get("Content-Length") != strconv.Itoa(len(body)) || response.ContentLength != int64(len(body)) {
		t.Fatalf("Content-Length %q does not match the rewritten body (%d bytes)", response.Header.Get("Content-Length"), len(body))
	}
	if response.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("inline branding requires the upstream CSP to be dropped")
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
