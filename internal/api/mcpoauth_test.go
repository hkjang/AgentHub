package api

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// The parts of MCP SSO that need no provider and no database: how a bearer is
// told apart from a key, which tokens are for this server, which claims make
// an otherwise valid JWT unusable, and what an SSO principal may do.

// A key is a key by its prefix; a token is three non-empty dot-separated
// parts; everything else is refused as a bad key, exactly as before.
func TestABearerIsAKeyOrATokenByItsShape(t *testing.T) {
	cases := map[string]bool{
		"ahk_abcdef.ghij.klmn":                      false, // the prefix wins, whatever follows
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln": true,
		"a.b.c":                     true,
		"a.b":                       false,
		"a..c":                      false,
		"":                          false,
		"not-a-key-and-not-a-token": false,
	}
	for bearer, want := range cases {
		if got := looksLikeJWT(bearer); got != want {
			t.Errorf("looksLikeJWT(%q) = %v, want %v", bearer, got, want)
		}
	}
}

// Measured against a real Keycloak 26: the client id is in azp and aud holds
// only "account". So a token is ours when aud names the resource (the mapper
// path) or when aud or azp is on the administrator's list (the plain path).
func TestATokenIsForThisServerByAudOrAzp(t *testing.T) {
	resource := "https://agenthub.example.test/mcp"
	if !acceptedAudience(resource, nil, []string{resource}, "") {
		t.Error("a token whose aud names the resource was refused")
	}
	if !acceptedAudience(resource, []string{"claude-mcp"}, []string{"account"}, "claude-mcp") {
		t.Error("a token issued to a client on the administrator's list was refused")
	}
	if !acceptedAudience(resource, []string{"claude-mcp"}, []string{"claude-mcp", "account"}, "") {
		t.Error("a listed client id in aud was refused")
	}
	if acceptedAudience(resource, []string{"claude-mcp"}, []string{"account"}, "some-other-app") {
		t.Error("a token issued to another application in the realm was accepted")
	}
	if acceptedAudience(resource, nil, nil, "") {
		t.Error("a token bound to nothing was accepted")
	}
	// An empty entry never matches an empty claim.
	if acceptedAudience(resource, []string{""}, []string{""}, "") {
		t.Error("an empty audience matched an empty list entry")
	}
}

// go-oidc checks signature, issuer and exp; these are the claims it does not.
func TestTheClaimsTheLibraryDoesNotCheckAreCheckedHere(t *testing.T) {
	now := time.Now()
	if refusal := refuseAccessClaims(oauthAccessClaims{Type: "Bearer"}, now); refusal != nil {
		t.Errorf("a plain access token was refused: %v", refusal)
	}
	if refusal := refuseAccessClaims(oauthAccessClaims{}, now); refusal != nil {
		t.Errorf("a token with no typ was refused: %v", refusal)
	}
	if refuseAccessClaims(oauthAccessClaims{Type: "ID"}, now) == nil {
		t.Error("an ID token was accepted as an API credential")
	}
	if refuseAccessClaims(oauthAccessClaims{Type: "Bearer", Confirmation: json.RawMessage(`{"jkt":"x"}`)}, now) == nil {
		t.Error("a token bound to a key this server cannot verify (cnf) was accepted")
	}
	if refuseAccessClaims(oauthAccessClaims{Type: "Bearer", NotBefore: now.Add(time.Hour).Unix()}, now) == nil {
		t.Error("a token that is not yet valid was accepted")
	}
	// A clock a few seconds ahead is not a refusal.
	if refusal := refuseAccessClaims(oauthAccessClaims{Type: "Bearer", NotBefore: now.Add(10 * time.Second).Unix()}, now); refusal != nil {
		t.Errorf("a token whose nbf is within clock skew was refused: %v", refusal)
	}
}

// The administrator's setting is the ceiling. A token that speaks this
// platform's vocabulary narrows it; one that speaks Keycloak's usual "openid
// profile email" does not touch it.
func TestAnSSOPrincipalGetsTheAdministratorsScopesAndNoMore(t *testing.T) {
	ceiling := []string{ScopeMCP, ScopeWrite}
	if got := grantedScopes(ceiling, "openid profile email"); !slices.Equal(got, ceiling) {
		t.Errorf("a token without our vocabulary got %v, want the ceiling %v", got, ceiling)
	}
	if got := grantedScopes(ceiling, "openid mcp:read"); !slices.Equal(got, []string{ScopeMCP}) {
		t.Errorf("a token asking for mcp:read got %v", got)
	}
	// Asking for more than the ceiling yields the ceiling's part of it, never
	// the extra.
	if got := grantedScopes(ceiling, "runtime:manage mcp:read"); slices.Contains(got, ScopeRuntime) || !slices.Contains(got, ScopeMCP) {
		t.Errorf("a token asking past the ceiling got %v", got)
	}
	if got := grantedScopes([]string{ScopeMCP}, "runtime:manage"); len(got) != 0 {
		t.Errorf("a token asking only for what the ceiling excludes got %v", got)
	}
}

// The resource is the configured identifier; the request's Host is the last
// resort, and then the proxy's scheme is honoured.
func TestTheResourceComesFromSettingsBeforeTheHostHeader(t *testing.T) {
	request := httptest.NewRequest("POST", "/mcp", nil)
	request.Host = "spoofed.example"
	request.Header.Set("X-Forwarded-Proto", "https")
	configured := mcpOAuthConfig{Resource: "https://agenthub.example.test/mcp"}
	if got := configured.mcpResource(request); got != "https://agenthub.example.test/mcp" {
		t.Errorf("the configured resource lost to the Host header: %q", got)
	}
	if got := configured.metadataURL(request); got != "https://agenthub.example.test/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("metadata URL %q", got)
	}
	if got := (mcpOAuthConfig{}).mcpResource(request); got != "https://spoofed.example/mcp" {
		t.Errorf("with nothing configured the Host is the last resort, with the proxy's scheme: %q", got)
	}
}
