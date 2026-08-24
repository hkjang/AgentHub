package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

const runtimeAccessCookie = "agenthub_runtime_access"

type sessionGatewaySettings struct {
	Enabled      bool   `json:"enabled"`
	Scheme       string `json:"scheme"`
	BaseDomain   string `json:"baseDomain"`
	SessionHours int    `json:"sessionHours"`
}

// hostMode reports whether runtimes get an origin of their own. Without a usable
// Runtime Base Domain the platform falls back to serving them from the Portal's
// own origin under /{runtimeId}/, so a deployment with no wildcard DNS can still
// open a workspace.
func (v sessionGatewaySettings) hostMode() (hostname, port string, ok bool) {
	if !v.Enabled {
		return "", "", false
	}
	hostname, port, err := splitHostPort(v.BaseDomain)
	if err != nil {
		return "", "", false
	}
	return hostname, port, true
}

// sessionLifetime bounds how long a runtime browser session lasts.
func (v sessionGatewaySettings) sessionLifetime() time.Duration {
	hours := v.SessionHours
	if hours < 1 || hours > 24 {
		hours = 8
	}
	return time.Duration(hours) * time.Hour
}

type runtimeAccess struct {
	RuntimeID   string    `json:"runtimeId"`
	UserID      string    `json:"userId"`
	Endpoint    string    `json:"endpoint"`
	Token       string    `json:"token"`
	RuntimeType string    `json:"runtimeType"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// runtimeSession is the authenticated API data-plane boundary. Native browser
// UIs use runtimeHostGateway because OpenCode addresses assets and APIs from
// the origin root rather than from a configurable subpath.
func (s *Server) runtimeSession(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	runtimeID := chi.URLParam(r, "id")
	connection, err := s.runtimeConnection(r, runtimeID, user.ID, user.Role == "admin")
	if err != nil {
		writeRuntimeConnectionError(w, err)
		return
	}
	prefix := "/api/v1/runtimes/" + runtimeID + "/session"
	proxiedPath := "/" + strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	s.serveRuntimeProxy(w, r, runtimeID, user.ID, connection, proxiedPath, prefix)
}

func (s *Server) launchRuntime(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	runtimeID := chi.URLParam(r, "id")
	instance, err := s.store.RuntimeByID(r.Context(), runtimeID, user.ID, user.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := strings.ToLower(instance.Status)
	if instance.DesiredState != "running" || (status != "running" && status != "ready") {
		writeError(w, http.StatusConflict, "runtime_not_ready", "Runtime이 Ready 상태일 때 브라우저 세션을 열 수 있습니다.")
		return
	}
	settings := s.cachedSessionGatewaySettings(r.Context())
	hostname, hostPort, hostMode := settings.hostMode()
	// Some runtimes address their assets and their API from the root of whatever
	// origin they were loaded from and have no base-path setting — Langflow is
	// one. Serving those from the Portal under /{runtimeId}/ produces a blank
	// page, so the session is refused with the reason instead.
	if !hostMode {
		if agent, agentErr := s.store.AgentByID(r.Context(), instance.AgentID, user.ID, user.Role == "admin"); agentErr == nil && runtimetype.HostSessionOnly(agent.RuntimeType) {
			writeError(w, http.StatusConflict, "runtime_base_domain_required",
				runtimetype.Describe(agent.RuntimeType).Label+" 런타임은 하위 경로로 서비스할 수 없어 Runtime 전용 도메인이 필요합니다. Admin ▸ 설정 ▸ Runtime 세션에서 Runtime Base Domain을 설정한 뒤 다시 열어 주세요.")
			return
		}
	}
	ticket, expires, err := s.store.CreateRuntimeLaunchTicket(r.Context(), runtimeID, instance.OwnerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	query := url.Values{"ticket": []string{ticket}}.Encode()
	// With a Runtime Base Domain each runtime gets an origin of its own, which is
	// what keeps a runtime UI out of the Portal's origin. Without one the session
	// is served from the Portal itself under /{runtimeId}/ — a relative URL, so it
	// works on whatever hostname the user already reached the Portal on.
	// A runtime started with its base path set to its own id has to be opened
	// there in both modes, or its own links point at a prefix nothing serves.
	launchPath := "/"
	if agent, agentErr := s.store.AgentByID(r.Context(), instance.AgentID, user.ID, user.Role == "admin"); agentErr == nil && runtimetype.ServesUnderRuntimePath(agent.RuntimeType) {
		launchPath = "/" + runtimeID + "/"
	}
	mode, launch := "path", "/"+runtimeID+"/?"+query
	if hostMode {
		host := runtimeID + "." + hostname
		if hostPort != "" {
			host = net.JoinHostPort(host, hostPort)
		}
		mode = "host"
		launch = (&url.URL{Scheme: settings.Scheme, Host: host, Path: launchPath, RawQuery: query}).String()
	}
	s.store.TouchRuntime(r.Context(), runtimeID)
	s.store.TouchRuntimeSessions(r.Context(), runtimeID)
	// Opening the workspace is a takeover: the warm pool must not stop a runtime
	// somebody has just started working in.
	s.releaseWarmClaim(r.Context(), instance)
	s.store.Audit(r.Context(), &user, "runtime.launch", "runtime", runtimeID, "success", clientIP(r), map[string]any{"mode": mode})
	writeJSON(w, http.StatusCreated, map[string]any{"url": launch, "expiresAt": expires, "mode": mode})
}

func (s *Server) runtimeHostGateway(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings := s.cachedSessionGatewaySettings(r.Context())
		runtimeID, ok := runtimeIDFromHost(r.Host, settings)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		if ticket := r.URL.Query().Get("ticket"); ticket != "" {
			ticketRuntimeID, userID, err := s.store.ConsumeRuntimeLaunchTicket(r.Context(), ticket)
			if err != nil || ticketRuntimeID != runtimeID {
				// A spent ticket on a request that also carries a valid session is a
				// stale URL, not an intrusion: fall through and serve the session.
				if _, valid := s.hostRuntimeAccess(r, runtimeID); valid {
					http.Redirect(w, r, ticketFreeLocation(r.URL), http.StatusSeeOther)
					return
				}
				s.logger.Warn("invalid launch ticket", "runtime", runtimeID, "error", err)
				runtimeUnauthorized(w, "Launch ticket이 만료되었거나 이미 사용되었습니다.")
				return
			}
			connection, err := s.runtimeConnection(r, runtimeID, userID, false)
			if err != nil {
				writeRuntimeConnectionError(w, err)
				return
			}
			access := runtimeAccess{RuntimeID: runtimeID, UserID: userID, Endpoint: connection.Endpoint, Token: connection.Token, RuntimeType: connection.RuntimeType, ExpiresAt: time.Now().UTC().Add(settings.sessionLifetime())}
			raw, _ := json.Marshal(access)
			encrypted, err := s.cipher.Encrypt(raw, "runtime-host-session")
			if err != nil {
				writeError(w, http.StatusInternalServerError, "runtime_session_failed", "Runtime 세션을 만들지 못했습니다.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: runtimeAccessCookie, Value: encrypted, Path: "/", HttpOnly: true, Secure: settings.Scheme == "https", SameSite: http.SameSiteLaxMode, Expires: access.ExpiresAt, MaxAge: int(time.Until(access.ExpiresAt).Seconds())})
			// Redirect rather than proxy, so the one-time ticket leaves the address
			// bar. A runtime UI builds its own URLs from the page's location: ttyd
			// opens its websocket with the page's query string attached, which sent
			// the spent ticket back here and was refused — a terminal that renders
			// and never connects. The path gateway has always redirected for the
			// same reason; this one proxied straight through.
			http.Redirect(w, r, ticketFreeLocation(r.URL), http.StatusSeeOther)
			return
		}

		if access, valid := s.hostRuntimeAccess(r, runtimeID); valid {
			connection := appRuntime.Connection{Endpoint: access.Endpoint, Token: access.Token, RuntimeType: access.RuntimeType}
			if shouldTouchRuntime(r.URL.Path) {
				s.store.TouchRuntime(r.Context(), runtimeID)
				s.store.TouchRuntimeSessions(r.Context(), runtimeID)
			}
			s.serveRuntimeProxy(w, r, runtimeID, access.UserID, connection, r.URL.Path, "")
			return
		}

		// No usable ticket and no valid session cookie: the request is
		// unauthenticated.
		// There is deliberately no direct-proxy fallback here — the runtime
		// subdomain is publicly resolvable, so serving it without a ticket would
		// hand anyone who guesses a runtime ID a full workspace session.
		s.logger.Warn("runtime access unauthorized", "runtime", runtimeID, "host", r.Host)
		runtimeUnauthorized(w, "AgentHub에서 Runtime을 다시 열어 주세요.")
	})
}

// forwardCookies rewrites the Cookie header to drop only AgentHub's own cookies.
func forwardCookies(request *http.Request) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		// Prefix, not equality: the path gateway names its cookie per runtime
		// (agenthub_runtime_access_<id>), and a leak of that one hands over a
		// whole workspace session.
		if strings.HasPrefix(cookie.Name, agentHubCookiePrefix) {
			continue
		}
		request.AddCookie(cookie)
	}
}

// hostRuntimeAccess reads the session cookie a runtime origin carries.
func (s *Server) hostRuntimeAccess(r *http.Request, runtimeID string) (runtimeAccess, bool) {
	cookie, err := r.Cookie(runtimeAccessCookie)
	if err != nil {
		return runtimeAccess{}, false
	}
	plain, decryptErr := s.cipher.Decrypt(cookie.Value, "runtime-host-session")
	var access runtimeAccess
	if decryptErr != nil || json.Unmarshal(plain, &access) != nil {
		return runtimeAccess{}, false
	}
	if access.RuntimeID != runtimeID || !access.ExpiresAt.After(time.Now()) {
		return runtimeAccess{}, false
	}
	return access, true
}

// ticketFreeLocation is the same URL with the launch ticket removed.
func ticketFreeLocation(original *url.URL) string {
	stripped := *original
	query := stripped.Query()
	query.Del("ticket")
	stripped.RawQuery = query.Encode()
	// Relative, so the browser stays on the runtime's own origin.
	stripped.Scheme, stripped.Host = "", ""
	if stripped.Path == "" {
		stripped.Path = "/"
	}
	return stripped.String()
}

func (s *Server) runtimeConnection(r *http.Request, runtimeID, userID string, admin bool) (appRuntime.Connection, error) {
	instance, err := s.store.RuntimeByID(r.Context(), runtimeID, userID, admin)
	if err != nil {
		return appRuntime.Connection{}, err
	}
	agent, err := s.store.AgentByID(r.Context(), instance.AgentID, userID, admin)
	if err != nil {
		return appRuntime.Connection{}, err
	}
	return s.spawner.Connection(r.Context(), appRuntime.Spec{Runtime: instance, Agent: agent})
}

func (s *Server) serveRuntimeProxy(w http.ResponseWriter, r *http.Request, runtimeID, userID string, connection appRuntime.Connection, proxiedPath, prefix string) {
	target, err := url.Parse(connection.Endpoint)
	if err != nil || target.Scheme != "http" || target.Host == "" {
		writeError(w, http.StatusBadGateway, "runtime_endpoint_invalid", "Runtime endpoint가 유효하지 않습니다.")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.URL.Path = proxiedPath
		request.URL.RawPath = ""
		request.Host = target.Host
		request.Header.Del("Authorization")
		request.Header.Del("Origin")
		// AgentHub's own cookies must not reach the runtime — the Portal session
		// and the runtime access ticket are credentials for this platform, not for
		// the application behind the proxy. Everything else is forwarded, because
		// a runtime UI may keep its own session: Langflow signs the browser in
		// automatically and then authenticates every one of its own API calls with
		// the cookie it just set, so stripping the whole header left the editor
		// loading forever.
		forwardCookies(request)
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Connection", "Upgrade")
		}
		if prefix != "" {
			request.Header.Set("X-Forwarded-Prefix", prefix)
		}
		request.Header.Set("X-AgentHub-User", userID)
		switch {
		case runtimetype.UsesGatewayProxy(connection.RuntimeType):
			// The agenthub-runtime-proxy sidecar expects the fixed "agenthub" user.
			request.Header.Set("Authorization", "Basic "+basicCredential("agenthub", connection.Token))
		case connection.RuntimeType == runtimetype.OpenCode:
			request.Header.Set("Authorization", "Basic "+basicCredential("opencode", connection.Token))
		default:
			request.Header.Set("Authorization", "Bearer "+connection.Token)
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode == http.StatusSwitchingProtocols {
			return nil
		}
		keepRuntimeCookies(response, prefix)
		response.Header.Set("X-AgentHub-Runtime-ID", runtimeID)
		response.Header.Set("X-Content-Type-Options", "nosniff")
		response.Header.Set("Referrer-Policy", "same-origin")
		response.Header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if prefix != "" {
			if location := response.Header.Get("Location"); strings.HasPrefix(location, "/") {
				response.Header.Set("Location", prefix+location)
			}
		}

		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		s.logger.Warn("runtime session proxy failed", "runtime", runtimeID, "error", proxyErr)
		writeError(writer, http.StatusBadGateway, "runtime_proxy_failed", "Runtime에 연결하지 못했습니다.")
	}
	// A terminal is one request. The upgrade is proxied, the connection is
	// hijacked, and the person then works for an hour without this platform
	// serving another HTTP request for them — so refreshing the session where the
	// request arrives marks it live once and lets it go stale underneath somebody
	// who never stopped typing. While the connection is open, the session is in
	// use; that is the whole of what the rule wants to know.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		done := make(chan struct{})
		defer close(done)
		go s.holdRuntimePresence(r.Context(), runtimeID, done)
	}
	proxy.ServeHTTP(w, r)
}

// holdRuntimePresence keeps saying somebody is here for as long as their
// connection is.
//
// It stops when the proxied request returns, which for a hijacked websocket is
// when the connection closes — a closed tab stops refreshing, and the session
// goes stale on its own within the window that reads it.
func (s *Server) holdRuntimePresence(ctx context.Context, runtimeID string, done <-chan struct{}) {
	ticker := time.NewTicker(runtimePresenceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			touch, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			s.store.TouchRuntime(touch, runtimeID)
			s.store.TouchRuntimeSessions(touch, runtimeID)
			cancel()
		}
	}
}

// runtimePresenceInterval is how often an open connection says so. Well inside
// the fifteen minutes that decide whether somebody is at a keyboard, and rare
// enough that a room full of open terminals is a handful of writes a minute.
const runtimePresenceInterval = 2 * time.Minute

// keepRuntimeCookies lets a runtime keep its own session while denying it the two
// things it must not have: this platform's cookie names, and a scope wider than
// the runtime itself.
//
// The whole Set-Cookie header used to be discarded. That is safe, and it is also
// why Langflow's editor answered 403 to its own API calls: it signs the browser
// in through /api/v1/auto_login and then authenticates with the cookies that
// response sets, so a browser that never receives them stays anonymous.
//
// Under a path prefix the runtime shares the Portal's origin, so a cookie it sets
// lands in the Portal's jar. Scoping it to /{runtimeId} is what keeps one runtime
// out of another's session and out of the Portal's, and dropping the agenthub_
// prefix is what stops a runtime from minting something that looks like a
// platform credential. With an origin of its own the cookie is left as it was.
func keepRuntimeCookies(response *http.Response, prefix string) {
	values := response.Header.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": values}}).Cookies()
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		if strings.HasPrefix(cookie.Name, agentHubCookiePrefix) {
			continue
		}
		if prefix != "" {
			// Under a prefix the browser must send this back to this runtime and to
			// nothing else; a domain the runtime chose would widen it again.
			cookie.Path = prefix + "/"
			cookie.Domain = ""
		}
		if encoded := cookie.String(); encoded != "" {
			response.Header.Add("Set-Cookie", encoded)
		}
	}
}

func basicCredential(username, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
}

func writeRuntimeConnectionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "runtime_not_found", "Runtime을 찾을 수 없습니다.")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", err.Error())
}

func runtimeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>AgentHub Runtime</title><style>body{font-family:system-ui;background:#0f172a;color:#e2e8f0;display:grid;place-items:center;min-height:100vh;margin:0}main{max-width:34rem;padding:2rem;border:1px solid #334155;border-radius:1rem;background:#111827}a{color:#a78bfa}</style><main><h1>Runtime 세션을 열 수 없습니다</h1><p>%s</p><p>이 창을 닫고 AgentHub Portal에서 다시 시도하세요.</p></main>`, message)
}

func runtimeIDFromHost(requestHost string, settings sessionGatewaySettings) (string, bool) {
	base, _, ok := settings.hostMode()
	if !ok {
		return "", false
	}
	host, _, err := splitHostPort(requestHost)
	if err != nil {
		return "", false
	}
	suffix := "." + strings.ToLower(base)
	host = strings.ToLower(host)
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	runtimeID := strings.TrimSuffix(host, suffix)
	if runtimeID == "" || strings.Contains(runtimeID, ".") {
		return "", false
	}
	return runtimeID, true
}

func splitHostPort(value string) (hostname, port string, err error) {
	value = strings.TrimSpace(strings.TrimSuffix(value, "."))
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return "", "", errors.New("invalid host")
	}
	if strings.Contains(value, ":") {
		hostname, port, err = net.SplitHostPort(value)
		if err != nil {
			return "", "", err
		}
		if n, parseErr := strconv.Atoi(port); parseErr != nil || n < 1 || n > 65535 {
			return "", "", errors.New("invalid port")
		}
	} else {
		hostname = value
	}
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	if hostname == "" || strings.Contains(hostname, "..") {
		return "", "", errors.New("invalid hostname")
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", "", errors.New("invalid hostname")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", "", errors.New("invalid hostname")
			}
		}
	}
	return hostname, port, nil
}

func shouldTouchRuntime(path string) bool {
	for _, prefix := range []string{"/assets/", "/favicon", "/apple-touch", "/site.webmanifest", "/social-share", "/oc-theme-preload"} {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}
