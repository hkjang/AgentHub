// Package tracking injects a visitor tracking snippet into the console's pages.
//
// The console is served under a content security policy that allows scripts
// from its own origin only, so a tracking snippet cannot simply be pasted into
// the page: the browser refuses it and the administrator sees an empty
// dashboard with no explanation. This package produces both halves of what a
// snippet needs to run under that policy — the markup with a per-request nonce
// on every script tag, and the extra policy sources read out of the snippet —
// without ever loosening the policy with 'unsafe-inline', which would stay
// loose after tracking is switched off.
//
// Momento, the in-house collector, comes first because it is the only provider
// that keeps the data inside the building. Through the same-origin proxy under
// ProxyPath no external origin appears in the policy at all.
package tracking

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
)

// SettingKey is the system_settings row these settings live in.
const SettingKey = "tracking"

// Providers, in the order the console offers them.
const (
	ProviderNone    = "none"
	ProviderMomento = "momento"
	ProviderGA4     = "ga4"
	ProviderGTM     = "gtm"
	ProviderMatomo  = "matomo"
	ProviderCustom  = "custom"
)

// Providers lists every provider the settings accept.
var Providers = []string{ProviderNone, ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo, ProviderCustom}

// MaxSnippetBytes bounds a pasted snippet. A loader is a few hundred bytes; a
// snippet past this size is a page, not a tracker.
const MaxSnippetBytes = 8 * 1024

// ProxyPath is where the console forwards Momento's tracker and collector, so
// a browser talks to the collector through the console's own origin.
const ProxyPath = "/momento"

// ReportPath receives the browser's reports of what the policy blocked. It is
// only written into the policy while tracking is on.
const ReportPath = "/api/v1/tracking/csp-report"

// Settings is what an administrator configures. Field names are the console's
// keys; the settings-key sweep in internal/api checks that each one is read.
type Settings struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	// MomentoURL is the collector, for example https://momento.corp.example.
	MomentoURL    string `json:"momentoUrl"`
	MomentoSiteID string `json:"momentoSiteId"`
	// MomentoProxy sends the tracker and its events through ProxyPath on this
	// origin instead of straight to the collector. On by default: with it the
	// policy names no external origin, which is the whole difficulty.
	MomentoProxy bool `json:"momentoProxy"`
	// MomentoEnvironment is what the collector files the visits under.
	MomentoEnvironment string `json:"momentoEnvironment"`
	// MeasurementID is the GA4 or GTM id.
	MeasurementID string `json:"measurementId"`
	MatomoURL     string `json:"matomoUrl"`
	MatomoSiteID  string `json:"matomoSiteId"`
	// CustomSnippet is pasted markup, injected as written with a nonce added.
	CustomSnippet string `json:"customSnippet"`
	// AllowedHosts is where an administrator adds an origin the snippet did not
	// name, one per line or comma.
	AllowedHosts string `json:"allowedHosts"`
	// IncludeAdmin extends tracking to the administration screens. Off by
	// default: console traffic is rarely the visitor data anybody wants.
	IncludeAdmin bool `json:"includeAdmin"`
	// Placement is "head" or "body".
	Placement string `json:"placement"`
}

// Defaults are what an unconfigured deployment gets: nothing injected, nothing
// added to the policy.
func Defaults() Settings {
	return Settings{Provider: ProviderNone, MomentoProxy: true, MomentoEnvironment: "prd", Placement: "head"}
}

// Normalized trims what an administrator typed and fills the fields that have
// a fixed set of values.
func (s Settings) Normalized() Settings {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if s.Provider == "" {
		s.Provider = ProviderNone
	}
	s.MomentoURL = strings.TrimRight(strings.TrimSpace(s.MomentoURL), "/")
	s.MomentoSiteID = strings.TrimSpace(s.MomentoSiteID)
	s.MomentoEnvironment = strings.TrimSpace(s.MomentoEnvironment)
	if s.MomentoEnvironment == "" {
		s.MomentoEnvironment = "prd"
	}
	s.MeasurementID = strings.TrimSpace(s.MeasurementID)
	s.MatomoURL = strings.TrimRight(strings.TrimSpace(s.MatomoURL), "/")
	s.MatomoSiteID = strings.TrimSpace(s.MatomoSiteID)
	s.CustomSnippet = strings.TrimSpace(s.CustomSnippet)
	s.Placement = strings.ToLower(strings.TrimSpace(s.Placement))
	if s.Placement != "body" {
		s.Placement = "head"
	}
	return s
}

// Validate reports what is missing for the chosen provider. It is called on
// the way in, so a snippet that cannot work is refused at the form rather than
// discovered as an empty dashboard.
func (s Settings) Validate() error {
	s = s.Normalized()
	if !containsString(Providers, s.Provider) {
		return fmt.Errorf("추적 제공자는 %s 중 하나여야 합니다", strings.Join(Providers, ", "))
	}
	if len(s.CustomSnippet) > MaxSnippetBytes {
		return fmt.Errorf("추적 코드는 %d바이트를 넘을 수 없습니다", MaxSnippetBytes)
	}
	if len(s.MomentoEnvironment) > 32 || strings.ContainsAny(s.MomentoEnvironment, "\"'<>&") {
		return errors.New("Momento 환경 이름을 확인해 주세요 (32자 이하, 따옴표·꺾쇠 없이)")
	}
	for _, host := range splitHosts(s.AllowedHosts) {
		if originOf(host) == "" || !strings.HasPrefix(strings.ToLower(host), "http") {
			return fmt.Errorf("허용 출처 %q 는 https://호스트 형태여야 합니다", host)
		}
	}
	if !s.Enabled {
		return nil
	}
	switch s.Provider {
	case ProviderNone:
		return nil
	case ProviderMomento:
		if s.MomentoURL == "" || s.MomentoSiteID == "" {
			return errors.New("Momento 수집기 주소와 사이트 id 가 필요합니다")
		}
		if originOf(s.MomentoURL) == "" || !strings.HasPrefix(strings.ToLower(s.MomentoURL), "http") {
			return errors.New("Momento 수집기 주소는 http(s)://호스트 형태여야 합니다")
		}
	case ProviderGA4, ProviderGTM:
		if s.MeasurementID == "" {
			return errors.New("GA4 · GTM 에는 measurement id 가 필요합니다")
		}
	case ProviderMatomo:
		if s.MatomoURL == "" || s.MatomoSiteID == "" {
			return errors.New("Matomo 주소와 사이트 id 가 필요합니다")
		}
		if originOf(s.MatomoURL) == "" || !strings.HasPrefix(strings.ToLower(s.MatomoURL), "http") {
			return errors.New("Matomo 주소는 http(s)://호스트 형태여야 합니다")
		}
	case ProviderCustom:
		if s.CustomSnippet == "" {
			return errors.New("붙여 넣을 추적 코드가 비어 있습니다")
		}
	}
	return nil
}

// Active reports whether a page at this path should carry the snippet.
func (s Settings) Active(path string) bool {
	s = s.Normalized()
	if !s.Enabled || s.Provider == ProviderNone {
		return false
	}
	if !s.IncludeAdmin && (path == "/admin" || strings.HasPrefix(path, "/admin/")) {
		return false
	}
	return strings.TrimSpace(s.Snippet("")) != ""
}

// ProxyActive reports whether ProxyPath should forward to a Momento collector.
func (s Settings) ProxyActive() bool {
	s = s.Normalized()
	return s.Enabled && s.Provider == ProviderMomento && s.MomentoProxy && originOf(s.MomentoURL) != ""
}

// Snippet renders the markup to inject. The nonce goes on every script tag in
// it, which is what lets inline code run while the policy stays strict.
func (s Settings) Snippet(nonce string) string {
	s = s.Normalized()
	switch s.Provider {
	case ProviderMomento:
		if s.MomentoURL == "" || s.MomentoSiteID == "" {
			return ""
		}
		attributes := fmt.Sprintf(` data-site-id="%s" data-environment="%s" data-contract-version="1"`, html.EscapeString(s.MomentoSiteID), html.EscapeString(s.MomentoEnvironment))
		if s.MomentoProxy {
			return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-endpoint="%s"%s></script>`, ProxyPath, ProxyPath, attributes), nonce)
		}
		return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js"%s></script>`, html.EscapeString(s.MomentoURL), attributes), nonce)
	case ProviderGA4:
		if s.MeasurementID == "" {
			return ""
		}
		id := html.EscapeString(s.MeasurementID)
		return withNonce(fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','%s');</script>`, id, id), nonce)
	case ProviderGTM:
		if s.MeasurementID == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);})(window,document,'script','dataLayer','%s');</script>`, html.EscapeString(s.MeasurementID)), nonce)
	case ProviderMatomo:
		if s.MatomoURL == "" || s.MatomoSiteID == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>var _paq=window._paq=window._paq||[];_paq.push(['trackPageView']);_paq.push(['enableLinkTracking']);(function(){var u="%s/";_paq.push(['setTrackerUrl',u+'matomo.php']);_paq.push(['setSiteId','%s']);var d=document,g=d.createElement('script'),s=d.getElementsByTagName('script')[0];g.async=true;g.src=u+'matomo.js';s.parentNode.insertBefore(g,s);})();</script>`, html.EscapeString(s.MatomoURL), html.EscapeString(s.MatomoSiteID)), nonce)
	case ProviderCustom:
		return withNonce(s.CustomSnippet, nonce)
	}
	return ""
}

// withNonce adds the nonce to every script tag that does not already carry
// one. A pasted snippet is left otherwise as written.
func withNonce(snippet, nonce string) string {
	if nonce == "" || snippet == "" {
		return snippet
	}
	var builder strings.Builder
	remaining := snippet
	for {
		index := strings.Index(strings.ToLower(remaining), "<script")
		if index < 0 {
			builder.WriteString(remaining)
			return builder.String()
		}
		end := index + len("<script")
		builder.WriteString(remaining[:end])
		tag := remaining[end:]
		if closing := strings.Index(tag, ">"); closing >= 0 {
			tag = tag[:closing]
		}
		if !strings.Contains(strings.ToLower(tag), "nonce=") {
			builder.WriteString(` nonce="` + html.EscapeString(nonce) + `"`)
		}
		remaining = remaining[end:]
	}
}

// Sources are the origins a policy has to allow for the snippet to work, by
// the directive each one belongs to.
type Sources struct {
	Scripts  []string
	Connects []string
	Images   []string
}

// all lists every origin once, whichever directive it came in under.
func (s Sources) all() []string {
	seen := map[string]struct{}{}
	var origins []string
	for _, group := range [][]string{s.Scripts, s.Connects, s.Images} {
		for _, origin := range group {
			key := strings.ToLower(strings.TrimSuffix(origin, "/"))
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			origins = append(origins, origin)
		}
	}
	return origins
}

// PolicySources lists the origins the configured snippet needs: the
// provider's known addresses, every http(s) origin written into a pasted
// snippet, and whatever the administrator added by hand. Momento through the
// proxy needs none, which is the point of the proxy.
func (s Settings) PolicySources() Sources {
	s = s.Normalized()
	var sources Sources
	everywhere := func(origin string) {
		sources.Scripts = append(sources.Scripts, origin)
		sources.Connects = append(sources.Connects, origin)
		sources.Images = append(sources.Images, origin)
	}
	switch s.Provider {
	case ProviderMomento:
		if !s.MomentoProxy {
			if origin := originOf(s.MomentoURL); origin != "" {
				everywhere(origin)
			}
		}
	case ProviderGA4, ProviderGTM:
		sources.Scripts = append(sources.Scripts, "https://www.googletagmanager.com")
		sources.Connects = append(sources.Connects, "https://www.google-analytics.com", "https://analytics.google.com", "https://*.google-analytics.com")
		sources.Images = append(sources.Images, "https://www.google-analytics.com", "https://www.googletagmanager.com")
	case ProviderMatomo:
		if origin := originOf(s.MatomoURL); origin != "" {
			everywhere(origin)
		}
	case ProviderCustom:
		// A pasted snippet names the addresses it loads and reports to, so they
		// are allowed without anybody translating a policy error into a host name.
		for _, origin := range SnippetOrigins(s.CustomSnippet) {
			everywhere(origin)
		}
	}
	for _, host := range splitHosts(s.AllowedHosts) {
		everywhere(host)
	}
	return sources
}

// SnippetOrigins lists every http(s) origin written into a snippet: the script
// it loads, the endpoint it posts to, the pixel it requests.
func SnippetOrigins(snippet string) []string {
	origins := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for index := 0; index < len(snippet); {
		start := strings.Index(strings.ToLower(snippet[index:]), "http")
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(snippet) && !isURLBoundary(snippet[end]) {
			end++
		}
		index = end
		origin := originOf(snippet[start:end])
		if origin == "" || !strings.HasPrefix(strings.ToLower(origin), "http") {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

// isURLBoundary reports the characters that cannot appear in a URL written
// inside HTML or JavaScript, which is where each address ends.
func isURLBoundary(letter byte) bool {
	switch letter {
	case '"', '\'', '`', '<', '>', ' ', '\t', '\n', '\r', ')', ',', ';', '\\', '+':
		return true
	}
	return false
}

// originOf reduces an address to scheme://host[:port], which is what a policy
// source is.
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host
}

func splitHosts(list string) []string {
	var hosts []string
	for _, host := range strings.FieldsFunc(list, func(letter rune) bool {
		return letter == ',' || letter == ' ' || letter == '\n' || letter == '\r' || letter == '\t'
	}) {
		if trimmed := strings.TrimSuffix(strings.TrimSpace(host), "/"); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

// AddAllowedHost appends an origin to the allow list, leaving the existing
// entries and their order alone.
func AddAllowedHost(existing, origin string) string {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" {
		return existing
	}
	for _, host := range splitHosts(existing) {
		if strings.EqualFold(host, origin) {
			return existing
		}
	}
	if strings.TrimSpace(existing) == "" {
		return origin
	}
	return strings.TrimSpace(existing) + "\n" + origin
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
