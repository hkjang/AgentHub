package tracking

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
		"momento site id too long": {Enabled: true, Provider: ProviderMomento, MomentoURL: "https://m.corp.example",
			MomentoSiteID: strings.Repeat("a", MaxProviderIDRunes+1)},
		"measurement id too long": {Enabled: true, Provider: ProviderGA4, MeasurementID: "G-" + strings.Repeat("1", MaxProviderIDRunes)},
		"matomo site id too long": {Enabled: true, Provider: ProviderMatomo, MatomoURL: "https://m.corp.example",
			MatomoSiteID: strings.Repeat("1", MaxProviderIDRunes+1)},
		"matomo url too long": {Enabled: true, Provider: ProviderMatomo, MatomoSiteID: "1",
			MatomoURL: "https://m.corp.example/" + strings.Repeat("p", MaxProviderURLRunes)},
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

// Provider URLs contribute only their origins to the policy. The whole URL
// may be long, and validation must neither trim it nor depend on its use today.
func TestProviderOriginsAreBoundedInEveryMode(t *testing.T) {
	for _, provider := range []struct{ name, value string }{
		{"Momento", ProviderMomento}, {"Matomo", ProviderMatomo},
	} {
		for _, mode := range []string{"selected", "disabled", "none", "other provider", "proxy"} {
			for _, fixture := range []struct {
				name  string
				url   string
				count int
			}{
				{"ASCII boundary", allowList(1, 300, 'a'), 300},
				{"ASCII overflow", allowList(1, 301, 'a'), 301},
				{"Korean boundary", allowList(1, 300, '가'), 300},
				{"Korean overflow", allowList(1, 301, '가'), 301},
				{"normalized boundary", "  " + allowList(1, 300, 'a') + "/  ", 300},
				{"long path and query", "https://collector.corp.example:8443/" + strings.Repeat("base/", 100) + "?key=value", 35},
			} {
				t.Run(provider.name+"/"+mode+"/"+fixture.name, func(t *testing.T) {
					settings := Settings{Enabled: true, Provider: provider.value,
						MomentoURL: "https://m.corp.example", MomentoSiteID: "x",
						MatomoURL: "https://m.corp.example", MatomoSiteID: "1"}
					if provider.value == ProviderMomento {
						settings.MomentoURL = fixture.url
					} else {
						settings.MatomoURL = fixture.url
					}
					switch mode {
					case "disabled":
						settings.Enabled = false
					case "none":
						settings.Provider = ProviderNone
					case "other provider":
						settings.Provider = ProviderMomento
						if provider.value == ProviderMomento {
							settings.Provider = ProviderMatomo
						}
					case "proxy":
						settings.Provider, settings.MomentoProxy = ProviderMomento, true
					}
					before := settings
					err := settings.Validate()
					if settings != before {
						t.Error("validation rewrote the settings")
					}
					if fixture.count <= 300 {
						if err != nil {
							t.Fatalf("an allowed provider origin was refused: %v", err)
						}
						return
					}
					if err == nil {
						t.Fatalf("%s origin of %d runes was accepted in %s mode", provider.name, fixture.count, mode)
					}
					for _, want := range []string{provider.name, "300자", "301자"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error does not identify the field and lengths (%s): %v", want, err)
						}
					}
					if strings.Contains(err.Error(), fixture.url) {
						t.Error("the error repeats the oversized URL")
					}
				})
			}
		}
	}
}

func TestProviderOriginLimitsLeaveIncompleteURLsToProviderValidation(t *testing.T) {
	for _, provider := range []string{ProviderMomento, ProviderMatomo} {
		for _, raw := range []string{"", "https://", strings.Repeat("a", 301), "https://[" + strings.Repeat("a", 301)} {
			for _, mode := range []string{"selected", "disabled", "unselected"} {
				t.Run(provider+"/"+mode+"/"+fmt.Sprint(len(raw)), func(t *testing.T) {
					settings := Settings{Enabled: mode != "disabled", Provider: provider,
						MomentoURL: raw, MomentoSiteID: "x", MatomoURL: raw, MatomoSiteID: "1"}
					if mode == "unselected" {
						settings.Provider = ProviderNone
					}
					if err := settings.Validate(); (err != nil) != (mode == "selected") {
						t.Fatalf("incomplete URL validation changed: %v", err)
					}
				})
			}
		}
	}
}

// providerAddress writes an address of exactly size runes whose origin is well
// inside MaxProviderOriginRunes, so a test can sit on either side of the limit
// on the whole address without the origin limit answering first. The filler is
// a parameter because the limit counts runes and the page carries bytes.
func providerAddress(size int, letter string) string {
	const prefix = "https://m.corp.example/"
	return prefix + strings.Repeat(letter, size-utf8.RuneCountInString(prefix))
}

// providerID writes a value of exactly size runes, which is all a site or
// measurement id has to be for the limit on it to be tested.
func providerID(size int, letter string) string {
	return strings.Repeat(letter, size)
}

// MaxProviderOriginRunes bounds the host of a provider address because the host
// is what reaches the policy header. What reaches the page is more than that:
// Snippet writes MomentoSiteID, MeasurementID and MatomoSiteID, and the whole of
// MomentoURL and MatomoURL with their paths and queries, into the markup
// injected into the body of every tracked page, and validation used to ask of
// those five only whether the ones the chosen provider needs are filled in. So
// each carries its own limit here, counted in runes because an id or an address
// can be written in Korean, refused rather than trimmed because a value an
// administrator reads back has to be the one they entered, and refused above the
// provider switch so that a value stored while the provider points elsewhere is
// not a value a later write only has to flip the provider to serve.
func TestWhatTheProviderBranchesRenderIsBoundedInEveryMode(t *testing.T) {
	for _, field := range []struct {
		name  string
		owner string
		limit int
		value func(size int, letter string) string
		set   func(*Settings, string)
	}{
		{"Momento 사이트 id", ProviderMomento, MaxProviderIDRunes, providerID,
			func(s *Settings, value string) { s.MomentoSiteID = value }},
		{"measurement id", ProviderGA4, MaxProviderIDRunes, providerID,
			func(s *Settings, value string) { s.MeasurementID = value }},
		{"Matomo 사이트 id", ProviderMatomo, MaxProviderIDRunes, providerID,
			func(s *Settings, value string) { s.MatomoSiteID = value }},
		{"Momento 주소", ProviderMomento, MaxProviderURLRunes, providerAddress,
			func(s *Settings, value string) { s.MomentoURL = value }},
		{"Matomo 주소", ProviderMatomo, MaxProviderURLRunes, providerAddress,
			func(s *Settings, value string) { s.MatomoURL = value }},
	} {
		for _, script := range []struct{ name, letter string }{{"ASCII", "a"}, {"Korean", "가"}} {
			for _, size := range []int{field.limit, field.limit + 1} {
				for _, mode := range []string{"selected", "disabled", "none", "other provider", "proxy"} {
					t.Run(fmt.Sprintf("%s/%s/%d/%s", field.name, script.name, size, mode), func(t *testing.T) {
						settings := Settings{Enabled: true, Provider: field.owner,
							MomentoURL: "https://m.corp.example", MomentoSiteID: "x", MomentoEnvironment: "prd",
							MeasurementID: "G-1",
							MatomoURL:     "https://m.corp.example", MatomoSiteID: "1"}
						value := field.value(size, script.letter)
						field.set(&settings, value)
						switch mode {
						case "disabled":
							settings.Enabled = false
						case "none":
							settings.Provider = ProviderNone
						case "other provider":
							settings.Provider = ProviderGA4
							if field.owner == ProviderGA4 {
								settings.Provider = ProviderMomento
							}
						case "proxy":
							settings.Provider, settings.MomentoProxy = ProviderMomento, true
						}
						before := settings
						err := settings.Validate()
						if settings != before {
							t.Error("validation rewrote the settings")
						}
						if size <= field.limit {
							if err != nil {
								t.Fatalf("a value on the limit was refused: %v", err)
							}
							return
						}
						if err == nil {
							t.Fatalf("%s of %d runes was accepted in %s mode", field.name, size, mode)
						}
						// The administrator has the form in front of them and no view of
						// the page the value is rendered into, so the message has to name
						// the field and both lengths.
						for _, want := range []string{field.name, fmt.Sprintf("%d자", field.limit), fmt.Sprintf("%d자", size)} {
							if !strings.Contains(err.Error(), want) {
								t.Errorf("the error does not say %q: %v", want, err)
							}
						}
						if strings.Contains(err.Error(), value) {
							t.Error("the error repeats the oversized value")
						}
					})
				}
			}
		}
	}
}

// What the limits accept is what every tracked page then carries, so the size of
// the largest markup they allow is measured here rather than reasoned about from
// the format strings. A pasted snippet is held to MaxSnippetBytes, and the point
// of the provider limits is that the markup the provider branches generate is
// held to the same discipline — which is an assertion about bytes, so it is made
// in each of the scripts an id and an address can be written in and with the
// letters html.EscapeString expands, since those are what turn runes into the
// bytes a page carries.
func TestTheLargestProviderSnippetTheLimitsAllowIsMeasured(t *testing.T) {
	for _, script := range []struct{ name, letter string }{
		{"ASCII", "a"},
		{"Korean", "가"},
		{"four-byte letters", "😀"},
		{"letters html escaping expands", "&"},
	} {
		for _, provider := range []string{ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo} {
			t.Run(script.name+"/"+provider, func(t *testing.T) {
				id := strings.Repeat(script.letter, MaxProviderIDRunes)
				address := providerAddress(MaxProviderURLRunes, script.letter)
				settings := Settings{Enabled: true, Provider: provider,
					// The collector is addressed directly, since the proxy renders
					// ProxyPath instead of the address and is therefore not the
					// largest shape. The environment keeps its own 32-byte limit.
					MomentoURL: address, MomentoSiteID: id, MomentoProxy: false,
					MomentoEnvironment: strings.Repeat("e", 32),
					MeasurementID:      id,
					MatomoURL:          address, MatomoSiteID: id}
				if err := settings.Validate(); err != nil {
					t.Fatalf("the largest configuration the limits describe is refused: %v", err)
				}
				rendered := settings.Snippet("")
				t.Logf("%s/%s: the largest accepted configuration renders %d bytes / %d runes of markup",
					script.name, provider, len(rendered), utf8.RuneCountInString(rendered))
				if len(rendered) > MaxSnippetBytes {
					t.Errorf("the largest accepted %s configuration in %s renders %d bytes into every page, past the %d a pasted snippet is held to",
						provider, script.name, len(rendered), MaxSnippetBytes)
				}
			})
		}
	}
}

// An allow-list entry is written into the policy header of every page exactly
// as it was typed, so an entry carrying a semicolon would end the directive it
// sits in and open one of its own choosing for the whole console. It is refused
// where it is typed rather than rewritten, because an entry that was already
// stored has to keep meaning what it meant.
func TestAnAllowedHostCannotOpenAPolicyDirectiveOfItsOwn(t *testing.T) {
	refused := map[string]string{
		"a semicolon opens a new directive": "https://a.corp.example/;script-src-elem",
		"a semicolon after a good entry":    "https://a.corp.example\nhttps://b.corp.example/;object-src",
		"a semicolon at the end":            "https://a.corp.example/x;",
		"longer than any host can be":       "https://" + strings.Repeat("a", MaxAllowedHostRunes) + ".corp.example",
		"long with nothing to break it up":  strings.Repeat("https://a.corp.example/", 20),
	}
	for name, hosts := range refused {
		err := (Settings{Provider: ProviderNone, AllowedHosts: hosts}).Validate()
		if err == nil {
			t.Errorf("%s was accepted: %q", name, hosts)
			continue
		}
		if !strings.HasPrefix(err.Error(), "허용 출처 ") {
			t.Errorf("%s: the message does not say which entry is wrong: %v", name, err)
		}
	}

	// What already worked keeps working: a wildcard host, a port, a trailing
	// slash, and the comma-and-newline mixture the console's textarea produces.
	accepted := map[string]string{
		"wildcard":         "https://*.corp.example",
		"port":             "https://b.corp.example:8443",
		"trailing slash":   "https://other.corp.example/",
		"mixed separators": "https://a.corp.example, https://b.corp.example:8443\nhttps://*.corp.example\r\nhttps://c.corp.example/",
		// The limit counts letters, not bytes: this is 223 runes and 623 bytes,
		// and a Korean address must not be refused for being written in Korean.
		"korean path": "https://a.corp.example/" + strings.Repeat("가", 200),
	}
	for name, hosts := range accepted {
		if err := (Settings{Provider: ProviderNone, AllowedHosts: hosts}).Validate(); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}

	// The limit is on one entry, not on the list: an administrator with many
	// origins is not asked to choose between them.
	var many []string
	for range 40 {
		many = append(many, "https://"+strings.Repeat("a", 60)+".corp.example")
	}
	if err := (Settings{Provider: ProviderNone, AllowedHosts: strings.Join(many, "\n")}).Validate(); err != nil {
		t.Errorf("a long list of short entries was refused: %v", err)
	}

	// And what the console offers to allow with one click still goes in. A
	// listed origin is originOf's output cut at the same number of runes, so the
	// button beside a report can never produce an entry this refuses — which
	// would be a refusal the administrator has no way to act on.
	recorder := NewRecorder()
	recorder.Record("https://"+strings.Repeat("b", 16*1024)+".example/collect;script-src", "connect-src", "/")
	recorder.Record("https://pixel.corp.example:8443/p.gif", "img-src", "/")
	listed := recorder.List(Settings{})
	if len(listed) != 2 {
		t.Fatalf("expected two reported origins, got %+v", listed)
	}
	for _, item := range listed {
		hosts := AddAllowedHost("https://a.corp.example", item.Origin)
		if err := (Settings{Provider: ProviderNone, AllowedHosts: hosts}).Validate(); err != nil {
			t.Errorf("allowing the reported origin %q is refused: %v", item.Origin, err)
		}
	}
}

// allowList writes count entries of exactly size runes each, so a test can sit
// on either side of a limit on purpose. The filler is ASCII unless a rune wider
// than a byte is asked for, which is how the same list can be produced short in
// runes and long in bytes.
func allowList(count, size int, filler rune) string {
	const prefix, suffix = "https://", ".corp.example"
	entries := make([]string, 0, count)
	for range count {
		entries = append(entries, prefix+strings.Repeat(string(filler), size-utf8.RuneCountInString(prefix)-utf8.RuneCountInString(suffix))+suffix)
	}
	return strings.Join(entries, "\n")
}

// The limit on one entry does not bound the allow list, and the list is what
// ends up in the header: every entry is written into three directives, so a list
// of n runes costs 3n in the header of every page. Without a limit on the list
// itself an administrator — or a settings document filled to the body limit —
// can produce a header no proxy or browser will carry, which takes the console
// down rather than the tracking. Both halves of the list are bounded here, its
// entry count and its total length, and both are refused rather than trimmed for
// the same reason one entry is: an origin read back has to be the one that was
// allowed.
func TestTheAllowListAsAWholeIsBoundedAndNotJustItsEntries(t *testing.T) {
	const size = MaxAllowedHostsTotalRunes / MaxAllowedHostEntries

	// The largest list there is: the entry count at its limit and the total
	// exactly on its limit. This one has to keep working, because a limit that
	// refuses what it says it allows is a limit nobody can plan around.
	full := allowList(MaxAllowedHostEntries, size, 'a')
	if err := (Settings{Provider: ProviderNone, AllowedHosts: full}).Validate(); err != nil {
		t.Fatalf("the largest list the limits describe is refused: %v", err)
	}

	refused := map[string]string{
		// 63 entries at the limit's width plus one a rune wider: the count is
		// within its limit, the total is one rune past it.
		"one rune past the total": allowList(MaxAllowedHostEntries-1, size, 'a') + "\n" + allowList(1, size+1, 'a'),
		// Narrower entries, so the total stays inside its limit and only the
		// count is past it.
		"one entry too many": allowList(MaxAllowedHostEntries+1, size-1, 'a'),
		// Written in Korean, one rune per entry past the width the total allows.
		// The refusal has to come from the runes, not from the bytes.
		"korean, one rune per entry past the total": allowList(MaxAllowedHostEntries, size+1, '가'),
	}
	for name, hosts := range refused {
		err := (Settings{Provider: ProviderNone, AllowedHosts: hosts}).Validate()
		if err == nil {
			t.Errorf("%s was accepted: %d entries, %d runes", name, len(splitHosts(hosts)), utf8.RuneCountInString(hosts))
			continue
		}
		if !strings.HasPrefix(err.Error(), "허용 출처 목록") {
			t.Errorf("%s: the message does not say the list is what is wrong: %v", name, err)
		}
	}

	// And the counting is in runes in the accepting direction too: this list is
	// on the total's limit in runes and nearly three times it in bytes.
	korean := allowList(MaxAllowedHostEntries, size, '가')
	if utf8.RuneCountInString(korean) >= len(korean) {
		t.Fatal("the korean list is not wider in bytes than in runes")
	}
	if err := (Settings{Provider: ProviderNone, AllowedHosts: korean}).Validate(); err != nil {
		t.Errorf("a list of korean addresses within the limit was refused: %v", err)
	}

	// The one-click button beside a report must not offer what the settings then
	// refuse, so a list already at the limit answers the click with the list
	// limit rather than with something about the origin.
	clicked := AddAllowedHost(full, "https://pixel.corp.example")
	err := (Settings{Provider: ProviderNone, AllowedHosts: clicked}).Validate()
	if err == nil {
		t.Error("one more click past a full list was accepted")
	} else if !strings.HasPrefix(err.Error(), "허용 출처 목록") {
		t.Errorf("a click past a full list blames the origin, not the list: %v", err)
	}
}

// snippetList writes count distinct origins of exactly size runes each into a
// snippet, separated by the space isURLBoundary reads as the end of an
// address, so a test can sit on either side of a limit on purpose. The origins
// have to differ because SnippetOrigins lists each one once, and the filler is
// ASCII unless a rune wider than a byte is asked for, which is how the same
// snippet can be produced short in runes and long in bytes.
func snippetList(count, size int, filler rune) string {
	const prefix, suffix = "https://", ".corp.example"
	entries := make([]string, 0, count)
	for index := range count {
		tag := fmt.Sprintf("%d", index)
		width := size - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix) - len(tag)
		entries = append(entries, prefix+tag+strings.Repeat(string(filler), width)+suffix)
	}
	return strings.Join(entries, " ")
}

// MaxSnippetBytes bounds the markup of a pasted snippet and nothing else, and
// it is the addresses written inside that markup which reach the header: the
// ProviderCustom branch of PolicySources hands each one to img-src, connect-src
// and script-src alike, exactly as an allow-list entry is handed to them. So
// the snippet's origins carry the same pair of limits the list carries, their
// count and their total length, and the pair is needed rather than either half
// — a snippet naming hundreds of short origins is stopped by the count, and a
// snippet naming one address eight thousand runes long is one entry and is
// stopped only by the total. Both refuse rather than trim, for the reason the
// list's do: a snippet read back has to be the one that was pasted.
func TestTheOriginsReadOutOfASnippetAreBoundedToo(t *testing.T) {
	const size = MaxSnippetOriginsTotalRunes / MaxSnippetOriginEntries

	// The largest snippet there is: the origin count at its limit and the total
	// exactly on its limit. This one has to keep working, because a limit that
	// refuses what it says it allows is a limit nobody can plan around.
	full := snippetList(MaxSnippetOriginEntries, size, 'a')
	if origins := SnippetOrigins(full); len(origins) != MaxSnippetOriginEntries {
		t.Fatalf("the largest snippet names %d origins, not %d", len(origins), MaxSnippetOriginEntries)
	}
	if err := (Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: full}).Validate(); err != nil {
		t.Fatalf("the largest snippet the limits describe is refused: %v", err)
	}

	// One address with no boundary character in it: a single entry, so the count
	// cannot object to it, and 8007 runes of it arrive three times over in the
	// header. This is the case the total exists for.
	long := "http://" + strings.Repeat("a", 8000)
	if origins := SnippetOrigins(long); len(origins) != 1 {
		t.Fatalf("the long address is read as %d origins, not one", len(origins))
	}
	if len(long) > MaxSnippetBytes {
		t.Fatalf("the long address is %d bytes, which MaxSnippetBytes already refuses", len(long))
	}

	refused := map[string]string{
		"one address longer than the whole total": long,
		// 31 entries at the limit's width plus one a rune wider: the count is
		// within its limit, the total is one rune past it.
		"one rune past the total": snippetList(MaxSnippetOriginEntries-1, size, 'a') + " " + snippetList(1, size+1, 'b'),
		// Narrower entries, so the total stays inside its limit and only the
		// count is past it.
		"one origin too many": snippetList(MaxSnippetOriginEntries+1, size-1, 'a'),
		// Written in Korean, one rune per origin past the width the total allows.
		// The refusal has to come from the runes, not from the bytes.
		"korean, one rune per origin past the total": snippetList(MaxSnippetOriginEntries, size+1, '가'),
	}
	for name, snippet := range refused {
		err := (Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: snippet}).Validate()
		if err == nil {
			t.Errorf("%s was accepted: %d origins, %d runes", name, len(SnippetOrigins(snippet)), snippetOriginRunes(snippet))
			continue
		}
		if !strings.HasPrefix(err.Error(), "추적 코드가 명명한 출처") {
			t.Errorf("%s: the message does not say the snippet's origins are what is wrong: %v", name, err)
		}
	}

	// And the counting is in runes in the accepting direction too: these origins
	// are on the total's limit in runes and nearly three times it in bytes.
	korean := snippetList(MaxSnippetOriginEntries, size, '가')
	if snippetOriginRunes(korean) >= len(korean) {
		t.Fatal("the korean snippet is not wider in bytes than in runes")
	}
	if err := (Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: korean}).Validate(); err != nil {
		t.Errorf("a snippet of korean addresses within the limit was refused: %v", err)
	}

	// The limits sit where MaxSnippetBytes sits, above the provider switch, for
	// the reason that one does: a snippet stored while tracking points somewhere
	// else is a snippet a later write only has to flip the provider to serve.
	if err := (Settings{Provider: ProviderNone, CustomSnippet: long}).Validate(); err == nil {
		t.Error("a snippet past the origin limits was stored because the provider was not custom")
	}

	// A real loader names a handful of addresses, and the message about the byte
	// limit on the markup is still the one an oversized paste gets.
	if err := (Settings{Enabled: true, Provider: ProviderCustom, CustomSnippet: `<script src="https://t.corp.example/t.js"></script>`}).Validate(); err != nil {
		t.Errorf("an ordinary loader was refused: %v", err)
	}
	err := (Settings{Provider: ProviderNone, CustomSnippet: strings.Repeat("x", MaxSnippetBytes+1)}).Validate()
	if err == nil || !strings.HasPrefix(err.Error(), "추적 코드는") {
		t.Errorf("the byte limit on the markup no longer reports itself: %v", err)
	}
}

// snippetOriginRunes is what the limit on the total counts, so a test can
// report the number the refusal is about.
func snippetOriginRunes(snippet string) int {
	total := 0
	for _, origin := range SnippetOrigins(snippet) {
		total += utf8.RuneCountInString(origin)
	}
	return total
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

// SingleHost is what the one-click "allow" route asks before it appends, and
// what it names in its audit row. Two things have to hold: a value carrying any
// separator splitHosts recognises is refused rather than silently stored as
// several entries, and an accepted value comes back as the entry the list will
// actually hold, so the row and the list say the same thing.
func TestSingleHostTakesOneEntryAndReturnsItAsStored(t *testing.T) {
	accepted := map[string]string{
		"an ordinary origin":      "https://a.corp.example",
		"with a port":             "https://a.corp.example:8443",
		"a trailing slash":        "https://a.corp.example/",
		"surrounded by space":     "  https://a.corp.example  ",
		"both at once":            "\n https://a.corp.example/ \t",
		"a wildcard host":         "https://*.corp.example",
		"written in korean":       "https://사내추적.example",
		"not an origin at all":    "nonsense",
		"keeps its own case":      "HTTPS://A.corp.example",
		"a path, not a separator": "https://a.corp.example/collect",
	}
	for name, raw := range accepted {
		host, single := SingleHost(raw)
		if !single {
			t.Errorf("%s: %q was refused", name, raw)
			continue
		}
		// The point of the helper: the caller may store this and report it as the
		// stored entry, so it must be what AddAllowedHost appends and what
		// splitHosts reads back out again.
		if appended := AddAllowedHost("", host); appended != host {
			t.Errorf("%s: AddAllowedHost stored %q, not the %q it was handed", name, appended, host)
		}
		if read := splitHosts(host); len(read) != 1 || read[0] != host {
			t.Errorf("%s: the list reads %q back as %q", name, host, read)
		}
	}
	if host, _ := SingleHost(" https://a.corp.example/ "); host != "https://a.corp.example" {
		t.Errorf("the space and the trailing slash are still on the entry: %q", host)
	}

	refused := map[string]string{
		"empty":                  "",
		"only space":             "   \n\t",
		"only a separator":       ",",
		"two, comma":             "https://a.corp.example,https://evil.corp.example",
		"two, space":             "https://a.corp.example https://evil.corp.example",
		"two, newline":           "https://a.corp.example\nhttps://evil.corp.example",
		"two, carriage return":   "https://a.corp.example\rhttps://evil.corp.example",
		"two, tab":               "https://a.corp.example\thttps://evil.corp.example",
		"two, comma and space":   "https://a.corp.example, https://evil.corp.example",
		"one entry and a stray":  "https://a.corp.example,x",
		"a whole pasted list":    allowList(3, 40, 'a'),
		"an origin and a policy": "https://a.corp.example https://evil.corp.example;script-src-elem",
	}
	for name, raw := range refused {
		if host, single := SingleHost(raw); single {
			t.Errorf("%s: %q was read as the single entry %q", name, raw, host)
		}
	}

	// And the settings form is untouched by any of this: splitHosts still reads a
	// pasted list as the several entries that route exists to store.
	if hosts := splitHosts("https://a.corp.example,https://b.corp.example"); len(hosts) != 2 {
		t.Errorf("a pasted list no longer reads as a list: %q", hosts)
	}

	// What the console actually offers to allow is a reported origin, so this must
	// not refuse one — a button that produces a refusal the administrator cannot
	// act on is worse than the button not being there. The exception is the only
	// separator a reported origin can carry: a comma survives url.Parse inside an
	// authority, where it is not a host any browser resolved, and appended as one
	// entry it would become two sources in the policy header. Refusing it is the
	// point.
	recorder := NewRecorder()
	recorder.Record("https://pixel.corp.example:8443/p.gif", "img-src", "/")
	recorder.Record("https://사내추적.example/collect?v=1", "connect-src", "/")
	recorder.Record("https://a,b.corp.example/p.gif", "img-src", "/")
	for _, item := range recorder.List(Settings{}) {
		host, single := SingleHost(item.Origin)
		if strings.ContainsRune(item.Origin, ',') {
			if single {
				t.Errorf("a reported authority with a comma in it was read as the single entry %q", host)
			}
			continue
		}
		if !single {
			t.Errorf("the one-click button offers %q, which this refuses", item.Origin)
		}
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

// A violation report arrives without a session, so the size of what the
// administration screen lists has to be decided here rather than by whatever
// posted the report.
func TestARecordedReportDoesNotChooseHowLongItsStringsAre(t *testing.T) {
	recorder := NewRecorder()
	huge := strings.Repeat("a", 16*1024)
	recorder.Record("https://"+huge+".example/collect", huge, "https://console.corp.example/"+huge)
	items := recorder.List(Settings{})
	if len(items) != 1 {
		t.Fatalf("expected one entry, got %+v", items)
	}
	for name, pair := range map[string]struct {
		value string
		limit int
	}{
		"origin":    {items[0].Origin, maxOriginRunes},
		"directive": {items[0].Directive, maxDirectiveRunes},
		"page":      {items[0].Page, maxPageRunes},
	} {
		if count := utf8.RuneCountInString(pair.value); count > pair.limit {
			t.Errorf("%s kept %d runes of what the report sent, over the %d allowed", name, count, pair.limit)
		}
		if !utf8.ValidString(pair.value) {
			t.Errorf("%s is not valid UTF-8 after being cut: %q", name, pair.value)
		}
	}
	// A cut origin is no longer the origin anybody allowed, so it stays blocked
	// rather than being marked as already permitted.
	if items := recorder.List(Settings{AllowedHosts: "https://" + huge + ".example"}); items[0].Allowed {
		t.Error("a cut origin was marked as allowed")
	}
}

// The page a report names can be a Korean address, and cutting it on a byte
// boundary would leave half of the last letter behind for the JSON response to
// turn into a replacement character.
func TestACutPageKeepsWholeLetters(t *testing.T) {
	recorder := NewRecorder()
	page := "https://console.corp.example/runs/" + strings.Repeat("가", 300)
	recorder.Record("https://collect.corp.example/v1/events", "connect-src", page)
	items := recorder.List(Settings{})
	if len(items) != 1 {
		t.Fatalf("expected one entry, got %+v", items)
	}
	if count := utf8.RuneCountInString(items[0].Page); count != maxPageRunes {
		t.Errorf("the page was cut to %d runes, not %d", count, maxPageRunes)
	}
	if !utf8.ValidString(items[0].Page) {
		t.Errorf("the page was cut inside a letter: %q", items[0].Page)
	}
	if want := page[:len("https://console.corp.example/runs/")+(maxPageRunes-len("https://console.corp.example/runs/"))*3]; items[0].Page != want {
		t.Errorf("page = %q", items[0].Page)
	}
}

// Cutting has to happen before the map key is built. Two reports the screen
// cannot tell apart — they differ only past the limit — have to be one entry
// with a count of two; keying on the full strings would list the same origin
// twice and neither row would ever count up.
func TestReportsThatDifferOnlyPastTheLimitAreOneEntry(t *testing.T) {
	recorder := NewRecorder()
	huge := strings.Repeat("b", 16*1024)
	recorder.Record("https://"+huge+".example/collect", "connect-src"+huge, "/")
	recorder.Record("https://"+huge+".other/collect", "connect-src"+huge+huge, "/")
	items := recorder.List(Settings{})
	if len(items) != 1 || items[0].Count != 2 {
		t.Fatalf("reports the screen cannot tell apart were not folded: %d entries %+v", len(items), items)
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
