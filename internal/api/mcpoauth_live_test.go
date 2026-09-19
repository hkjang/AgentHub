package api

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// MCP SSO through the production route table, against a database and a
// stand-in Keycloak that signs real tokens.
//
// The authorization flow itself — PKCE, the redirect, the code exchange — is
// Keycloak's and the client's. What is this server's is the resource-server
// half of the specification, and that is what these tests hold it to: it says
// where the authorization server is, it turns a 401 into a pointer there, and
// it accepts exactly the tokens that server issued for this resource, for a
// person the platform already knows, with the powers the administrator chose
// and no more. Every case goes through server.Handler(), because the place
// this can break is the wiring — which bearer goes to which lookup, which
// route carries the challenge — and a test that calls oauthPrincipal directly
// would not see it.
//
//	AGENTHUB_TEST_DSN=postgres://... AGENTHUB_ENCRYPTION_KEY=<base64 32 bytes> \
//	go test ./internal/api/ -run MCPSSO -v

const mcpListTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// sign mints a token the way the realm would: RS256 under the stand-in's key,
// with whatever header and claims the test asks for.
func (p *standInProvider) sign(t *testing.T, header map[string]any, claims map[string]any) string {
	t.Helper()
	if header == nil {
		header = map[string]any{"alg": "RS256", "typ": "JWT", "kid": "stand-in"}
	}
	rawHeader, _ := json.Marshal(header)
	rawClaims, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(rawHeader) + "." + base64.RawURLEncoding.EncodeToString(rawClaims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// accessToken is what Keycloak hands an MCP client after the person signed
// in: issued by the realm, for an audience, typ=Bearer, an hour to live.
func (p *standInProvider) accessToken(t *testing.T, subject string, audience any, extra map[string]any) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": p.server.URL, "sub": subject, "aud": audience, "typ": "Bearer",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "scope": "openid profile email",
	}
	for key, value := range extra {
		claims[key] = value
	}
	return p.sign(t, nil, claims)
}

// mcpSSODeployment is the platform with OIDC pointed at the stand-in (the
// silent sign-in deployment does that) and the MCP SSO row written as asked.
func mcpSSODeployment(t *testing.T, settings mcpOAuthSettings) (http.Handler, *store.Store, *standInProvider) {
	t.Helper()
	handler, db, provider := silentSignInDeployment(t, false)
	ctx := context.Background()
	operator, err := db.UpsertOIDCUser(ctx, "agenthub-silent-sso-test:operator", "silent-sso-operator", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	var previous json.RawMessage
	_ = db.Setting(ctx, mcpOAuthSettingKey, &previous)
	t.Cleanup(func() {
		if previous != nil {
			_ = db.PutSetting(ctx, mcpOAuthSettingKey, previous, nil, operator.ID)
		} else {
			_ = db.PutSetting(ctx, mcpOAuthSettingKey, mcpOAuthSettings{}, nil, operator.ID)
		}
	})
	if err := db.PutSetting(ctx, mcpOAuthSettingKey, settings, nil, operator.ID); err != nil {
		t.Fatal(err)
	}
	return handler, db, provider
}

// registeredMember is somebody who has signed in to the web once — which is
// what registers a person — with the status asked for.
func registeredMember(t *testing.T, db *store.Store, status string) store.User {
	t.Helper()
	ctx := context.Background()
	member, err := db.UpsertOIDCUser(ctx, "agenthub-mcp-sso-test:"+status, "mcp-sso-"+status, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if member.Status != status {
		if member, err = db.UpdateUserGovernance(ctx, member.ID, member.Role, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	return member
}

func callMCP(handler http.Handler, bearer string) *httptest.ResponseRecorder {
	return callMCPWith(handler, bearer, mcpListTools)
}

func callMCPWith(handler http.Handler, bearer, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "localhost:8080"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func getMetadata(handler http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

// Off by default: a deployment that has not switched SSO on advertises
// nothing, and a token is refused with the words a bad key gets.
func TestMCPSSOIsOffUntilAnAdministratorTurnsItOn(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{})
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		if got := getMetadata(handler, path); got.Code != http.StatusNotFound {
			t.Errorf("%s is served with SSO off: %d %s", path, got.Code, got.Body.String())
		}
	}
	refusal := callMCP(handler, "")
	if refusal.Code != http.StatusUnauthorized || strings.Contains(refusal.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("with SSO off the challenge pointed at metadata: %d %q", refusal.Code, refusal.Header().Get("WWW-Authenticate"))
	}
	member := registeredMember(t, db, "active")
	token := provider.accessToken(t, "agenthub-mcp-sso-test:active", "http://localhost:8080/mcp", map[string]any{"preferred_username": member.Username})
	refused := callMCP(handler, token)
	if refused.Code != http.StatusUnauthorized || !strings.Contains(refused.Body.String(), "API Key가 유효하지 않습니다") {
		t.Fatalf("with SSO off a token was answered %d %s; it should be refused exactly as a bad key is", refused.Code, refused.Body.String())
	}
	// And the key path is untouched by the shape test.
	_, key, err := db.CreateAPIKey(context.Background(), member.ID, "mcp sso test", []string{ScopeMCP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened := callMCP(handler, key); opened.Code != http.StatusOK || !strings.Contains(opened.Body.String(), "agenthub_list_agents") {
		t.Fatalf("a key was refused: %d %s", opened.Code, opened.Body.String())
	}
}

// With SSO on, a refused client is told where to sign in, and the metadata
// names this resource and the configured issuer as a bare document.
func TestMCPSSORefusalPointsAtTheMetadata(t *testing.T) {
	handler, _, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true})

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		got := getMetadata(handler, path)
		if got.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, got.Code, got.Body.String())
		}
		if got.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s is not readable from a browser-based client", path)
		}
		var metadata struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			BearerMethods        []string `json:"bearer_methods_supported"`
			Scopes               []string `json:"scopes_supported"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &metadata); err != nil {
			t.Fatalf("%s is not a bare document: %s", path, got.Body.String())
		}
		if metadata.Resource != "http://localhost:8080/mcp" {
			t.Errorf("resource %q, want the public URL plus the MCP path", metadata.Resource)
		}
		if len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != provider.server.URL {
			t.Errorf("authorization_servers %v, want the configured issuer", metadata.AuthorizationServers)
		}
		if len(metadata.BearerMethods) != 1 || metadata.BearerMethods[0] != "header" || len(metadata.Scopes) == 0 {
			t.Errorf("bearer methods %v, scopes %v", metadata.BearerMethods, metadata.Scopes)
		}
	}

	refusal := callMCP(handler, "")
	header := refusal.Header().Get("WWW-Authenticate")
	if refusal.Code != http.StatusUnauthorized || !strings.HasPrefix(header, "Bearer ") ||
		!strings.Contains(header, `resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("the MCP 401 does not point at the metadata: %d %q", refusal.Code, header)
	}
	if strings.Contains(header, "invalid_token") {
		t.Errorf("no token was presented, yet the challenge says invalid_token: %q", header)
	}
	refusedToken := callMCP(handler, "a.b.c")
	if got := refusedToken.Header().Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) || !strings.Contains(got, "resource_metadata") {
		t.Errorf("a refused token's challenge: %q", got)
	}

	// A REST 401 carries none of it: a browser or REST client sent to an
	// authorization server it knows nothing about goes somewhere wrong.
	rest := httptest.NewRecorder()
	handler.ServeHTTP(rest, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if rest.Code != http.StatusUnauthorized || rest.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("the REST 401 carries a challenge: %d %q", rest.Code, rest.Header().Get("WWW-Authenticate"))
	}
}

// A token the realm issued for this resource opens MCP for the account that
// signed in to the web before — and for nobody else.
func TestMCPSSOOpensForAnAccountThePlatformKnows(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true, Audience: "claude-mcp"})
	member := registeredMember(t, db, "active")

	// The mapper path: aud names the resource.
	opened := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:active", "http://localhost:8080/mcp", nil))
	if opened.Code != http.StatusOK || !strings.Contains(opened.Body.String(), "agenthub_list_agents") {
		t.Fatalf("a token for this resource was refused: %d %s", opened.Code, opened.Body.String())
	}
	// The default ceiling is read-only: nothing that writes is offered.
	if strings.Contains(opened.Body.String(), "agenthub_queue_task") || strings.Contains(opened.Body.String(), "agenthub_runtime_action") {
		t.Errorf("an SSO principal under the default scopes is offered a writing tool: %s", opened.Body.String())
	}

	// The plain path, as a real Keycloak 26 mints it: aud=["account"], the
	// client in azp, and the administrator has listed that client.
	viaClient := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:active", "account", map[string]any{"azp": "claude-mcp"}))
	if viaClient.Code != http.StatusOK {
		t.Errorf("a token issued to the listed client was refused: %d %s", viaClient.Code, viaClient.Body.String())
	}

	// The subject is the only link, as it is for the web sign-in. A token
	// whose username claim happens to name a registered account — a local
	// administrator's, say — does not become that account.
	byName := callMCP(handler, provider.accessToken(t, "some-other-subject", "http://localhost:8080/mcp", map[string]any{"preferred_username": member.Username}))
	if byName.Code != http.StatusUnauthorized {
		t.Errorf("a token naming a registered username (but an unknown subject) was answered %d %s", byName.Code, byName.Body.String())
	}

	// Somebody the platform has never seen is refused, and nothing is created
	// for them: a token is not the moment to decide who somebody is.
	before := countUsers(t, db)
	stranger := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:stranger", "http://localhost:8080/mcp", map[string]any{"preferred_username": "mcp-sso-stranger"}))
	if stranger.Code != http.StatusUnauthorized || !strings.Contains(stranger.Body.String(), "먼저 웹으로") {
		t.Errorf("an unregistered subject was answered %d %s", stranger.Code, stranger.Body.String())
	}
	if after := countUsers(t, db); after != before {
		t.Errorf("an unregistered subject's token created an account (%d → %d users)", before, after)
	}

	// A disabled account stays disabled however valid the token.
	disabled := registeredMember(t, db, "disabled")
	if got := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:disabled", "http://localhost:8080/mcp", map[string]any{"preferred_username": disabled.Username})); got.Code != http.StatusUnauthorized {
		t.Errorf("a disabled account's token opened MCP: %d %s", got.Code, got.Body.String())
	}

	// And a valid token opens nothing but /mcp.
	rest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	rest.Header.Set("Authorization", "Bearer "+provider.accessToken(t, "agenthub-mcp-sso-test:active", "http://localhost:8080/mcp", nil))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, rest)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("a valid MCP token opened a REST route: %d %s", recorder.Code, recorder.Body.String())
	}
}

// A token minted for another application in the realm is not ours, however
// real its signature — and the refusal says what it saw and what to write.
func TestMCPSSORefusesATokenForAnotherApplication(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true})
	registeredMember(t, db, "active")
	other := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:active", "account", map[string]any{"azp": "some-other-app"}))
	if other.Code != http.StatusUnauthorized {
		t.Fatalf("a token issued to another application opened MCP: %d %s", other.Code, other.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(other.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aud=[account]", `azp="some-other-app"`, `허용 대상에 "some-other-app"`, `매퍼로 "http://localhost:8080/mcp"`} {
		if !strings.Contains(body.Error.Message, want) {
			t.Errorf("the refusal does not tell the operator %q: %s", want, body.Error.Message)
		}
	}
}

// Expired, another issuer, an ID token, a shared-secret signature, a bound
// token, one not yet valid: each refused on its own.
func TestMCPSSORefusesWhatIsNotAnAccessTokenForThisServer(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true})
	registeredMember(t, db, "active")
	resource := "http://localhost:8080/mcp"
	subject := "agenthub-mcp-sso-test:active"
	now := time.Now()

	otherIssuer := newStandInProvider(t, "another-realm")
	hs256 := func() string {
		header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
		claims, _ := json.Marshal(map[string]any{"iss": provider.server.URL, "sub": subject, "aud": resource, "typ": "Bearer", "exp": now.Add(time.Hour).Unix()})
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		mac := hmac.New(sha256.New, []byte("shared-secret"))
		mac.Write([]byte(input))
		return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	cases := map[string]string{
		"expired":        provider.accessToken(t, subject, resource, map[string]any{"exp": now.Add(-time.Minute).Unix()}),
		"another issuer": otherIssuer.accessToken(t, subject, resource, nil),
		"ID token":       provider.accessToken(t, subject, resource, map[string]any{"typ": "ID"}),
		"HS256":          hs256(),
		"cnf-bound":      provider.accessToken(t, subject, resource, map[string]any{"cnf": map[string]string{"jkt": "thumbprint"}}),
		"not yet valid":  provider.accessToken(t, subject, resource, map[string]any{"nbf": now.Add(time.Hour).Unix()}),
		"no subject":     provider.accessToken(t, "", resource, nil),
		"unsigned":       "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.x",
	}
	for name, token := range cases {
		if got := callMCP(handler, token); got.Code != http.StatusUnauthorized {
			t.Errorf("%s: answered %d %s", name, got.Code, got.Body.String())
		}
	}
	// The control: the same subject with a proper token is let in, so the
	// refusals above are about the tokens and not the deployment.
	if got := callMCP(handler, provider.accessToken(t, subject, resource, nil)); got.Code != http.StatusOK {
		t.Fatalf("the control token was refused: %d %s", got.Code, got.Body.String())
	}
}

// The administrator's scopes are the ceiling; a wider setting opens the tools
// a key with those scopes would see, and the token's own scope claim can only
// narrow it.
func TestMCPSSOScopesAreTheAdministratorsCeiling(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true, Scopes: ScopeMCP + " " + ScopeWrite})
	registeredMember(t, db, "active")
	resource := "http://localhost:8080/mcp"
	wide := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:active", resource, nil))
	if wide.Code != http.StatusOK || !strings.Contains(wide.Body.String(), "agenthub_queue_task") {
		t.Fatalf("a ceiling with agent:write does not offer the writing tool: %d %s", wide.Code, wide.Body.String())
	}
	if strings.Contains(wide.Body.String(), "agenthub_runtime_action") {
		t.Errorf("a tool outside the ceiling is offered: %s", wide.Body.String())
	}
	narrowed := callMCP(handler, provider.accessToken(t, "agenthub-mcp-sso-test:active", resource, map[string]any{"scope": "openid mcp:read"}))
	if narrowed.Code != http.StatusOK || strings.Contains(narrowed.Body.String(), "agenthub_queue_task") {
		t.Errorf("a token asking only for mcp:read was given agent:write: %d %s", narrowed.Code, narrowed.Body.String())
	}
}

// The switch alone is not enough: with OIDC not configured the row is on and
// the deployment behaves as if it were off, so no client is sent into a login
// loop by metadata that exists while every token is refused.
func TestMCPSSOStaysOffWhileOIDCIsNotConfigured(t *testing.T) {
	handler, db, _ := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true})
	ctx := context.Background()
	operator, _ := db.UpsertOIDCUser(ctx, "agenthub-silent-sso-test:operator", "silent-sso-operator", "", "", true)
	var previous json.RawMessage
	_ = db.Setting(ctx, "authentication", &previous)
	previousSecret, _ := db.SettingSecret(ctx, "authentication")
	t.Cleanup(func() { _ = db.PutSetting(ctx, "authentication", previous, &previousSecret, operator.ID) })
	if err := db.PutSetting(ctx, "authentication", authSettings{LocalLoginEnabled: true}, nil, operator.ID); err != nil {
		t.Fatal(err)
	}
	if got := getMetadata(handler, "/.well-known/oauth-protected-resource/mcp"); got.Code != http.StatusNotFound {
		t.Errorf("metadata is served with no issuer to point at: %d %s", got.Code, got.Body.String())
	}
	if got := callMCP(handler, ""); strings.Contains(got.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Errorf("the challenge points at metadata that answers 404: %q", got.Header().Get("WWW-Authenticate"))
	}
}

// The admin route refuses a row the deployment could not honour, where the
// form can say why, rather than storing it and logging at request time.
func TestMCPSSOSettingsAreRefusedWhenTheyCannotWork(t *testing.T) {
	handler, db, _ := mcpSSODeployment(t, mcpOAuthSettings{})
	ctx := context.Background()
	operator, _ := db.UpsertOIDCUser(ctx, "agenthub-silent-sso-test:operator", "silent-sso-operator", "", "", true)
	session, csrf, _, err := db.CreateSession(ctx, operator.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	put := func(value map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"value": value})
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/"+mcpOAuthSettingKey, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if got := put(map[string]any{"enabled": true, "resource": "https://agenthub.example.test/api"}); got.Code != http.StatusBadRequest {
		t.Errorf("a resource that is not the MCP endpoint was saved: %d %s", got.Code, got.Body.String())
	}
	if got := put(map[string]any{"enabled": true, "scopes": "mcp:read admin:everything"}); got.Code != http.StatusBadRequest {
		t.Errorf("an unknown scope was saved: %d %s", got.Code, got.Body.String())
	}
	if got := put(map[string]any{"enabled": true, "resource": "https://agenthub.example.test/mcp", "audience": "claude-mcp", "scopes": "mcp:read"}); got.Code != http.StatusOK {
		t.Errorf("a proper row was refused: %d %s", got.Code, got.Body.String())
	}
	var saved mcpOAuthSettings
	if err := db.Setting(ctx, mcpOAuthSettingKey, &saved); err != nil || !saved.Enabled || saved.Audience != "claude-mcp" {
		t.Errorf("the row did not land as written: %+v %v", saved, err)
	}
}

// Two doors into the same room leave the same kind of entry, and the trail
// says which door: a call under a key is filed auth=key, a call under an SSO
// token auth=oauth with the client (azp) that presented it. The entries are
// read back from audit_events, because what an operator filters is what the
// store wrote, not what the handler meant to write.
func TestTheTrailSaysWhichDoorAToolCallCameThrough(t *testing.T) {
	handler, db, provider := mcpSSODeployment(t, mcpOAuthSettings{Enabled: true, Audience: "claude-mcp"})
	ctx := context.Background()
	member := registeredMember(t, db, "active")
	const listAgents = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agenthub_list_agents","arguments":{}}}`

	// The newest mcp.tool_call entry for this tool — the one the call just made.
	newest := func(t *testing.T) map[string]any {
		t.Helper()
		page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "mcp.tool_call", ResourceID: "agenthub_list_agents", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatal("the call left no mcp.tool_call entry")
		}
		entry := page.Items[0]
		if entry["actor"] != member.Username {
			t.Fatalf("the newest entry is somebody else's (%q, want %q)", entry["actor"], member.Username)
		}
		details, _ := entry["details"].(map[string]any)
		return details
	}

	_, key, err := db.CreateAPIKey(ctx, member.ID, "trail", []string{ScopeMCP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := callMCPWith(handler, key, listAgents); got.Code != http.StatusOK || strings.Contains(got.Body.String(), `"isError":true`) {
		t.Fatalf("the call under a key failed: %d %s", got.Code, got.Body.String())
	}
	byKey := newest(t)
	if byKey["tool"] != "agenthub_list_agents" || byKey["auth"] != "key" {
		t.Errorf("a call under a key is filed as %v; want tool=agenthub_list_agents auth=key", byKey)
	}
	if _, present := byKey["client"]; present {
		t.Errorf("a call under a key names a client: %v", byKey)
	}

	// As a real Keycloak 26 mints it: aud=["account"], the client in azp.
	viaClient := provider.accessToken(t, "agenthub-mcp-sso-test:active", "account", map[string]any{"azp": "claude-mcp"})
	if got := callMCPWith(handler, viaClient, listAgents); got.Code != http.StatusOK || strings.Contains(got.Body.String(), `"isError":true`) {
		t.Fatalf("the call under an SSO token failed: %d %s", got.Code, got.Body.String())
	}
	bySSO := newest(t)
	if bySSO["tool"] != "agenthub_list_agents" || bySSO["auth"] != "oauth" || bySSO["client"] != "claude-mcp" {
		t.Errorf("a call under an SSO token is filed as %v; want auth=oauth client=claude-mcp", bySSO)
	}

	// The mapper path carries no azp: the door is still named, the client is not
	// invented — no empty "client" key.
	viaMapper := provider.accessToken(t, "agenthub-mcp-sso-test:active", "http://localhost:8080/mcp", nil)
	if got := callMCPWith(handler, viaMapper, listAgents); got.Code != http.StatusOK || strings.Contains(got.Body.String(), `"isError":true`) {
		t.Fatalf("the call under a mapper token failed: %d %s", got.Code, got.Body.String())
	}
	byMapper := newest(t)
	if byMapper["auth"] != "oauth" {
		t.Errorf("a call under a mapper token is filed as %v; want auth=oauth", byMapper)
	}
	if _, present := byMapper["client"]; present {
		t.Errorf("a token without azp is filed with a client: %v", byMapper)
	}

	// Nothing that could identify the token itself is in the entry.
	for _, entry := range []map[string]any{byKey, bySSO, byMapper} {
		raw, _ := json.Marshal(entry)
		for _, secret := range []string{key, viaClient, viaMapper, "agenthub-mcp-sso-test:active"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("the entry carries the credential or subject: %s", raw)
			}
		}
	}
}

func countUsers(t *testing.T, db *store.Store) int {
	t.Helper()
	users, err := db.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(users)
}
