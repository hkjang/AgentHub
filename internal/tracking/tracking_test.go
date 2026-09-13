package tracking

import (
	"strings"
	"testing"
	"time"
)

// A fresh deployment has nothing switched on, and nothing in the policy.
func TestDefaultsInjectNothing(t *testing.T) {
	settings := Defaults()
	if settings.Enabled || settings.Active("/") || settings.ProxyActive() {
		t.Fatal("tracking is on by default")
	}
	if settings.Snippet("n") != "" {
		t.Fatalf("a default configuration renders a snippet: %q", settings.Snippet("n"))
	}
	sources := settings.PolicySources()
	if len(sources.Scripts)+len(sources.Connects)+len(sources.Images) != 0 {
		t.Fatalf("a default configuration adds policy sources: %+v", sources)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// Momento through the proxy is the recommended shape: the tracker loads from
// this origin and reports to this origin, so the policy names nobody else.
func TestMomentoThroughTheProxyNamesNoExternalOrigin(t *testing.T) {
	settings := Settings{Enabled: true, Provider: ProviderMomento, MomentoURL: "https://momento.corp.example/", MomentoSiteID: "agenthub", MomentoProxy: true, MomentoEnvironment: "prd"}
	snippet := settings.Snippet("abc")
	for _, want := range []string{`src="/momento/tracker.js"`, `data-endpoint="/momento"`, `data-site-id="agenthub"`, `data-environment="prd"`, `data-contract-version="1"`, `nonce="abc"`} {
		if !strings.Contains(snippet, want) {
			t.Errorf("snippet lacks %s:\n%s", want, snippet)
		}
	}
	if strings.Contains(snippet, "momento.corp.example") {
		t.Errorf("the collector's address leaked into the proxied snippet:\n%s", snippet)
	}
	if sources := settings.PolicySources(); len(sources.Scripts)+len(sources.Connects)+len(sources.Images) != 0 {
		t.Errorf("the proxy shape still adds policy sources: %+v", sources)
	}
	if !settings.ProxyActive() {
		t.Error("the proxy is not active")
	}

	// Sent straight to the collector, its origin has to be in every directive.
	settings.MomentoProxy = false
	snippet = settings.Snippet("abc")
	if !strings.Contains(snippet, `src="https://momento.corp.example/tracker.js"`) || strings.Contains(snippet, "data-endpoint") {
		t.Errorf("the direct snippet does not load from the collector:\n%s", snippet)
	}
	sources := settings.PolicySources()
	for name, group := range map[string][]string{"script": sources.Scripts, "connect": sources.Connects, "img": sources.Images} {
		if len(group) != 1 || group[0] != "https://momento.corp.example" {
			t.Errorf("%s-src should name the collector once, got %v", name, group)
		}
	}
	if settings.ProxyActive() {
		t.Error("the proxy is active for a direct configuration")
	}
}

// Every script tag gets the nonce, including the ones inside a pasted snippet,
// and a tag that already carries one is left alone.
func TestEveryScriptTagCarriesTheNonce(t *testing.T) {
	settings := Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>
<SCRIPT>window.__t=1</SCRIPT>
<script nonce="theirs">x()</script>`}
	snippet := settings.Snippet("ours")
	if got := strings.Count(snippet, `nonce="ours"`); got != 2 {
		t.Errorf("expected the nonce on 2 tags, found %d:\n%s", got, snippet)
	}
	if !strings.Contains(snippet, `<script nonce="theirs">`) {
		t.Errorf("a tag with its own nonce was rewritten:\n%s", snippet)
	}
	for _, provider := range []string{ProviderGA4, ProviderGTM, ProviderMatomo} {
		settings := Settings{Enabled: true, Provider: provider, MeasurementID: "G-1", MatomoURL: "https://matomo.corp.example", MatomoSiteID: "3"}
		snippet := settings.Snippet("ours")
		if tags, nonces := strings.Count(strings.ToLower(snippet), "<script"), strings.Count(snippet, `nonce="ours"`); tags == 0 || tags != nonces {
			t.Errorf("%s: %d script tags, %d nonces:\n%s", provider, tags, nonces, snippet)
		}
	}
}

// A pasted snippet names the addresses it loads and reports to; those are
// what the policy has to allow, and they are read out of the snippet rather
// than out of a browser console.
func TestOriginsAreReadOutOfAPastedSnippet(t *testing.T) {
	snippet := `<script src="https://momento.corp.example/tracker.js"></script>
<script>window.__t={endpoint:"https://momento.corp.example/collect/v1/events",pixel:'https://pixel.corp.example/p.gif?id=1'};
fetch("HTTP://insecure.corp.example:8080/x")</script>`
	origins := SnippetOrigins(snippet)
	want := []string{"https://momento.corp.example", "https://pixel.corp.example", "http://insecure.corp.example:8080"}
	if strings.Join(origins, " ") != strings.Join(want, " ") {
		t.Fatalf("origins = %v, want %v", origins, want)
	}
	settings := Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: snippet, AllowedHosts: "https://extra.corp.example, https://other.corp.example/\nhttps://extra.corp.example"}
	sources := settings.PolicySources()
	if strings.Join(sources.Scripts, " ") != strings.Join(append(want, "https://extra.corp.example", "https://other.corp.example", "https://extra.corp.example"), " ") {
		t.Fatalf("script sources = %v", sources.Scripts)
	}
}

func TestValidationRefusesWhatCannotWork(t *testing.T) {
	cases := map[string]Settings{
		"unknown provider":    {Enabled: true, Provider: "piwik"},
		"momento without id":  {Enabled: true, Provider: ProviderMomento, MomentoURL: "https://momento.corp.example"},
		"momento without url": {Enabled: true, Provider: ProviderMomento, MomentoSiteID: "x"},
		"momento bad url":     {Enabled: true, Provider: ProviderMomento, MomentoURL: "momento.corp.example", MomentoSiteID: "x"},
		"ga4 without id":      {Enabled: true, Provider: ProviderGA4},
		"matomo without url":  {Enabled: true, Provider: ProviderMatomo, MatomoSiteID: "1"},
		"custom empty":        {Enabled: true, Provider: ProviderCustom},
		"custom too large":    {Enabled: true, Provider: ProviderCustom, CustomSnippet: "<script>" + strings.Repeat("x", MaxSnippetBytes) + "</script>"},
		"bad allowed host":    {Provider: ProviderNone, AllowedHosts: "momento.corp.example"},
		"environment markup":  {Enabled: true, Provider: ProviderMomento, MomentoURL: "https://m.corp.example", MomentoSiteID: "x", MomentoEnvironment: `"><script>`},
	}
	for name, settings := range cases {
		if err := settings.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// The size limit applies whether or not the snippet is in use: a stored
	// page-sized blob is a page-sized blob.
	oversized := Settings{Provider: ProviderNone, CustomSnippet: strings.Repeat("x", MaxSnippetBytes+1)}
	if err := oversized.Validate(); err == nil {
		t.Error("an oversized snippet was stored because tracking was off")
	}
	// And a switched-off configuration with half-filled fields is fine — the
	// administrator may be filling the form in before turning it on.
	if err := (Settings{Provider: ProviderMomento, MomentoURL: "https://m.corp.example"}).Validate(); err != nil {
		t.Errorf("a switched-off configuration was refused: %v", err)
	}
	if err := (Settings{Enabled: true, Provider: ProviderMomento, MomentoURL: "https://m.corp.example", MomentoSiteID: "x", AllowedHosts: "https://a.corp.example\nhttps://b.corp.example:8443"}).Validate(); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

// The administration screens are left alone unless asked for.
func TestAdministrationPagesAreExcludedUnlessAsked(t *testing.T) {
	settings := Settings{Enabled: true, Provider: ProviderMomento, MomentoURL: "https://m.corp.example", MomentoSiteID: "x"}
	if !settings.Active("/") || !settings.Active("/runs") || !settings.Active("/administration-of-things") {
		t.Error("a user page is not tracked")
	}
	if settings.Active("/admin/settings") || settings.Active("/admin") {
		t.Error("an administration page is tracked without includeAdmin")
	}
	settings.IncludeAdmin = true
	if !settings.Active("/admin/settings") {
		t.Error("an administration page is not tracked with includeAdmin")
	}
	settings.Enabled = false
	if settings.Active("/") {
		t.Error("a switched-off configuration is active")
	}
}

func TestAddAllowedHostKeepsTheListAsWritten(t *testing.T) {
	list := AddAllowedHost("", "https://a.corp.example/")
	list = AddAllowedHost(list, "https://b.corp.example")
	list = AddAllowedHost(list, "HTTPS://A.corp.example")
	if list != "https://a.corp.example\nhttps://b.corp.example" {
		t.Fatalf("list = %q", list)
	}
}

// The recorder keeps distinct origins, not a count of page views, and says
// which of them the current settings already allow.
func TestRecorderKeepsDistinctOriginsAndMarksAllowedOnes(t *testing.T) {
	recorder := NewRecorder()
	moment := time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { return moment }
	for range 5 {
		recorder.Record("https://momento.corp.example/collect/v1/events", "connect-src", "https://console.corp.example/runs")
	}
	recorder.Record("chrome-extension://abc/inject.js", "script-src", "/")
	recorder.Record("data", "img-src", "/")
	moment = moment.Add(time.Minute)
	recorder.Record("https://pixel.corp.example/p.gif", "img-src 'self' data:", "/")
	items := recorder.List(Settings{})
	if len(items) != 2 {
		t.Fatalf("expected 2 distinct origins, got %+v", items)
	}
	if items[0].Origin != "https://pixel.corp.example" || items[0].Directive != "img-src" {
		t.Errorf("most recent first: %+v", items[0])
	}
	if items[1].Origin != "https://momento.corp.example" || items[1].Count != 5 || items[1].Allowed {
		t.Errorf("repeat reports were not folded: %+v", items[1])
	}
	items = recorder.List(Settings{AllowedHosts: "https://momento.corp.example"})
	if !items[1].Allowed || items[0].Allowed {
		t.Errorf("allowed marking is wrong: %+v", items)
	}
	items = recorder.List(Settings{Provider: ProviderGA4, MeasurementID: "G-1", AllowedHosts: "https://*.corp.example"})
	if !items[0].Allowed || !items[1].Allowed {
		t.Errorf("a wildcard entry did not match: %+v", items)
	}
	recorder.Forget()
	if len(recorder.List(Settings{})) != 0 {
		t.Error("Forget kept something")
	}
}

func TestRecorderIsBounded(t *testing.T) {
	recorder := NewRecorder()
	moment := time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { moment = moment.Add(time.Second); return moment }
	for index := range MaxViolations + 10 {
		recorder.Record("https://host-"+strings.Repeat("x", index%50)+".example:"+itoa(index)+"/x", "connect-src", "/")
	}
	items := recorder.List(Settings{})
	if len(items) != MaxViolations {
		t.Fatalf("expected %d entries, got %d", MaxViolations, len(items))
	}
	for _, item := range items {
		if strings.HasSuffix(item.Origin, ":0") || strings.HasSuffix(item.Origin, ":9") {
			t.Fatalf("the oldest entries were kept: %s", item.Origin)
		}
	}
}

func itoa(value int) string {
	digits := "0123456789"
	if value == 0 {
		return "0"
	}
	var out []byte
	for value > 0 {
		out = append([]byte{digits[value%10]}, out...)
		value /= 10
	}
	return string(out)
}
