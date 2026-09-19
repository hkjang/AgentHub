package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/hkjang/AgentHub/internal/store"
)

// MCP 를 SSO 로 — 개인 키 없이, Keycloak 이 발급한 토큰으로.
//
// The MCP authorization specification (2025-06-18 and later) is OAuth 2.1: the
// MCP server is a *resource server* that publishes where its authorization
// server is (RFC 9728, /.well-known/oauth-protected-resource), and a client
// refused with 401 reads that document, sends the person through the
// authorization server with PKCE, and comes back with an access token whose
// audience (RFC 8707) is this server. Nothing about issuing tokens happens here
// — Keycloak does that, and this file has only two questions to answer: where
// is the authorization server, and is this token one it issued for us.
//
// The personal key stays. It is what an automation with no person behind it
// uses, and what a deployment without Keycloak uses. A token from SSO is a
// second door into the same room: it authenticates an *existing* account,
// carries the scopes the administrator chose, and walks the same scope checks a
// key does. It never creates an account — signing in to the web once is what
// registers a person, and a machine presenting a token is not the moment to
// decide who somebody is. It is accepted on /mcp only: a token that opened the
// whole API would make this "a new door into everything", not "MCP without a
// key".

// mcpOAuthSettingKey is the system_settings row. Its fields spell out to the
// names the company standard uses (mcp.oauth.enabled, mcp.oauth.resource,
// mcp.oauth.audience, mcp.oauth.scopes), the way the mail row's do.
const mcpOAuthSettingKey = "mcp.oauth"

// mcpOAuthDefaultScopes is what an SSO principal may do unless the
// administrator says otherwise: the same read-only scope a key made for MCP
// gets by default.
const mcpOAuthDefaultScopes = ScopeMCP

type mcpOAuthSettings struct {
	// Enabled is off by default. A fresh install advertises nothing and refuses
	// a token exactly as it refuses a bad key.
	Enabled bool `json:"enabled"`
	// Resource is the identifier this server claims (RFC 8707): the public
	// address clients actually connect to plus the MCP path. Empty means it is
	// built from general.publicUrl.
	Resource string `json:"resource"`
	// Audience is a space-separated list of additional accepted aud/azp values.
	// A real Keycloak 26 puts the client id in azp and only "account" in aud, so
	// naming the MCP client here is the way that works without a mapper.
	Audience string `json:"audience"`
	// Scopes, space-separated, is the ceiling for an SSO principal. It is the
	// administrator's setting rather than the token's scope claim so nobody has
	// to teach Keycloak this platform's vocabulary before MCP works.
	Scopes string `json:"scopes"`
}

// mcpOAuthConfig is the settings row resolved against the web sign-in's OIDC
// settings and the deployment's public address — everything a request needs,
// read once per request.
type mcpOAuthConfig struct {
	Enabled  bool
	Issuer   string
	ClientID string
	// Resource is empty when neither the setting nor the public URL is set; the
	// request's Host is then the last resort (mcpResource).
	Resource  string
	Audiences []string
	Scopes    []string
	// Reason says why Enabled is false although the switch is on, for the log
	// and the settings screen.
	Reason string
}

func (s *Server) mcpOAuthConfig(ctx context.Context) mcpOAuthConfig {
	var settings mcpOAuthSettings
	_ = s.store.Setting(ctx, mcpOAuthSettingKey, &settings)
	var auth authSettings
	_ = s.store.Setting(ctx, "authentication", &auth)
	var general generalSettings
	_ = s.store.Setting(ctx, "general", &general)

	config := mcpOAuthConfig{
		Issuer:    strings.TrimRight(strings.TrimSpace(auth.IssuerURL), "/"),
		ClientID:  strings.TrimSpace(auth.ClientID),
		Resource:  strings.TrimSpace(settings.Resource),
		Audiences: strings.Fields(settings.Audience),
		Scopes:    strings.Fields(settings.Scopes),
	}
	if config.Resource == "" && strings.TrimSpace(general.PublicURL) != "" {
		config.Resource = strings.TrimRight(strings.TrimSpace(general.PublicURL), "/") + "/mcp"
	}
	if len(config.Scopes) == 0 {
		config.Scopes = strings.Fields(mcpOAuthDefaultScopes)
	}
	switch {
	case !settings.Enabled:
		config.Reason = "mcp.oauth.enabled is off"
	case !auth.OIDCEnabled || config.Issuer == "":
		config.Reason = "oidc is not configured (authentication.oidcEnabled, authentication.issuerUrl)"
	default:
		config.Enabled = true
	}
	return config
}

// mcpResource is the identifier this deployment claims for its MCP endpoint:
// what the metadata document advertises and what a token's aud may name. The
// request's Host is used only when nothing is configured — anybody can set a
// Host header, which is why the setting and the public URL come first.
func (c mcpOAuthConfig) mcpResource(r *http.Request) string {
	if c.Resource != "" {
		return c.Resource
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp"
}

// metadataURL is where a refused client is sent to learn the above.
func (c mcpOAuthConfig) metadataURL(r *http.Request) string {
	return strings.TrimSuffix(c.mcpResource(r), "/mcp") + "/.well-known/oauth-protected-resource/mcp"
}

// oauthProviders caches discovery per issuer. Discovery is a round trip to
// Keycloak and the JWKS behind it verifies every token; doing that per request
// would put Keycloak's latency in front of every MCP call. go-oidc refetches
// the key set on an unknown key id, so key rotation needs no invalidation here.
type oauthProviders struct {
	mu       sync.Mutex
	byIssuer map[string]*oidc.Provider
}

func (s *Server) oauthProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	if s.oauth.byIssuer == nil {
		s.oauth.byIssuer = map[string]*oidc.Provider{}
	}
	if provider := s.oauth.byIssuer[issuer]; provider != nil {
		return provider, nil
	}
	// Discovery must outlive this request: the provider keeps the context for
	// later key fetches.
	provider, err := oidc.NewProvider(context.WithoutCancel(ctx), issuer)
	if err != nil {
		return nil, err
	}
	s.oauth.byIssuer[issuer] = provider
	return provider, nil
}

// apiKeyPrefix is what every key this platform issues starts with
// (store.CreateAPIKey). A bearer with it is a key whatever else it looks like.
const apiKeyPrefix = "ahk_"

// looksLikeJWT is the cheap shape test that separates a key from a token:
// three non-empty dot-separated parts. A bearer that is neither is refused as
// a bad key, the way it always was, so a deployment with SSO off says nothing
// new.
func looksLikeJWT(token string) bool {
	if strings.HasPrefix(token, apiKeyPrefix) {
		return false
	}
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// mcpRefusal is why a bearer was not accepted: the code and message the client
// sees, and the fact that a token (rather than nothing) was presented, which
// decides whether the challenge says error="invalid_token".
type mcpRefusal struct {
	code    string
	message string
}

func (e *mcpRefusal) Error() string { return e.code + ": " + e.message }

// oauthAccessClaims are the claims go-oidc does not check on its own. Its
// verifier covers signature, issuer and exp; the rest of the table in the
// standard is here.
type oauthAccessClaims struct {
	Type         string          `json:"typ"`
	AZP          string          `json:"azp"`
	Scope        string          `json:"scope"`
	NotBefore    int64           `json:"nbf"`
	Confirmation json.RawMessage `json:"cnf"`
}

// oauthClockSkew is how far a token's nbf may sit in the future before it is
// refused. Keycloak and this server keep their own clocks.
const oauthClockSkew = time.Minute

// refuseAccessClaims applies the checks that decide whether an otherwise valid
// JWT is an access token this server may act on.
func refuseAccessClaims(claims oauthAccessClaims, now time.Time) *mcpRefusal {
	// An ID token is proof that somebody signed in, not a credential for an
	// API. Keycloak marks its access tokens typ=Bearer and its ID tokens typ=ID.
	if strings.EqualFold(strings.TrimSpace(claims.Type), "ID") {
		return &mcpRefusal{"invalid_token", "ID 토큰은 MCP 자격이 아닙니다. Keycloak 이 발급한 액세스 토큰을 보내세요."}
	}
	// cnf binds the token to a key or certificate this server cannot verify
	// (DPoP, mTLS). Accepting it as a plain bearer would drop that binding.
	if len(claims.Confirmation) > 0 && string(claims.Confirmation) != "null" {
		return &mcpRefusal{"invalid_token", "소지자 증명(cnf)이 묶인 토큰은 받지 않습니다. 일반 Bearer 액세스 토큰을 보내세요."}
	}
	if claims.NotBefore > 0 && time.Unix(claims.NotBefore, 0).After(now.Add(oauthClockSkew)) {
		return &mcpRefusal{"invalid_token", "SSO 액세스 토큰이 아직 유효하지 않습니다(nbf). 서버 시계를 확인하거나 잠시 후 다시 시도하세요."}
	}
	return nil
}

// acceptedAudience reports whether the token was minted for this deployment.
//
// Measured against a real Keycloak 26: an access token issued to a client
// carries that client in azp and aud: ["account"] — the client id is *not* in
// aud, whatever an ID token does. So the binding checked here is "aud names our
// resource, or aud/azp is a client the administrator trusts". Either is the
// token being for this deployment rather than passed through from some other
// application in the realm, which is what RFC 8707 and the MCP specification
// guard against. The administrator's list applies to both, so the plain path —
// put the MCP client's id in the list — needs no mapper at all.
func acceptedAudience(resource string, audiences []string, tokenAudience []string, azp string) bool {
	accepted := append([]string{resource}, audiences...)
	bound := append(slices.Clone(tokenAudience), azp)
	return slices.ContainsFunc(bound, func(value string) bool { return value != "" && slices.Contains(accepted, value) })
}

// grantedScopes is what an SSO principal may do: the administrator's ceiling,
// narrowed to what the token itself asks for when it speaks this platform's
// vocabulary. A Keycloak token normally carries "openid profile email" and
// nothing of ours, and then the ceiling applies as it is.
func grantedScopes(ceiling []string, tokenScope string) []string {
	var asked []string
	for _, scope := range strings.Fields(tokenScope) {
		if slices.Contains(APIKeyScopes, scope) {
			asked = append(asked, scope)
		}
	}
	if len(asked) == 0 {
		return slices.Clone(ceiling)
	}
	granted := []string{}
	for _, scope := range ceiling {
		if slices.Contains(asked, scope) {
			granted = append(granted, scope)
		}
	}
	return granted
}

// mcpPrincipal is who is calling /mcp and through which door. Both doors —
// the key this platform issued and the token Keycloak issued — resolve to
// this one value, so everything after the bearer check reads the same thing.
type mcpPrincipal struct {
	user   store.User
	scopes []string
	// auth is mcpAuthKey or mcpAuthOAuth: which credential opened the door.
	auth string
	// client is the OAuth client that presented the token (azp), empty under a
	// key or a token minted without one. A public identifier — the value an
	// administrator writes in the audience list — never the token itself.
	client string
}

const (
	mcpAuthKey   = "key"
	mcpAuthOAuth = "oauth"
)

// oauthPrincipal turns a bearer access token into a user and scopes, or says
// exactly why it will not. The message is for the operator reading the
// client's error: a refused audience names what the token carried and what
// to write, because that one line is the whole configuration.
func (s *Server) oauthPrincipal(ctx context.Context, r *http.Request, token string) (mcpPrincipal, *mcpRefusal) {
	config := s.mcpOAuthConfig(ctx)
	if !config.Enabled {
		if config.Reason != "mcp.oauth.enabled is off" {
			s.logger.Warn("mcp oauth is switched on but cannot run", "reason", config.Reason)
		}
		// The same words a bad key gets: a deployment with SSO off says nothing
		// about doors it does not have.
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "API Key가 유효하지 않습니다."}
	}
	provider, err := s.oauthProvider(ctx, config.Issuer)
	if err != nil {
		s.logger.Warn("mcp oauth discovery", "issuer", config.Issuer, "error", err)
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "Keycloak 발급자 정보를 읽지 못해 SSO 토큰을 확인할 수 없습니다. 잠시 후 다시 시도하거나 관리자에게 알리세요."}
	}
	// Signature, issuer and expiry. Only asymmetric algorithms: a token signed
	// with a shared secret (HS*) or not at all is refused before its claims are
	// read. The audience is checked below by hand because more than one value
	// is acceptable and the library compares one, and knows nothing of azp.
	verified, err := provider.Verifier(&oidc.Config{
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512},
	}).Verify(ctx, token)
	if err != nil {
		s.logger.Info("mcp oauth token refused", "error", err)
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "SSO 액세스 토큰이 유효하지 않습니다(서명·발급자·만료). 클라이언트에서 다시 로그인하세요."}
	}
	var claims oauthAccessClaims
	if err := verified.Claims(&claims); err != nil {
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "SSO 토큰의 내용을 읽을 수 없습니다."}
	}
	if refusal := refuseAccessClaims(claims, time.Now()); refusal != nil {
		return mcpPrincipal{}, refusal
	}
	resource := config.mcpResource(r)
	if !acceptedAudience(resource, config.Audiences, verified.Audience, claims.AZP) {
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", fmt.Sprintf(
			"SSO 토큰이 이 서버를 위해 발급된 것이 아닙니다(aud=%v, azp=%q). 관리자가 MCP SSO 설정의 허용 대상에 %q 를 더하거나, Keycloak 클라이언트에 Audience 매퍼로 %q 를 넣어야 합니다.",
			verified.Audience, claims.AZP, firstNonEmpty(claims.AZP, "<클라이언트 ID>"), resource)}
	}
	subject := strings.TrimSpace(verified.Subject)
	if subject == "" {
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "SSO 토큰에 사용자 식별자(sub)가 없습니다."}
	}
	// The same link the web sign-in made, without the provisioning half.
	user, err := s.store.ActiveUserForSSOSubject(ctx, subject)
	if errors.Is(err, store.ErrNotFound) {
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "이 SSO 계정은 AgentHub 에 등록되지 않았거나 비활성입니다. 먼저 웹으로 한 번 로그인하세요."}
	}
	if err != nil {
		s.logger.Error("mcp oauth account lookup", "error", err)
		return mcpPrincipal{}, &mcpRefusal{"invalid_token", "SSO 계정을 확인하지 못했습니다. 잠시 후 다시 시도하세요."}
	}
	return mcpPrincipal{user: user, scopes: grantedScopes(config.Scopes, claims.Scope), auth: mcpAuthOAuth, client: strings.TrimSpace(claims.AZP)}, nil
}

// mcpChallenge writes the WWW-Authenticate header that turns a 401 into an
// invitation: with SSO on, the client reads resource_metadata and starts the
// OAuth flow from there; without it a refusal is a dead end. Only /mcp sends
// it — a REST 401 carrying it would send browsers and other clients somewhere
// they have no business going.
func (s *Server) mcpChallenge(w http.ResponseWriter, r *http.Request, tokenPresented bool) {
	fields := []string{`realm="agenthub-mcp"`}
	if tokenPresented {
		fields = append(fields, `error="invalid_token"`)
	} else {
		fields = append(fields, `scope="`+ScopeMCP+`"`)
	}
	if config := s.mcpOAuthConfig(r.Context()); config.Enabled {
		fields = append(fields, fmt.Sprintf(`resource_metadata=%q`, config.metadataURL(r)))
	}
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(fields, ", "))
}

// protectedResourceMetadata is RFC 9728: the document a refused MCP client
// reads to find the authorization server. Public by design — it says where to
// sign in, not who is signed in — and CORS-open because clients that run in a
// browser read it. A bare document, not the product's {error:…} envelope: the
// reader is an OAuth client library.
func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	config := s.mcpOAuthConfig(r.Context())
	if !config.Enabled {
		// Metadata that exists while tokens are refused sends a client into a
		// login loop; off means not here.
		writeError(w, http.StatusNotFound, "mcp_oauth_disabled", "이 서버의 MCP 는 SSO 토큰을 받지 않습니다. API Key(ahk_)를 사용하세요.")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 config.mcpResource(r),
		"authorization_servers":    []string{config.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         config.Scopes,
		"resource_name":            "AgentHub MCP",
	})
}
