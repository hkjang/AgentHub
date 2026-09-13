package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/tracking"
)

// Visitor tracking on the console's pages.
//
// The console ships under a policy that allows scripts from its own origin
// only. A tracking snippet pasted into it is refused by the browser, and the
// administrator is left with an empty dashboard and no reason. What follows is
// the other half of pasting a snippet: a nonce minted per page so the snippet's
// inline code runs without 'unsafe-inline', the snippet's own origins added to
// the policy for as long as tracking is on, and the browser's reports of what
// it still refused, kept where the administrator can read them.

// The page policy every document is served under. Tracking adds to it per
// request and never replaces it; with tracking off this is the whole policy.
const basePagePolicy = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// API responses are not pages. Nothing in them should ever be run, so their
// policy allows nothing.
const apiPolicy = "default-src 'none'; frame-ancestors 'none'"

// maxCSPReportBytes bounds a violation report; a browser's is under a kilobyte.
const maxCSPReportBytes = 16 * 1024

// isAPIPath reports the paths that answer with data rather than a page.
func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/") || path == "/mcp" || path == "/healthz" || path == "/readyz" || path == "/metrics"
}

// cachedTrackingSettings reads the tracking settings, remembering them for a
// few seconds: every page load asks, and a settings read per page would be
// the only database query on a path that otherwise serves a static file. A
// settings outage means "no tracking", never a broken page.
func (s *Server) cachedTrackingSettings(ctx context.Context) tracking.Settings {
	s.trackingMu.RLock()
	if time.Now().Before(s.trackingUntil) {
		value := s.trackingSettings
		s.trackingMu.RUnlock()
		return value
	}
	s.trackingMu.RUnlock()

	value := tracking.Defaults()
	if s.store != nil {
		if err := s.store.Setting(ctx, tracking.SettingKey, &value); err != nil {
			value = tracking.Defaults()
		}
	}
	value = value.Normalized()
	s.trackingMu.Lock()
	s.trackingSettings, s.trackingUntil = value, time.Now().Add(5*time.Second)
	s.trackingMu.Unlock()
	return value
}

func (s *Server) invalidateTrackingSettings() {
	s.trackingMu.Lock()
	s.trackingUntil = time.Time{}
	s.trackingMu.Unlock()
}

// decodeTrackingSettings re-reads a submitted document into its typed form,
// starting from the defaults so a key the console did not send keeps its
// default rather than becoming false or empty.
func decodeTrackingSettings(value map[string]any) (tracking.Settings, error) {
	settings := tracking.Defaults()
	raw, err := json.Marshal(value)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, errors.New("방문 추적 설정 형식을 확인해 주세요")
	}
	return settings, nil
}

// pagePolicy is the base policy plus what the snippet needs: the nonce, the
// snippet's origins, and — while tracking is on — an address for the browser
// to report what it refused to. Every addition is tied to the request or to
// the current settings, so switching tracking off returns the exact base.
func pagePolicy(settings tracking.Settings, nonce string) string {
	sources := settings.PolicySources()
	scripts := append([]string{"'self'", "'nonce-" + nonce + "'"}, sources.Scripts...)
	connects := append([]string{"'self'", "ws:", "wss:"}, sources.Connects...)
	images := append([]string{"'self'", "data:"}, sources.Images...)
	return "default-src 'self'; img-src " + strings.Join(images, " ") +
		"; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src " + strings.Join(connects, " ") +
		"; script-src " + strings.Join(scripts, " ") +
		"; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; report-uri " + tracking.ReportPath
}

// newNonce mints the per-request value that ties the injected script tags to
// the policy header of the same response.
func newNonce() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(raw[:])
}

// serveIndex serves the single-page shell for pagePath. With tracking off it
// is the file as built; with tracking on for this page the snippet is written
// in with a fresh nonce and the policy header names the same nonce.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, pagePath string) {
	settings := s.cachedTrackingSettings(r.Context())
	if !settings.Active(pagePath) {
		http.ServeFileFS(w, r, s.static, "index.html")
		return
	}
	page, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	nonce := newNonce()
	w.Header().Set("Content-Security-Policy", pagePolicy(settings, nonce))
	// A page carrying a nonce is good for exactly one response.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(injectSnippet(page, settings.Snippet(nonce), settings.Placement)))
}

// injectSnippet places the markup just before the closing tag it belongs to,
// falling back to the end of the document when the tag is missing.
func injectSnippet(page []byte, snippet, placement string) []byte {
	marker := "</head>"
	if placement == "body" {
		marker = "</body>"
	}
	text := string(page)
	index := strings.LastIndex(strings.ToLower(text), marker)
	if index < 0 {
		return []byte(text + "\n" + snippet + "\n")
	}
	return []byte(text[:index] + snippet + "\n" + text[index:])
}

// momentoTransport is the client side of the collector proxy. The default
// transport with a bound on how long a collector may keep a page waiting.
var momentoTransport = func() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	return transport
}()

// momentoProxy forwards ProxyPath to the configured Momento collector, so the
// tracker loads from and reports to the console's own origin and no external
// address has to appear in the policy. It is a relay to one administrator-set
// address only, and it carries none of the console's credentials: the session
// cookie is stripped before the request leaves.
func (s *Server) momentoProxy(w http.ResponseWriter, r *http.Request) {
	settings := s.cachedTrackingSettings(r.Context())
	if !settings.ProxyActive() {
		http.NotFound(w, r)
		return
	}
	target, err := url.Parse(settings.MomentoURL)
	if err != nil || target.Host == "" {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, tracking.ProxyPath)
	proxy := &httputil.ReverseProxy{
		Transport: momentoTransport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + rest
			request.Out.URL.RawPath = ""
			request.Out.Header.Del("Cookie")
			request.Out.Header.Del("Authorization")
			// The collector wants the visitor's address, not the console's.
			request.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logger.Warn("momento proxy failed", "path", r.URL.Path, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// cspReport is the shape a browser posts to report-uri.
type cspReport struct {
	Report struct {
		BlockedURI         string `json:"blocked-uri"`
		EffectiveDirective string `json:"effective-directive"`
		ViolatedDirective  string `json:"violated-directive"`
		DocumentURI        string `json:"document-uri"`
	} `json:"csp-report"`
}

// receiveCSPReport records what a browser refused. The address is only in the
// policy while tracking is on, and the recorder ignores anything else, so a
// deployment that never turned tracking on keeps nothing.
func (s *Server) receiveCSPReport(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	settings := s.cachedTrackingSettings(r.Context())
	if s.violations == nil || !settings.Enabled || settings.Provider == tracking.ProviderNone {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCSPReportBytes))
	if err != nil || len(body) == 0 {
		return
	}
	var report cspReport
	if json.Unmarshal(body, &report) != nil {
		return
	}
	directive := report.Report.EffectiveDirective
	if directive == "" {
		directive = report.Report.ViolatedDirective
	}
	s.violations.Record(report.Report.BlockedURI, directive, report.Report.DocumentURI)
}

// trackingViolations shows the administrator which addresses the policy is
// blocking, so a snippet can be fixed without reading a browser console.
func (s *Server) trackingViolations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.violations.List(s.cachedTrackingSettings(r.Context()))})
}

// clearTrackingViolations forgets the reports, which is how an administrator
// checks whether a change actually fixed the snippet.
func (s *Server) clearTrackingViolations(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	cleared := len(s.violations.List(s.cachedTrackingSettings(r.Context())))
	s.violations.Forget()
	s.store.Audit(r.Context(), &u, "tracking.violations_clear", "setting", tracking.SettingKey, "success", clientIP(r), map[string]any{"cleared": cleared})
	w.WriteHeader(http.StatusNoContent)
}

// allowTrackingOrigin adds one blocked origin to the allow list: the one-click
// fix for the reports listed above.
func (s *Server) allowTrackingOrigin(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Origin string `json:"origin"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	origin := strings.TrimSpace(input.Origin)
	if origin == "" || !strings.HasPrefix(strings.ToLower(origin), "http") {
		writeError(w, http.StatusBadRequest, "invalid_origin", "허용할 출처는 https://호스트 형태여야 합니다.")
		return
	}
	settings := tracking.Defaults()
	if err := s.store.Setting(r.Context(), tracking.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeStoreError(w, err)
		return
	}
	settings = settings.Normalized()
	settings.AllowedHosts = tracking.AddAllowedHost(settings.AllowedHosts, origin)
	if err := settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_setting", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), tracking.SettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.invalidateTrackingSettings()
	s.store.Audit(r.Context(), &u, "settings.update", "setting", tracking.SettingKey, "success", clientIP(r), map[string]any{"keys": []string{"allowedHosts"}, "origin": origin})
	writeJSON(w, http.StatusOK, map[string]any{"allowedHosts": settings.AllowedHosts})
}
