package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hkjang/AgentHub/internal/tracking"
)

const testShell = `<!doctype html>
<html lang="ko">
  <head>
    <meta charset="UTF-8" />
    <title>AgentHub</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/assets/index.js"></script>
  </body>
</html>
`

// trackingServer is a Server with the console shell in place and both settings
// caches filled, so pages can be requested without a database behind it.
func trackingServer(t *testing.T, settings tracking.Settings) *Server {
	t.Helper()
	server := &Server{logger: slog.Default(), violations: tracking.NewRecorder(), static: fstest.MapFS{
		"index.html":       {Data: []byte(testShell)},
		"assets/index.js":  {Data: []byte("console.log('hi')")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/style.css": {Data: []byte("body{}")},
	}}
	server.sessionSettings, server.sessionSettingsUntil = sessionGatewaySettings{Scheme: "https", SessionHours: 8}, time.Now().Add(time.Minute)
	server.trackingSettings, server.trackingUntil = settings.Normalized(), time.Now().Add(time.Minute)
	return server
}

func page(t *testing.T, server *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

var nonceInPolicy = regexp.MustCompile(`'nonce-([^']+)'`)

// A deployment that never configured tracking serves exactly what it served
// before: the built shell under the base policy, with no report address.
func TestWithTrackingOffNoPageChanges(t *testing.T) {
	server := trackingServer(t, tracking.Defaults())
	for _, path := range []string{"/", "/runs", "/admin/settings"} {
		response := page(t, server, path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s answered %d", path, response.Code)
		}
		if response.Body.String() != testShell {
			t.Errorf("%s: the shell was changed:\n%s", path, response.Body.String())
		}
		if policy := response.Header().Get("Content-Security-Policy"); policy != basePagePolicy {
			t.Errorf("%s: policy = %q", path, policy)
		}
	}
	if strings.Contains(basePagePolicy, "unsafe-inline'; script") || strings.Contains(basePagePolicy, "script-src") {
		t.Fatalf("the base policy names script-src; scripts are meant to fall to default-src 'self': %s", basePagePolicy)
	}
}

// With tracking on, the snippet is in the page at the configured place, every
// script tag in it carries a nonce, and the policy header of the same
// response names that nonce and the snippet's origins.
func TestTheSnippetAndThePolicyAgreeOnTheNonce(t *testing.T) {
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, Placement: "head",
		CustomSnippet: `<script src="https://t.corp.example/t.js"></script><script>fetch("https://collect.corp.example/v1")</script>`}
	server := trackingServer(t, settings)
	response := page(t, server, "/runs")
	body := response.Body.String()
	policy := response.Header().Get("Content-Security-Policy")
	match := nonceInPolicy.FindStringSubmatch(policy)
	if match == nil {
		t.Fatalf("the policy carries no nonce: %s", policy)
	}
	nonce := match[1]
	if got := strings.Count(body, `nonce="`+nonce+`"`); got != 2 {
		t.Errorf("expected the policy's nonce on 2 script tags, found %d:\n%s", got, body)
	}
	head := body[:strings.Index(body, "</head>")]
	if !strings.Contains(head, "t.corp.example") {
		t.Errorf("the snippet is not in the head:\n%s", body)
	}
	for _, want := range []string{
		"script-src 'self' 'nonce-" + nonce + "' https://t.corp.example https://collect.corp.example",
		"connect-src 'self' ws: wss: https://t.corp.example https://collect.corp.example",
		"img-src 'self' data: https://t.corp.example https://collect.corp.example",
		"report-uri " + tracking.ReportPath,
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy lacks %q:\n%s", want, policy)
		}
	}
	if strings.Contains(strings.SplitN(policy, "script-src", 2)[1], "unsafe-inline") {
		t.Errorf("script-src was loosened with unsafe-inline: %s", policy)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a page with a nonce is cacheable: %q", response.Header().Get("Cache-Control"))
	}
	// A second page gets its own nonce.
	if again := nonceInPolicy.FindStringSubmatch(page(t, server, "/runs").Header().Get("Content-Security-Policy")); again == nil || again[1] == nonce {
		t.Error("two responses shared a nonce")
	}

	// Placement body puts it before </body>.
	settings.Placement = "body"
	body = page(t, trackingServer(t, settings), "/").Body.String()
	if head := body[:strings.Index(body, "</head>")]; strings.Contains(head, "t.corp.example") {
		t.Errorf("placement body still injected into the head:\n%s", body)
	}
	if tail := body[strings.Index(body, "</head>"):strings.Index(body, "</body>")]; !strings.Contains(tail, "t.corp.example") {
		t.Errorf("placement body did not inject before </body>:\n%s", body)
	}
}

// Momento through the proxy: the page names this origin only, and the proxy
// forwards to the collector without the console's cookies.
func TestMomentoProxyForwardsWithoutCredentials(t *testing.T) {
	var seen *http.Request
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("window.momento=1"))
	}))
	defer collector.Close()
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderMomento, MomentoURL: collector.URL + "/base/", MomentoSiteID: "agenthub", MomentoProxy: true}
	server := trackingServer(t, settings)

	response := page(t, server, "/")
	policy := response.Header().Get("Content-Security-Policy")
	if strings.Contains(policy, "127.0.0.1") || strings.Contains(policy, collector.URL) {
		t.Errorf("the collector's origin is in the policy although the proxy is on: %s", policy)
	}
	if !strings.Contains(response.Body.String(), `src="/momento/tracker.js"`) {
		t.Errorf("the page does not load the tracker through the proxy:\n%s", response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/momento/tracker.js?v=1", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "secret"})
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "window.momento=1" {
		t.Fatalf("proxy answered %d %q", recorder.Code, recorder.Body.String())
	}
	if seen == nil || seen.URL.Path != "/base/tracker.js" || seen.URL.RawQuery != "v=1" {
		t.Fatalf("the collector saw %v", seen.URL)
	}
	if seen.Header.Get("Cookie") != "" || seen.Header.Get("Authorization") != "" {
		t.Errorf("the console's credentials reached the collector: %v", seen.Header)
	}
	if seen.Header.Get("X-Forwarded-For") == "" {
		t.Error("the visitor's address was not forwarded")
	}

	// Off, or direct, the path is nobody's.
	for name, off := range map[string]tracking.Settings{
		"disabled": tracking.Defaults(),
		"direct":   {Enabled: true, Provider: tracking.ProviderMomento, MomentoURL: collector.URL, MomentoSiteID: "x", MomentoProxy: false},
	} {
		if code := page(t, trackingServer(t, off), "/momento/tracker.js").Code; code != http.StatusNotFound {
			t.Errorf("%s: the proxy answered %d", name, code)
		}
	}
}

// Data endpoints are not pages: no snippet, and a policy that allows nothing.
func TestDataEndpointsGetTheNarrowPolicyAndNoSnippet(t *testing.T) {
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, IncludeAdmin: true, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>`}
	server := trackingServer(t, settings)
	for _, path := range []string{"/api/v1/version", "/healthz", "/api/openapi.json"} {
		response := page(t, server, path)
		if policy := response.Header().Get("Content-Security-Policy"); policy != apiPolicy {
			t.Errorf("%s: policy = %q", path, policy)
		}
		if strings.Contains(response.Body.String(), "t.corp.example") {
			t.Errorf("%s carries the snippet", path)
		}
	}
	// Static assets are served as built, under the base policy.
	response := page(t, server, "/assets/index.js")
	if response.Body.String() != "console.log('hi')" || response.Header().Get("Content-Security-Policy") != basePagePolicy {
		t.Errorf("an asset was changed: %q %q", response.Body.String(), response.Header().Get("Content-Security-Policy"))
	}
}

// The administration screens are excluded unless asked for, and an excluded
// page keeps the base policy: no nonce, no origins, no report address.
func TestAdministrationPagesKeepTheBasePolicyUnlessIncluded(t *testing.T) {
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>`}
	server := trackingServer(t, settings)
	response := page(t, server, "/admin/settings")
	if response.Body.String() != testShell || response.Header().Get("Content-Security-Policy") != basePagePolicy {
		t.Errorf("an administration page was tracked without includeAdmin:\n%s\n%s", response.Header().Get("Content-Security-Policy"), response.Body.String())
	}
	settings.IncludeAdmin = true
	response = page(t, trackingServer(t, settings), "/admin/settings")
	if !strings.Contains(response.Body.String(), "t.corp.example") || !strings.Contains(response.Header().Get("Content-Security-Policy"), "nonce-") {
		t.Error("an administration page was not tracked with includeAdmin")
	}
}

// What the browser refuses is recorded by origin and directive, once per
// origin, and only while tracking is on.
func TestPolicyViolationsAreRecordedByOrigin(t *testing.T) {
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>`}
	server := trackingServer(t, settings)
	report := func(server *Server) int {
		body := `{"csp-report":{"document-uri":"https://console.corp.example/runs","blocked-uri":"https://collect.corp.example/v1/events","effective-directive":"connect-src","violated-directive":"connect-src 'self'"}}`
		request := httptest.NewRequest(http.MethodPost, tracking.ReportPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/csp-report")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	for range 3 {
		if code := report(server); code != http.StatusNoContent {
			t.Fatalf("the report was answered %d", code)
		}
	}
	items := server.violations.List(settings)
	if len(items) != 1 || items[0].Origin != "https://collect.corp.example" || items[0].Directive != "connect-src" || items[0].Count != 3 || items[0].Allowed {
		t.Fatalf("recorded %+v", items)
	}
	if items[0].Page != "https://console.corp.example/runs" {
		t.Errorf("page = %q", items[0].Page)
	}
	settings.AllowedHosts = "https://collect.corp.example"
	if items := server.violations.List(settings); !items[0].Allowed {
		t.Error("an origin on the allow list is still reported as blocked")
	}

	off := trackingServer(t, tracking.Defaults())
	if code := report(off); code != http.StatusNoContent {
		t.Fatalf("with tracking off the report was answered %d", code)
	}
	if len(off.violations.List(tracking.Defaults())) != 0 {
		t.Error("a report was kept while tracking was off")
	}
}

// The setting is validated on the way in like every other one.
func TestTrackingSettingIsValidatedOnTheWayIn(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, nil)
	valid := map[string]any{"enabled": true, "provider": "momento", "momentoUrl": "https://momento.corp.example", "momentoSiteId": "agenthub", "momentoProxy": true, "placement": "head", "includeAdmin": false}
	if err := server.validateSetting(request, tracking.SettingKey, valid, nil); err != nil {
		t.Fatalf("a valid configuration was rejected: %v", err)
	}
	for name, value := range map[string]map[string]any{
		"oversized snippet": {"enabled": true, "provider": "custom", "customSnippet": strings.Repeat("x", tracking.MaxSnippetBytes+1)},
		"unknown provider":  {"enabled": true, "provider": "piwik"},
		"unknown field":     {"enabled": false, "unsafeInline": true},
		"wrong type":        {"enabled": "yes"},
	} {
		if err := server.validateSetting(request, tracking.SettingKey, value, nil); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A key the console did not send keeps its default rather than becoming
	// false: the proxy is the recommended shape and stays on unless turned off.
	decoded, err := decodeTrackingSettings(map[string]any{"enabled": true, "provider": "momento", "momentoUrl": "https://m.corp.example", "momentoSiteId": "x"})
	if err != nil || !decoded.MomentoProxy || decoded.Placement != "head" {
		t.Errorf("defaults were not kept for absent keys: %+v %v", decoded, err)
	}
}
