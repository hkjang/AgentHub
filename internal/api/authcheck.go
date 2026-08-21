package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Whether single sign-on will actually let anybody in.
//
// This is the setting that can lock a deployment out of itself. An administrator
// points the platform at an issuer, turns local login off, saves, and finds out
// whether the issuer was right by trying to log in — from an account that no
// longer has another way in. The platform already refuses to have both methods
// off at once, which stops the obvious mistake and not this one: OIDC enabled is
// not OIDC working.
//
// So the configuration can be asked, and the same question is asked on the way
// past when somebody turns local login off.

const authCheckTimeout = 8 * time.Second

// authCheckResult is what the identity provider said.
type authCheckResult struct {
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
	// Issuer as the provider states it, which is not always the URL that was
	// typed — a mismatch there fails token validation later with an error that
	// names neither field.
	Issuer string `json:"issuer,omitempty"`
	// Client says whether the id and secret were recognised, when there was a
	// secret to try them with.
	Client string `json:"client,omitempty"`
}

func (s *Server) authenticationCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var auth authSettings
	if err := s.store.Setting(r.Context(), "authentication", &auth); err != nil {
		writeStoreError(w, err)
		return
	}
	secret, _ := s.store.SettingSecret(r.Context(), "authentication")
	result := checkOIDC(r.Context(), auth.IssuerURL, auth.ClientID, secret)
	s.store.Audit(r.Context(), &u, "authentication.check", "settings", "authentication", result.Verdict, clientIP(r),
		map[string]any{"issuerUrl": auth.IssuerURL, "detail": result.Detail})
	writeJSON(w, http.StatusOK, result)
}

// checkOIDC reads the discovery document and, when there is a secret, offers the
// client credentials to the token endpoint to see whether the provider knows
// them.
//
// A provider that recognises the client and refuses the grant is a pass: the
// grant this asks for is not the one the login flow uses, and requiring it to be
// enabled would fail a correctly configured realm.
func checkOIDC(ctx context.Context, issuer, clientID, secret string) authCheckResult {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return authCheckResult{Verdict: "unconfigured", Detail: "Issuer URL이 비어 있습니다."}
	}
	if clientID == "" {
		return authCheckResult{Verdict: "unconfigured", Detail: "Client ID가 비어 있습니다."}
	}
	ctx, cancel := context.WithTimeout(ctx, authCheckTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return authCheckResult{Verdict: "unreachable", Detail: "요청을 만들지 못했습니다: " + shortError(err.Error())}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return authCheckResult{Verdict: "unreachable", Detail: "Issuer에 연결하지 못했습니다: " + shortError(err.Error())}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return authCheckResult{Verdict: "not_found", Detail: "이 주소에 OIDC Discovery 문서가 없습니다(HTTP 404). Keycloak이라면 대개 …/realms/<realm> 까지가 Issuer입니다."}
	}
	if response.StatusCode >= 400 {
		return authCheckResult{Verdict: "error", Detail: "Issuer가 HTTP " + response.Status + " 로 답했습니다."}
	}
	var document struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil || document.Token == "" {
		return authCheckResult{Verdict: "not_oidc", Detail: "주소는 응답하지만 OIDC Discovery 문서로 보이지 않습니다."}
	}
	if document.Issuer != "" && strings.TrimRight(document.Issuer, "/") != issuer {
		// The library validates the issuer inside the token against the one it
		// discovered. A mismatch here becomes a login failure later whose message
		// mentions neither setting.
		return authCheckResult{Verdict: "issuer_mismatch", Issuer: document.Issuer,
			Detail: "이 제공자는 자기 Issuer를 \"" + document.Issuer + "\" 라고 밝힙니다. 설정한 주소와 정확히 같아야 토큰 검증을 통과합니다."}
	}
	result := authCheckResult{Verdict: "ok", Issuer: document.Issuer,
		Detail: "Discovery 문서를 읽었습니다. 로그인·토큰·서명키 엔드포인트가 모두 있습니다."}
	if secret == "" {
		result.Client = "미확인"
		result.Detail += " Client Secret이 저장되어 있지 않아 클라이언트 자격은 확인하지 못했습니다."
		return result
	}
	switch clientVerdict, reason := askTokenEndpoint(ctx, document.Token, clientID, secret); clientVerdict {
	case "rejected":
		result.Verdict = "client_rejected"
		result.Client = "거절됨"
		result.Detail = "Issuer는 정상이지만 제공자가 Client ID/Secret을 거절했습니다(" + reason + ")."
	case "accepted":
		result.Client = "확인됨"
		result.Detail += " Client ID와 Secret도 제공자가 인정했습니다."
	default:
		result.Client = "미확인"
		result.Detail += " 클라이언트 자격은 확인하지 못했습니다(" + reason + ")."
	}
	return result
}

// askTokenEndpoint offers the client credentials and reads which way the
// provider refuses. `invalid_client` is the one answer that means the id or the
// secret is wrong; every other refusal is about the grant, which this deployment
// does not use.
func askTokenEndpoint(ctx context.Context, endpoint, clientID, secret string) (verdict, reason string) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "unknown", shortError(err.Error())
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "unknown", shortError(err.Error())
	}
	defer response.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	switch {
	case response.StatusCode < 400:
		return "accepted", ""
	case body.Error == "invalid_client":
		return "rejected", "invalid_client"
	case body.Error != "":
		return "accepted", body.Error
	}
	return "unknown", "HTTP " + response.Status
}
