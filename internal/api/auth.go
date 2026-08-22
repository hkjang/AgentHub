package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/store"
)

type contextKey int

const userContextKey contextKey = 1
const apiScopesContextKey contextKey = 2

type authSettings struct {
	LocalLoginEnabled bool     `json:"localLoginEnabled"`
	OIDCEnabled       bool     `json:"oidcEnabled"`
	IssuerURL         string   `json:"issuerUrl"`
	ClientID          string   `json:"clientId"`
	Scopes            []string `json:"scopes"`
	UsernameClaim     string   `json:"usernameClaim"`
	GroupsClaim       string   `json:"groupsClaim"`
	AdminGroups       []string `json:"adminGroups"`
}

type generalSettings struct {
	ServiceName   string `json:"serviceName"`
	PublicURL     string `json:"publicUrl"`
	DefaultLocale string `json:"defaultLocale"`
	Timezone      string `json:"timezone"`
}

func userFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userContextKey).(store.User)
	return u, ok
}

func (s *Server) authentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user store.User
		var scopes []string
		var err error
		if value := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(value), "bearer ") {
			user, scopes, err = s.store.UserAndScopesByAPIKey(r.Context(), strings.TrimSpace(value[7:]))
		} else if cookie, cookieErr := r.Cookie(sessionCookie); cookieErr == nil {
			user, err = s.store.SessionUser(r.Context(), cookie.Value)
		} else {
			err = store.ErrNotFound
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "로그인이 필요합니다.")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		if scopes != nil {
			ctx = context.WithValue(ctx, apiScopesContextKey, scopes)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) csrfProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || !s.store.ValidateCSRF(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token")) {
			writeError(w, http.StatusForbidden, "csrf_failed", "요청 보안 토큰이 만료되었습니다. 페이지를 새로고침해 주세요.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMethods(w http.ResponseWriter, r *http.Request) {
	settings := authSettings{LocalLoginEnabled: true}
	_ = s.store.Setting(r.Context(), "authentication", &settings)
	writeJSON(w, http.StatusOK, map[string]any{"local": settings.LocalLoginEnabled, "oidc": settings.OIDCEnabled, "oidcLabel": "Keycloak SSO"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var settings authSettings
	_ = s.store.Setting(r.Context(), "authentication", &settings)
	if !settings.LocalLoginEnabled {
		writeError(w, http.StatusForbidden, "local_login_disabled", "로컬 로그인이 비활성화되어 있습니다.")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ip := clientIP(r)
	// Asked before the password is checked, so a guess that is already over the
	// limit costs no bcrypt. The answer is the same whether or not the username
	// exists — a throttle that only fires on real accounts is a way to find them.
	if wait := s.logins.blocked(input.Username, ip); wait > 0 {
		s.refuseLogin(w, r, input.Username, ip, wait, "throttled")
		return
	}
	user, err := s.store.AuthenticateLocal(r.Context(), input.Username, input.Password)
	if err != nil {
		wait := s.logins.fail(input.Username, ip)
		s.store.Audit(r.Context(), nil, "auth.login", "user", "", "failure", ip, map[string]any{"username": input.Username})
		if wait > 0 {
			s.refuseLogin(w, r, input.Username, ip, wait, "blocked")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "아이디 또는 비밀번호를 확인해 주세요.")
		return
	}
	s.logins.succeed(input.Username, ip)
	s.issueSession(w, r, user)
}

// refuseLogin answers a throttled attempt.
//
// It says how long to wait, because a person who has mistyped their password
// five times needs to know the door is not broken, and the attacker learns
// nothing from it that the refusal itself did not already tell them. Every
// refusal is audited: a run of these is what an attack looks like from the
// operator's side, and there was previously nothing to see.
func (s *Server) refuseLogin(w http.ResponseWriter, r *http.Request, username, ip string, wait time.Duration, reason string) {
	seconds := int(wait.Seconds()) + 1
	s.store.Audit(r.Context(), nil, "auth.throttled", "user", "", "failure", ip,
		map[string]any{"username": username, "retryAfterSeconds": seconds, "reason": reason})
	s.logger.Warn("login attempts throttled", "username", username, "ip", ip, "retryAfterSeconds", seconds, "reason", reason)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts",
		fmt.Sprintf("로그인 시도가 너무 많습니다. %d초 후에 다시 시도해 주세요.", seconds))
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user store.User) {
	token, csrf, expires, err := s.store.CreateSession(r.Context(), user.ID, clientIP(r), r.UserAgent())
	if err != nil {
		s.logger.Error("create session", "error", err)
		writeStoreError(w, err)
		return
	}
	secure := r.TLS != nil
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	s.store.Audit(r.Context(), &user, "auth.login", "user", user.ID, "success", clientIP(r), nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrfToken": csrf})
}

// minimumPasswordLength is a floor, not a policy.
//
// The platform does not get to be clever here: rules about mixed case and
// punctuation push people towards one predictable word with a digit on the end.
// Length is the part that actually costs an attacker, and the throttle in front
// of the login route is what bounds how fast anybody can try.
const minimumPasswordLength = 12

// changePassword rotates the caller's own local password.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len([]rune(input.NewPassword)) < minimumPasswordLength {
		writeError(w, http.StatusBadRequest, "password_too_short",
			fmt.Sprintf("새 비밀번호는 %d자 이상이어야 합니다.", minimumPasswordLength))
		return
	}
	if input.NewPassword == input.CurrentPassword {
		writeError(w, http.StatusBadRequest, "password_unchanged", "새 비밀번호가 지금 쓰는 것과 같습니다.")
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "no_session", "다시 로그인해 주세요.")
		return
	}
	switch err := s.store.ChangePassword(r.Context(), u.ID, input.CurrentPassword, input.NewPassword, cookie.Value); {
	case err == nil:
	case errors.Is(err, store.ErrNoLocalPassword):
		writeError(w, http.StatusConflict, "no_local_password", "이 계정은 SSO로 로그인합니다. 비밀번호는 SSO 쪽에서 바꿔 주세요.")
		return
	case errors.Is(err, store.ErrInvalidPassword):
		// A wrong current password here is a guess at the account of whoever is
		// already signed in, so it counts against the same allowance the login
		// route uses. Otherwise this is the way around the throttle.
		s.logins.fail(u.Username, clientIP(r))
		s.store.Audit(r.Context(), &u, "auth.password_change", "user", u.ID, "failure", clientIP(r), nil)
		writeError(w, http.StatusForbidden, "invalid_credentials", "지금 쓰는 비밀번호를 확인해 주세요.")
		return
	default:
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "auth.password_change", "user", u.ID, "success", clientIP(r),
		map[string]any{"otherSessionsEnded": true})
	s.logger.Info("password changed", "user", u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"changed": true, "otherSessionsEnded": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Expires: time.Unix(1, 0), MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: "", Path: "/", HttpOnly: false, Expires: time.Unix(1, 0), MaxAge: -1})
	s.store.Audit(r.Context(), &user, "auth.logout", "user", user.ID, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	csrf := ""
	if cookie, err := r.Cookie(sessionCookie); err == nil { // rotate the browser-visible CSRF token by issuing a new session only on login; current value is carried in a readable companion cookie.
		_ = cookie
		if csrfCookie, err := r.Cookie(csrfCookie); err == nil {
			csrf = csrfCookie.Value
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrfToken": csrf, "version": s.version})
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	settings, oauthConfig, _, err := s.oidcConfig(r.Context())
	if err != nil {
		s.logger.Warn("OIDC start rejected", "error", err)
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "SSO 설정을 확인해 주세요.")
		return
	}
	state, err := cryptox.RandomToken(24)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	payload, _ := json.Marshal(map[string]any{"state": state, "verifier": verifier, "expires": time.Now().Add(10 * time.Minute).Unix()})
	encrypted, err := s.cipher.Encrypt(payload, "oidc-state")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: encrypted, Path: "/api/v1/auth/oidc/callback", HttpOnly: true, Secure: strings.HasPrefix(settings.PublicURL, "https://") || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, oauthConfig.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

type oidcCombinedSettings struct {
	authSettings
	generalSettings
}

func (s *Server) oidcConfig(ctx context.Context) (oidcCombinedSettings, *oauth2.Config, *oidc.Provider, error) {
	var auth authSettings
	var general generalSettings
	if err := s.store.Setting(ctx, "authentication", &auth); err != nil {
		return oidcCombinedSettings{}, nil, nil, err
	}
	if err := s.store.Setting(ctx, "general", &general); err != nil {
		return oidcCombinedSettings{}, nil, nil, err
	}
	combined := oidcCombinedSettings{authSettings: auth, generalSettings: general}
	if !auth.OIDCEnabled || auth.IssuerURL == "" || auth.ClientID == "" || general.PublicURL == "" {
		return combined, nil, nil, fmt.Errorf("OIDC issuer, client and public URL are required")
	}
	secret, err := s.store.SettingSecret(ctx, "authentication")
	if err != nil || secret == "" {
		return combined, nil, nil, fmt.Errorf("OIDC client secret is required")
	}
	provider, err := oidc.NewProvider(ctx, auth.IssuerURL)
	if err != nil {
		return combined, nil, nil, err
	}
	scopes := auth.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	config := &oauth2.Config{ClientID: auth.ClientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: strings.TrimRight(general.PublicURL, "/") + "/api/v1/auth/oidc/callback", Scopes: scopes}
	return combined, config, provider, nil
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	settings, oauthConfig, provider, err := s.oidcConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "SSO 설정을 확인해 주세요.")
		return
	}
	cookie, err := r.Cookie(oidcCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "oidc_state_missing", "SSO 요청이 만료되었습니다.")
		return
	}
	plain, err := s.cipher.Decrypt(cookie.Value, "oidc-state")
	if err != nil {
		writeError(w, http.StatusBadRequest, "oidc_state_invalid", "SSO 요청 검증에 실패했습니다.")
		return
	}
	var state struct {
		State    string `json:"state"`
		Verifier string `json:"verifier"`
		Expires  int64  `json:"expires"`
	}
	if json.Unmarshal(plain, &state) != nil || state.State != r.URL.Query().Get("state") || time.Now().Unix() > state.Expires {
		writeError(w, http.StatusBadRequest, "oidc_state_invalid", "SSO 요청 검증에 실패했습니다.")
		return
	}
	token, err := oauthConfig.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(state.Verifier))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "oidc_exchange_failed", "Keycloak 인증 코드를 확인하지 못했습니다.")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusUnauthorized, "oidc_token_missing", "Keycloak ID Token이 없습니다.")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: settings.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "oidc_token_invalid", "Keycloak ID Token 검증에 실패했습니다.")
		return
	}
	claims := map[string]any{}
	if idToken.Claims(&claims) != nil {
		writeError(w, http.StatusUnauthorized, "oidc_claims_invalid", "SSO 사용자 정보를 읽지 못했습니다.")
		return
	}
	claimString := func(key string) string {
		if v, ok := claims[key].(string); ok {
			return v
		}
		return ""
	}
	usernameClaim := settings.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}
	groupsClaim := settings.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	groups := toStrings(claims[groupsClaim])
	admin := false
	for _, group := range groups {
		if slices.Contains(settings.AdminGroups, group) {
			admin = true
			break
		}
	}
	user, err := s.store.UpsertOIDCUser(r.Context(), idToken.Subject, claimString(usernameClaim), claimString("email"), firstNonEmpty(claimString("name"), claimString(usernameClaim)), admin)
	if err != nil {
		s.logger.Error("upsert OIDC user", "error", err)
		writeStoreError(w, err)
		return
	}
	tokenValue, csrf, expires, err := s.store.CreateSession(r.Context(), user.ID, clientIP(r), r.UserAgent())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	secure := strings.HasPrefix(settings.PublicURL, "https://") || r.TLS != nil
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tokenValue, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires})
	s.store.Audit(r.Context(), &user, "auth.oidc_login", "user", user.ID, "success", clientIP(r), map[string]any{"issuer": settings.IssuerURL})
	http.Redirect(w, r, "/", http.StatusFound)
}

func toStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if values, ok := value.([]string); ok {
			return values
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if v, ok := item.(string); ok {
			result = append(result, v)
		}
	}
	return result
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func clientIP(r *http.Request) string {
	if value := r.Header.Get("X-Forwarded-For"); value != "" {
		if first, _, ok := strings.Cut(value, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(value)
	}
	host := r.RemoteAddr
	if parsed, err := url.Parse("//" + host); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return host
}
