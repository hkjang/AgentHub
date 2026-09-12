package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/cryptox"
)

// Silent sign-in, the part the callback owns.
//
// A prompt=none attempt never draws a screen: the provider either sends a code
// straight back or answers login_required. That answer is ordinary, and the
// whole danger is in what happens next — if the console tries again on the
// next page load, the browser bounces between the two sites for as long as
// anybody watches it flicker. The callback's job on a refusal is therefore to
// land on the login screen with the marker that stops the retry, and to do so
// only for an attempt this server actually began as silent.

func silentSsoServer(t *testing.T) *Server {
	t.Helper()
	cipher, err := cryptox.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{logger: slog.Default(), cipher: cipher}
}

// pendingCookie is what the start leg leaves behind for the callback to find.
func pendingCookie(t *testing.T, server *Server, state oidcState) *http.Cookie {
	t.Helper()
	if state.Expires == 0 {
		state.Expires = time.Now().Add(time.Minute).Unix()
	}
	payload, _ := json.Marshal(state)
	encrypted, err := server.cipher.Encrypt(payload, "oidc-state")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: oidcCookie, Value: encrypted}
}

func callbackWithError(t *testing.T, server *Server, cookie *http.Cookie, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+query.Encode(), nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.oidcCallback(recorder, request)
	return recorder
}

func TestARefusedSilentAttemptLandsOnTheLoginScreenMarked(t *testing.T) {
	server := silentSsoServer(t)
	cookie := pendingCookie(t, server, oidcState{State: "s1", Verifier: "v", Silent: true, ReturnTo: "/runs"})
	recorder := callbackWithError(t, server, cookie, url.Values{"error": {"login_required"}, "state": {"s1"}})

	if recorder.Code != http.StatusFound {
		t.Fatalf("a refused silent attempt answered %d %s; the person was meant to be sent to the login screen", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != silentRefusalPath {
		t.Fatalf("landed on %q; without the sso=none marker in the address the console would try again and loop", got)
	}
	cleared := false
	for _, set := range recorder.Result().Cookies() {
		if set.Name == oidcCookie && set.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the pending state cookie was left behind after the provider answered")
	}
}

// The same error on a login the person started by hand is not a silent
// refusal, and the marker would be wrong: it says "do not try silently again"
// about an attempt that was never silent, and hides that something is wrong
// with the provider.
func TestAProviderErrorOnAnOrdinaryLoginIsNotFiledAsARefusal(t *testing.T) {
	server := silentSsoServer(t)
	cookie := pendingCookie(t, server, oidcState{State: "s2", Verifier: "v"})
	recorder := callbackWithError(t, server, cookie, url.Values{"error": {"access_denied"}, "state": {"s2"}})
	if got := recorder.Header().Get("Location"); got != providerErrorPath {
		t.Fatalf("an ordinary login's provider error landed on %q, not %q", got, providerErrorPath)
	}
}

// The marker only follows an attempt this server began. A callback that
// carries a silent state it cannot match — no cookie, another state, a stale
// one — is treated as an error the console will not retry silently either,
// but is not reported as a refusal of something that did not happen.
func TestARefusalIsOnlySilentForTheAttemptThatWasBegun(t *testing.T) {
	server := silentSsoServer(t)
	silent := oidcState{State: "s3", Verifier: "v", Silent: true}
	for name, test := range map[string]struct {
		cookie *http.Cookie
		state  string
	}{
		"no cookie":       {nil, "s3"},
		"another state":   {pendingCookie(t, server, silent), "s4"},
		"expired attempt": {pendingCookie(t, server, oidcState{State: "s3", Verifier: "v", Silent: true, Expires: time.Now().Add(-time.Minute).Unix()}), "s3"},
	} {
		recorder := callbackWithError(t, server, test.cookie, url.Values{"error": {"login_required"}, "state": {test.state}})
		if got := recorder.Header().Get("Location"); got != providerErrorPath {
			t.Errorf("%s: landed on %q; a refusal the server cannot tie to a silent attempt it began should land on %q", name, got, providerErrorPath)
		}
	}
}

// Whatever the callback lands on, the console must not attempt again from
// there — both markers are ones the browser rule refuses to start from.
func TestBothLandingAddressesCarryAMarkerTheConsoleStopsOn(t *testing.T) {
	for _, landing := range []string{silentRefusalPath, providerErrorPath} {
		parsed, err := url.Parse(landing)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Path != "/login" {
			t.Errorf("%q lands somewhere other than the login screen, which is the one page the console never attempts from", landing)
		}
		if sso := parsed.Query().Get("sso"); sso != "none" && sso != "error" {
			t.Errorf("%q carries sso=%q, which the console's rule does not recognise as a reason to stop", landing, sso)
		}
	}
}

// return_to is where a person who arrived by a deep link goes after signing
// in. Only a path on this origin is accepted; anything a browser could read as
// another host makes the login flow a way out of the building.
func TestAReturnAddressStaysOnThisOrigin(t *testing.T) {
	for value, want := range map[string]bool{
		"/runs":                 true,
		"/runs?x=1#y":           true,
		"/":                     true,
		"":                      false,
		"runs":                  false,
		"//evil.example/":       false,
		"/\\evil.example":       false,
		"https://evil.example/": false,
		"/runs\r\nSet-Cookie:x": false,
	} {
		if got := safeReturnTo(value); got != want {
			t.Errorf("safeReturnTo(%q) = %v, want %v", value, got, want)
		}
	}
}

// Whether asking is honoured is the start route's business, and that route
// reads the administrator's setting — proved against a database and a stand-in
// provider in silentsso_live_test.go, because the wiring is the claim.
func TestAskingForASilentAttemptIsReadFromTheQuery(t *testing.T) {
	asked := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start?prompt=none", nil)
	plain := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start", nil)
	if !silentLoginRequested(asked) || silentLoginRequested(plain) {
		t.Fatal("silentLoginRequested does not read prompt=none from the query")
	}
}
