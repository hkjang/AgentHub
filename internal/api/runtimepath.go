package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// runtimePathGateway serves runtime browser sessions from the Portal's own origin
// at /{runtimeId}/… . It is the fallback for a deployment with no Runtime Base
// Domain: wildcard DNS and a wildcard certificate are a real prerequisite, and
// until they exist a workspace that cannot be opened at all is worse than one
// opened from a shared origin.
//
// A runtime's own origin stays the recommended setup, because it is what keeps a
// runtime's UI out of the Portal's origin. This mode is therefore only active
// while no base domain is configured.
func (s *Server) runtimePathGateway(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings := s.cachedSessionGatewaySettings(r.Context())
		if _, _, hostMode := settings.hostMode(); hostMode {
			next.ServeHTTP(w, r)
			return
		}
		if runtimeID, rest, ok := runtimeIDFromPath(r.URL.Path); ok {
			s.serveRuntimePathSession(w, r, runtimeID, rest, settings)
			return
		}
		// A runtime UI that addresses its assets and APIs from the origin root
		// lands here instead of under its prefix. The Referer names the page the
		// request came from, so a request made by a runtime page is routed to that
		// runtime; every request from a Portal page keeps going to the Portal.
		if runtimeID, ok := runtimeIDFromReferer(r); ok {
			if access, valid := s.pathRuntimeAccess(r, runtimeID); valid {
				s.serveRuntimeAccess(w, r, access, r.URL.Path)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serveRuntimePathSession handles a request under a runtime's path prefix: it
// exchanges a launch ticket for a session, or serves the proxy from one.
func (s *Server) serveRuntimePathSession(w http.ResponseWriter, r *http.Request, runtimeID, rest string, settings sessionGatewaySettings) {
	if ticket := r.URL.Query().Get("ticket"); ticket != "" {
		ticketRuntimeID, userID, err := s.store.ConsumeRuntimeLaunchTicket(r.Context(), ticket)
		if err != nil || ticketRuntimeID != runtimeID {
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
		encrypted, err := s.cipher.Encrypt(raw, runtimePathSessionContext)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "runtime_session_failed", "Runtime 세션을 만들지 못했습니다.")
			return
		}
		// Path "/" rather than the runtime's prefix: the same session has to cover
		// the root-relative requests the UI makes. The cookie is named per runtime
		// so two open workspaces do not overwrite each other's session.
		http.SetCookie(w, &http.Cookie{Name: runtimePathCookieName(runtimeID), Value: encrypted, Path: "/", HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode, Expires: access.ExpiresAt, MaxAge: int(time.Until(access.ExpiresAt).Seconds())})
		// Redirect rather than proxy, so the one-time ticket does not stay in the
		// address bar — or in the URL the runtime UI builds its own links from.
		http.Redirect(w, r, "/"+runtimeID+"/"+rest, http.StatusSeeOther)
		return
	}
	access, valid := s.pathRuntimeAccess(r, runtimeID)
	if !valid {
		s.logger.Warn("runtime access unauthorized", "runtime", runtimeID, "path", r.URL.Path)
		runtimeUnauthorized(w, "AgentHub에서 Runtime을 다시 열어 주세요.")
		return
	}
	s.serveRuntimeAccess(w, r, access, proxiedRuntimePath(access.RuntimeType, runtimeID, rest))
}

// proxiedRuntimePath decides what the runtime is asked for.
//
// Normally the prefix is stripped: the runtime knows nothing about it and serves
// from its own root. A runtime started with its base path set to its runtime id
// is the exception — it expects the prefix, and stripping it would 404 every
// request it makes.
func proxiedRuntimePath(runtimeType, runtimeID, rest string) string {
	if runtimetype.ServesUnderRuntimePath(runtimeType) {
		return "/" + runtimeID + "/" + rest
	}
	return "/" + rest
}

// serveRuntimeAccess proxies one request onto the runtime an established session
// belongs to. The prefix travels as X-Forwarded-Prefix and is put back in front
// of redirects, so a runtime that supports a base path builds correct links.
func (s *Server) serveRuntimeAccess(w http.ResponseWriter, r *http.Request, access runtimeAccess, proxiedPath string) {
	if shouldTouchRuntime(proxiedPath) {
		s.store.TouchRuntime(r.Context(), access.RuntimeID)
	}
	connection := appRuntime.Connection{Endpoint: access.Endpoint, Token: access.Token, RuntimeType: access.RuntimeType}
	s.serveRuntimeProxy(w, r, access.RuntimeID, access.UserID, connection, proxiedPath, "/"+access.RuntimeID)
}

// runtimePathSessionContext binds the encrypted session cookie to this mode, so a
// cookie minted for one gateway cannot be replayed against the other.
const runtimePathSessionContext = "runtime-path-session"

// runtimePathCookieName scopes the session cookie to one runtime. Both gateways
// serve from the Portal's origin in this mode, so a single shared name would let
// opening a second workspace end the first one's session.
func runtimePathCookieName(runtimeID string) string {
	return runtimeAccessCookie + "_" + strings.ReplaceAll(runtimeID, "-", "")
}

// pathRuntimeAccess reads and validates the session cookie for one runtime.
func (s *Server) pathRuntimeAccess(r *http.Request, runtimeID string) (runtimeAccess, bool) {
	cookie, err := r.Cookie(runtimePathCookieName(runtimeID))
	if err != nil {
		return runtimeAccess{}, false
	}
	plain, err := s.cipher.Decrypt(cookie.Value, runtimePathSessionContext)
	if err != nil {
		return runtimeAccess{}, false
	}
	var access runtimeAccess
	if json.Unmarshal(plain, &access) != nil {
		return runtimeAccess{}, false
	}
	if access.RuntimeID != runtimeID || !access.ExpiresAt.After(time.Now()) {
		return runtimeAccess{}, false
	}
	return access, true
}

// runtimeIDFromPath splits /{runtimeId}/rest. The first segment has to be a UUID:
// runtime identifiers are UUIDs, and requiring the shape is what keeps this
// gateway from shadowing a Portal route or an API path.
func runtimeIDFromPath(requestPath string) (runtimeID, rest string, ok bool) {
	first, remainder, _ := strings.Cut(strings.TrimPrefix(requestPath, "/"), "/")
	// Canonical form only: uuid.Validate also accepts the braced and URN spellings,
	// which no runtime identifier ever has.
	if len(first) != 36 || uuid.Validate(first) != nil {
		return "", "", false
	}
	return strings.ToLower(first), remainder, true
}

// runtimeIDFromReferer names the runtime whose page made this request. The
// Referer must point at this same origin: any site can send a Referer, and only
// one from the Portal itself says anything about which page is open.
func runtimeIDFromReferer(r *http.Request) (string, bool) {
	referer, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || referer.Host == "" || !strings.EqualFold(referer.Host, r.Host) {
		return "", false
	}
	runtimeID, _, ok := runtimeIDFromPath(referer.Path)
	return runtimeID, ok
}

// requestIsSecure reports whether the browser reached the Portal over TLS, which
// is what decides the Secure attribute on the session cookie. In this mode the
// session lives on the Portal's own origin, so the Portal's scheme is the answer
// rather than the gateway's configured one.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwarded := r.Header.Get("X-Forwarded-Proto")
	if index := strings.IndexByte(forwarded, ','); index >= 0 {
		forwarded = forwarded[:index]
	}
	return strings.EqualFold(strings.TrimSpace(forwarded), "https")
}
