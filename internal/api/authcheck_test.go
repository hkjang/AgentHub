package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This is the setting that can lock a deployment out of itself, so each answer
// has to name the field that is wrong rather than reporting that login is
// broken.
func TestTheSSOCheckNamesWhatIsWrong(t *testing.T) {
	t.Run("everything right", func(t *testing.T) {
		provider := oidcStub(t, true, "")
		defer provider.Close()
		result := checkOIDC(context.Background(), provider.URL, "agenthub", "s3cret")
		if result.Verdict != "ok" || result.Client != "확인됨" {
			t.Fatalf("%#v", result)
		}
	})

	t.Run("secret refused", func(t *testing.T) {
		provider := oidcStub(t, false, "")
		defer provider.Close()
		result := checkOIDC(context.Background(), provider.URL, "agenthub", "wrong")
		if result.Verdict != "client_rejected" || !strings.Contains(result.Detail, "Client ID/Secret") {
			t.Fatalf("%#v", result)
		}
	})

	// A provider that recognises the client and refuses this particular grant is
	// correctly configured: the login flow does not use client_credentials, and
	// insisting on it would fail a realm that is perfectly fine.
	t.Run("grant not enabled is not a failure", func(t *testing.T) {
		provider := oidcStub(t, false, "unauthorized_client")
		defer provider.Close()
		if result := checkOIDC(context.Background(), provider.URL, "agenthub", "s3cret"); result.Verdict != "ok" {
			t.Fatalf("%#v", result)
		}
	})

	// The issuer inside a token is validated against the one the provider
	// declares. A mismatch becomes a login failure later whose message mentions
	// neither setting.
	t.Run("issuer says something else", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": "https://sso.example/realms/other", "token_endpoint": "https://sso.example/token",
				"authorization_endpoint": "https://sso.example/auth", "jwks_uri": "https://sso.example/jwks",
			})
		}))
		defer provider.Close()
		result := checkOIDC(context.Background(), provider.URL, "agenthub", "")
		if result.Verdict != "issuer_mismatch" || !strings.Contains(result.Detail, "realms/other") {
			t.Fatalf("%#v", result)
		}
	})

	t.Run("not a discovery document", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>hello</html>"))
		}))
		defer provider.Close()
		if result := checkOIDC(context.Background(), provider.URL, "agenthub", ""); result.Verdict != "not_oidc" {
			t.Fatalf("%#v", result)
		}
	})

	t.Run("wrong path", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer provider.Close()
		result := checkOIDC(context.Background(), provider.URL, "agenthub", "")
		if result.Verdict != "not_found" || !strings.Contains(result.Detail, "realms") {
			t.Fatalf("%#v", result)
		}
	})

	t.Run("nothing configured", func(t *testing.T) {
		if result := checkOIDC(context.Background(), "", "", ""); result.Verdict != "unconfigured" {
			t.Fatalf("%#v", result)
		}
		if result := checkOIDC(context.Background(), "https://sso.example", "", ""); !strings.Contains(result.Detail, "Client ID") {
			t.Fatalf("%#v", result)
		}
	})

	t.Run("issuer unreachable", func(t *testing.T) {
		if result := checkOIDC(context.Background(), "http://127.0.0.1:9/realms/x", "agenthub", ""); result.Verdict != "unreachable" {
			t.Fatalf("%#v", result)
		}
	})
}

// oidcStub answers discovery, and answers the token endpoint the way a provider
// does when the credentials are right, wrong, or right but for another grant.
func oidcStub(t *testing.T, accept bool, refusal string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": server.URL, "token_endpoint": server.URL + "/token",
				"authorization_endpoint": server.URL + "/auth", "jwks_uri": server.URL + "/jwks",
			})
			return
		}
		switch {
		case accept:
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
		case refusal != "":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": refusal})
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		}
	}))
	return server
}
