package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

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

// The report address takes no session and no token, so a report that arrives
// with a host and a directive of its own choosing must not decide how much of
// the administration screen it fills, and a Korean address must come back out
// whole.
func TestAReportedViolationDoesNotChooseHowLongTheListedStringsAre(t *testing.T) {
	settings := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>`}
	server := trackingServer(t, settings)
	host := strings.Repeat("a", 4000)
	body := `{"csp-report":{"document-uri":"https://console.corp.example/runs/` + strings.Repeat("가", 1000) +
		`","blocked-uri":"https://` + host + `.example/v1/events","effective-directive":"connect-src` + strings.Repeat("x", 3000) + `"}}`
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, tracking.ReportPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/csp-report")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("the report was answered %d", recorder.Code)
		}
	}
	items := server.violations.List(settings)
	if len(items) != 1 || items[0].Count != 2 {
		t.Fatalf("the same oversized report was not folded: %d entries %+v", len(items), items)
	}
	for name, value := range map[string]string{"origin": items[0].Origin, "directive": items[0].Directive, "page": items[0].Page} {
		if count := utf8.RuneCountInString(value); count > 300 {
			t.Errorf("%s kept %d runes of what the report sent", name, count)
		}
		if !utf8.ValidString(value) {
			t.Errorf("%s is not valid UTF-8 after being cut: %q", name, value)
		}
	}
	if !strings.HasSuffix(items[0].Page, "가") {
		t.Errorf("the page was cut inside a letter: %q", items[0].Page)
	}
}

// carried is the budget for the policy header of a page: the eight kilobytes a
// reverse proxy commonly reserves for a response's headers, doubled. Two of the
// settings that reach the header are bounded against it and their costs add —
// the allow list, tripled at 3*(4096+64) = 12480, and the origins read out of a
// pasted snippet, tripled at 3*(1024+32) = 3168 — which with the base policy is
// 15921 of the header, measured, a little under 16 KiB. The two tests below
// measure that against this one number so the budget is stated in one place
// rather than per setting.
//
// Those 15921 are runes, though, and a proxy counts bytes. Both limits count
// runes, so the same worst case measures 15921 bytes with the addresses written
// in ASCII, 33513 with them in Korean and 42309 with letters that take four
// bytes each — all three measured, all three accepted by Validate. What the two
// limits hold under this number is therefore the runes they contribute and not
// the bytes, and the second test below measures every one of those scripts to
// say so: the rune count is the same for all of them, which is the bound, and
// the byte count is not.
//
// Provider origins now have their own 300-rune limit and add at most
// 3*(300+1) runes through these same directives. The provider regression below
// measures that separately. These remain rune limits, not a byte budget for
// the whole header, and new validation does not repair oversized settings that
// were already stored. The multibyte list and snippet cases remain accepted.
const carried = 16 * 1024

// pagePolicy writes every entry of the allow list into three directives, so
// the header grows by three runes for every rune of the list. A limit on one
// entry therefore says nothing about the size of the header: the header is as
// big as the list is long. This measures the header the largest accepted list
// produces, and shows that entries each inside the per-entry limit still build
// a header no proxy would carry unless the list itself is bounded.
func TestThePagePolicyHeaderIsBoundedByTheAllowListLimit(t *testing.T) {
	const nonce = "MDEyMzQ1Njc4OWFiY2RlZg=="
	list := func(count, size int) string {
		const prefix, suffix = "https://", ".corp.example"
		entries := make([]string, 0, count)
		for range count {
			entries = append(entries, prefix+strings.Repeat("a", size-len(prefix)-len(suffix))+suffix)
		}
		return strings.Join(entries, "\n")
	}

	base := len(pagePolicy(tracking.Settings{Provider: tracking.ProviderNone}, nonce))
	full := tracking.Settings{Provider: tracking.ProviderNone, AllowedHosts: list(tracking.MaxAllowedHostEntries, tracking.MaxAllowedHostsTotalRunes/tracking.MaxAllowedHostEntries)}
	if err := full.Validate(); err != nil {
		t.Fatalf("the largest list the limits describe is refused: %v", err)
	}
	header := pagePolicy(full, nonce)
	// Three directives, each entry preceded by the space that separates sources.
	if grown, want := len(header)-base, 3*(tracking.MaxAllowedHostsTotalRunes+tracking.MaxAllowedHostEntries); grown != want {
		t.Errorf("the largest accepted list grew the header by %d bytes, not the %d the limits account for", grown, want)
	}
	if len(header) > carried {
		t.Errorf("the largest accepted list builds a %d byte policy header, past the %d a proxy will carry", len(header), carried)
	}

	// Every entry of this one is inside MaxAllowedHostRunes and none of them
	// carries a semicolon, so nothing the per-entry checks look at objects to it.
	wide := tracking.Settings{Provider: tracking.ProviderNone, AllowedHosts: list(400, tracking.MaxAllowedHostRunes)}
	if err := wide.Validate(); err == nil {
		t.Errorf("a list of 400 entries of %d runes each is accepted, and the policy header it builds is %d bytes", tracking.MaxAllowedHostRunes, len(pagePolicy(wide, nonce)))
	}
}

// snippetOf writes count distinct origins of exactly size runes each, filled
// with letter and spaced by the character isURLBoundary reads as the end of an
// address, so a test can paste a snippet whose origins sit on either side of a
// limit on purpose. The origins have to differ because SnippetOrigins lists
// each one once. The filler is a parameter because the limits count runes and
// the budget is in bytes: the same size in runes is a different size in the
// header written in ASCII, in Korean, or in letters that take four bytes each,
// and a caller measuring the budget has to be able to ask for each of them.
func snippetOf(count, size int, letter string) string {
	const prefix, suffix = "https://", ".corp.example"
	entries := make([]string, 0, count)
	for index := range count {
		tag := fmt.Sprintf("%d", index)
		fill := size - utf8.RuneCountInString(prefix+suffix+tag)
		entries = append(entries, prefix+tag+strings.Repeat(letter, fill)+suffix)
	}
	return strings.Join(entries, " ")
}

// The allow list is not the only administrator-written value that becomes a
// response header: PolicySources writes every origin read out of a pasted
// snippet into the same three directives, and the only limit a snippet carried
// was MaxSnippetBytes — a count of bytes of markup, which says nothing about
// how many addresses are written in them. Filled with the shortest origins
// that parse, eight kilobytes of snippet name hundreds of them and the header
// leaves the budget far behind, so the snippet's origins need the same pair of
// limits the list has. This measures what an unbounded snippet costs, and then
// measures the worst case the two of them can build together in each of the
// scripts an address can be written in, which is as many runes of the header as
// these limits account for and, in anything but ASCII, more bytes of it than the
// budget holds — see carried for what these limits do and do not bound.
func TestThePagePolicyHeaderIsBoundedByTheSnippetOriginLimitAsWell(t *testing.T) {
	const nonce = "MDEyMzQ1Njc4OWFiY2RlZg=="

	// As many "http://aN.b" as fit inside the byte limit on a snippet: the
	// cheapest origin there is, so this is the largest number of them a stored
	// snippet can name.
	var filler strings.Builder
	for index := 1; ; index++ {
		entry := fmt.Sprintf("http://a%d.b ", index)
		if filler.Len()+len(entry) > tracking.MaxSnippetBytes {
			break
		}
		filler.WriteString(entry)
	}
	filled := tracking.Settings{Enabled: true, Provider: tracking.ProviderCustom, CustomSnippet: filler.String()}
	named := len(tracking.SnippetOrigins(filled.CustomSnippet))
	unbounded := len(pagePolicy(filled, nonce))
	t.Logf("a %d byte snippet names %d origins and builds a %d byte policy header", len(filled.CustomSnippet), named, unbounded)
	if unbounded <= carried {
		t.Fatalf("a snippet filled with %d origins builds a %d byte header, which is inside the %d byte budget — this test no longer measures anything", named, unbounded, carried)
	}
	if err := filled.Validate(); err == nil {
		t.Errorf("a snippet naming %d origins is accepted, and the policy header it builds is %d bytes", named, unbounded)
	}

	// The worst case these two limits bound: the largest accepted snippet and
	// the largest accepted list at once. Their costs add, because PolicySources
	// appends both to the same three directives.
	//
	// Both limits count runes, so the worst case has to be built once per script
	// to be measured at all. The rune count is what the limits hold and comes out
	// the same for every script, which is asserted exactly; the byte count is
	// what a proxy carries and does not, so only the ASCII case is inside the
	// budget and the others are measured to say how far outside they reach. The
	// multibyte cases are asserted to be outside it on purpose: bounding the
	// header in bytes would bring them in, and this is what then fails and asks
	// for carried's comment to be rewritten.
	const snippetSize = tracking.MaxSnippetOriginsTotalRunes / tracking.MaxSnippetOriginEntries
	const listSize = tracking.MaxAllowedHostsTotalRunes / tracking.MaxAllowedHostEntries
	const bounded = 3*(tracking.MaxAllowedHostsTotalRunes+tracking.MaxAllowedHostEntries) + 3*(tracking.MaxSnippetOriginsTotalRunes+tracking.MaxSnippetOriginEntries)
	base := utf8.RuneCountInString(pagePolicy(tracking.Settings{Provider: tracking.ProviderNone}, nonce))
	for _, script := range []struct {
		name   string
		letter string
		ascii  bool
	}{
		{name: "ASCII", letter: "a", ascii: true},
		{name: "Korean", letter: "가"},
		{name: "four-byte letters", letter: "😀"},
	} {
		both := tracking.Settings{
			Enabled:       true,
			Provider:      tracking.ProviderCustom,
			CustomSnippet: snippetOf(tracking.MaxSnippetOriginEntries, snippetSize, script.letter),
			AllowedHosts:  snippetOf(tracking.MaxAllowedHostEntries, listSize, script.letter),
		}
		if err := both.Validate(); err != nil {
			t.Errorf("the largest snippet and list the limits describe are refused together in %s: %v", script.name, err)
			continue
		}
		header := pagePolicy(both, nonce)
		runes := utf8.RuneCountInString(header)
		t.Logf("the largest accepted snippet and list in %s together build a policy header of %d bytes / %d runes", script.name, len(header), runes)
		// Three directives, each source preceded by the space that separates them.
		if grown := runes - base; grown != bounded {
			t.Errorf("the largest accepted snippet and list in %s grew the header by %d runes, not the %d the limits account for", script.name, grown, bounded)
		}
		if runes > carried {
			t.Errorf("the largest accepted snippet and list in %s build a policy header of %d runes, past the %d budgeted", script.name, runes, carried)
		}
		if script.ascii {
			if len(header) > carried {
				t.Errorf("the largest accepted snippet and list in %s build a %d byte policy header, past the %d a proxy will carry", script.name, len(header), carried)
			}
			continue
		}
		if len(header) <= carried {
			t.Errorf("the largest accepted snippet and list in %s build a %d byte policy header, inside the %d a proxy will carry — the limits now bound the header in bytes and carried says they do not", script.name, len(header), carried)
		}
	}
}

// Exercise the actual header builder and both validation entry points. This
// calls the API validation function, not an HTTP write or a database save.
func TestProviderOriginsCannotAmplifyThePagePolicyWithoutBound(t *testing.T) {
	const nonce = "MDEyMzQ1Njc4OWFiY2RlZg=="
	base := utf8.RuneCountInString(pagePolicy(tracking.Settings{Provider: tracking.ProviderNone}, nonce))
	server := &Server{}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, nil)
	for _, provider := range []string{tracking.ProviderMomento, tracking.ProviderMatomo} {
		for _, fixture := range []struct {
			name, origin string
			refused      bool
		}{
			{"oversized", "https://" + strings.Repeat("a", 8000) + ".corp.example", true},
			{"ASCII boundary", snippetOf(1, 300, "a"), false},
			{"Korean boundary", snippetOf(1, 300, "가"), false},
		} {
			t.Run(provider+"/"+fixture.name, func(t *testing.T) {
				value := map[string]any{"enabled": true, "provider": provider,
					provider + "Url": fixture.origin, provider + "SiteId": "1", "momentoProxy": false}
				settings, err := decodeTrackingSettings(value)
				if err != nil {
					t.Fatal(err)
				}
				header := pagePolicy(settings, nonce)
				grown := utf8.RuneCountInString(header) - base
				want := 3 * (300 + 1)
				if fixture.refused {
					want = 3 * (utf8.RuneCountInString(fixture.origin) + 1)
					if len(header) <= carried {
						t.Fatalf("the oversized origin no longer exceeds the header budget: %d bytes", len(header))
					}
				}
				if grown != want {
					t.Errorf("provider origin grew the header by %d runes, want %d", grown, want)
				}
				t.Logf("%s: header %d bytes / %d runes, growth %d runes", provider, len(header), utf8.RuneCountInString(header), grown)
				if err := settings.Validate(); (err != nil) != fixture.refused {
					t.Errorf("Validate refused=%t, want %t (header %d bytes): %v", err != nil, fixture.refused, len(header), err)
				}
				if err := server.validateSetting(request, tracking.SettingKey, value, nil); (err != nil) != fixture.refused {
					t.Errorf("API validation refused=%t, want %t (header %d bytes): %v", err != nil, fixture.refused, len(header), err)
				}
			})
		}
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
