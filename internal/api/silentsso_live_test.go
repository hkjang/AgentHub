package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/store"
)

// Silent sign-in through the production route table, against a database and
// a stand-in identity provider.
//
// The unit tests prove what the callback does with a refusal. What they cannot
// prove is the wiring: that the start route reads the administrator's setting
// before it honours prompt=none, that the state it hides in the cookie is the
// state the callback reads back, and that a person who arrived by a deep link
// is sent back to it once the provider has answered. Each of those is a place
// a refactor could break silently, so each is asked of the real handlers here.
//
//	AGENTHUB_TEST_DSN=postgres://... AGENTHUB_ENCRYPTION_KEY=<base64 32 bytes> \
//	go test ./internal/api/ -run SilentSignIn -v

// standInProvider is the smallest identity provider the login flow will talk
// to: a discovery document, a signing key, and a token endpoint that mints an
// ID token for whoever asks. The authorization endpoint is never called — the
// test plays the browser and goes straight to the callback with what the
// provider would have sent.
type standInProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	// tokensIssued counts calls to the token endpoint, so a test can tell an
	// exchange that happened from one that was skipped.
	tokensIssued int
}

func newStandInProvider(t *testing.T, clientID string) *standInProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &standInProvider{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		base := provider.server.URL
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer": base, "authorization_endpoint": base + "/auth", "token_endpoint": base + "/token", "jwks_uri": base + "/jwks",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		public := key.PublicKey
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "stand-in", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(public.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(public.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		provider.tokensIssued++
		now := time.Now().Unix()
		claims, _ := json.Marshal(map[string]any{
			"iss": provider.server.URL, "sub": "silent-sso-subject", "aud": clientID, "exp": now + 300, "iat": now,
			"preferred_username": "silent-sso-person", "email": "silent-sso@example.test", "name": "조용한 로그인",
		})
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "stand-in"})
		signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(signingInput))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "stand-in", "token_type": "Bearer", "id_token": signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)})
	})
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

// silentSignInDeployment is the platform with OIDC pointed at the stand-in and
// auto-login set as asked. The settings are written straight to the store: the
// admin route insists on HTTPS issuers, and the stand-in listens on loopback.
func silentSignInDeployment(t *testing.T, autoLogin bool) (http.Handler, *store.Store, *standInProvider) {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("AGENTHUB_ENCRYPTION_KEY"))
	if err != nil || len(rawKey) == 0 {
		t.Skip("no encryption key to seal the login state with")
	}
	cipher, err := cryptox.New(rawKey)
	if err != nil {
		t.Skip("no encryption key to seal the login state with")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("the schema could not be brought up to date: %v", err)
	}
	provider := newStandInProvider(t, "agenthub-silent-test")

	// Settings record who wrote them, so the writer has to exist. Upserted on a
	// fixed subject: re-running leaves one of them rather than a pile.
	operator, err := db.UpsertOIDCUser(ctx, "agenthub-silent-sso-test:operator", "silent-sso-operator", "", "", true)
	if err != nil {
		t.Skipf("this deployment will not let the check create the operator it needs: %v", err)
	}
	// Whatever this deployment had is put back afterwards, so a run against a
	// long-lived database does not leave it pointed at a provider that is gone.
	var previousAuth, previousGeneral json.RawMessage
	_ = db.Setting(ctx, "authentication", &previousAuth)
	_ = db.Setting(ctx, "general", &previousGeneral)
	previousSecret, _ := db.SettingSecret(ctx, "authentication")
	t.Cleanup(func() {
		if previousAuth != nil {
			_ = db.PutSetting(ctx, "authentication", previousAuth, &previousSecret, operator.ID)
		}
		if previousGeneral != nil {
			_ = db.PutSetting(ctx, "general", previousGeneral, nil, operator.ID)
		}
	})
	secret := "stand-in-secret"
	auth := authSettings{LocalLoginEnabled: true, OIDCEnabled: true, IssuerURL: provider.server.URL, ClientID: "agenthub-silent-test", AutoLogin: autoLogin}
	if err := db.PutSetting(ctx, "authentication", auth, &secret, operator.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSetting(ctx, "general", generalSettings{ServiceName: "AgentHub", PublicURL: "http://localhost:8080"}, nil, operator.ID); err != nil {
		t.Fatal(err)
	}
	server := New(db, cipher, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), appLog.NewRing(8), nil, nil)
	return server.Handler(), db, provider
}

// beginLogin plays the browser's first move and returns where it was sent and
// the cookie it was handed.
func beginLogin(t *testing.T, handler http.Handler, query string) (*url.URL, *http.Cookie) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start"+query, nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("the start route answered %d %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcCookie {
			return location, cookie
		}
	}
	t.Fatal("the start route sent the browser away without the state cookie the callback needs")
	return nil, nil
}

func finishLogin(t *testing.T, handler http.Handler, cookie *http.Cookie, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+query.Encode(), nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// With auto-login off, ?prompt=none on the start route changes nothing: the
// provider is asked for an ordinary login. This is what ties the redirect
// surface to the administrator's setting rather than to whatever a link says.
func TestSilentSignInIsNotOfferedUntilAnAdministratorTurnsItOn(t *testing.T) {
	handler, _, _ := silentSignInDeployment(t, false)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/methods", nil))
	var methods struct {
		AutoLogin bool `json:"autoLogin"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &methods); err != nil || methods.AutoLogin {
		t.Fatalf("the login methods advertise autoLogin=%v while the setting is off (%s)", methods.AutoLogin, recorder.Body.String())
	}

	location, cookie := beginLogin(t, handler, "?prompt=none&return_to=%2Fruns")
	if location.Query().Get("prompt") != "" {
		t.Fatalf("the provider was asked with prompt=%q although auto-login is off; anybody could change the flow by appending it to a link", location.Query().Get("prompt"))
	}
	// And the provider's refusal of that ordinary login is not filed as a
	// silent one — the state cookie recorded what was actually asked.
	done := finishLogin(t, handler, cookie, url.Values{"error": {"login_required"}, "state": {location.Query().Get("state")}})
	if got := done.Header().Get("Location"); got != providerErrorPath {
		t.Fatalf("a refused ordinary login landed on %q", got)
	}
}

// With auto-login on, the start route asks the provider with prompt=none and a
// refusal lands on the login screen carrying the marker that stops the retry.
func TestSilentSignInIsRefusedQuietlyWhenTheProviderHasNoSession(t *testing.T) {
	handler, _, provider := silentSignInDeployment(t, true)
	location, cookie := beginLogin(t, handler, "?prompt=none&return_to=%2Fruns")
	if location.Query().Get("prompt") != "none" {
		t.Fatalf("auto-login is on and the browser asked, yet the provider was sent %q", location.String())
	}
	if !strings.HasPrefix(location.String(), provider.server.URL+"/auth") {
		t.Fatalf("the browser was sent to %q, not the provider's authorization endpoint", location.String())
	}
	done := finishLogin(t, handler, cookie, url.Values{"error": {"login_required"}, "state": {location.Query().Get("state")}})
	if done.Code != http.StatusFound || done.Header().Get("Location") != silentRefusalPath {
		t.Fatalf("the refusal answered %d → %q; the console needs %q to know not to try again", done.Code, done.Header().Get("Location"), silentRefusalPath)
	}
	if provider.tokensIssued != 0 {
		t.Error("the callback tried to exchange a code the provider never sent")
	}
}

// When the provider does hold a session, the person who opened a deep link
// ends up signed in and back on that link, and the trail says the login was
// silent.
func TestSilentSignInReturnsToTheDeepLink(t *testing.T) {
	handler, db, provider := silentSignInDeployment(t, true)
	location, cookie := beginLogin(t, handler, "?prompt=none&return_to="+url.QueryEscape("/runs?tab=history#latest"))
	done := finishLogin(t, handler, cookie, url.Values{"code": {"stand-in-code"}, "state": {location.Query().Get("state")}})
	if done.Code != http.StatusFound {
		t.Fatalf("the callback answered %d %s", done.Code, done.Body.String())
	}
	if got := done.Header().Get("Location"); got != "/runs?tab=history#latest" {
		t.Fatalf("signed in and sent to %q; a person who arrived by a deep link was meant to go back to it", got)
	}
	if provider.tokensIssued != 1 {
		t.Fatalf("the token endpoint was called %d times", provider.tokensIssued)
	}
	var session *http.Cookie
	for _, set := range done.Result().Cookies() {
		if set.Name == sessionCookie && set.Value != "" {
			session = set
		}
	}
	if session == nil {
		t.Fatal("the callback redirected without issuing a session")
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	me.AddCookie(session)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, me)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "silent-sso-person") {
		t.Fatalf("the session the silent login issued does not open the console: %d %s", recorder.Code, recorder.Body.String())
	}

	page, err := db.AuditTrail(context.Background(), store.AuditFilter{Action: "auth.oidc_login", Limit: 1})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("no audit entry for the login: %v", err)
	}
	details, _ := page.Items[0]["details"].(map[string]any)
	if silent, _ := details["silent"].(bool); !silent {
		t.Errorf("the audit entry does not say the login was silent: %v", page.Items[0]["details"])
	}
}

// The way back is validated where it is stored and again where it is used; a
// return address on another host is replaced by the front page.
func TestSilentSignInWillNotReturnToAnotherHost(t *testing.T) {
	handler, _, _ := silentSignInDeployment(t, true)
	location, cookie := beginLogin(t, handler, "?prompt=none&return_to="+url.QueryEscape("//evil.example/steal"))
	done := finishLogin(t, handler, cookie, url.Values{"code": {"stand-in-code"}, "state": {location.Query().Get("state")}})
	if got := done.Header().Get("Location"); got != "/" {
		t.Fatalf("signed in and sent to %q; a login flow that forwards wherever it is told is a way out of the building", got)
	}
}
