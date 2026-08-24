package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// How the forges prove a delivery is theirs.
//
// The platform accepted one header, X-AgentHub-Signature, so a site pointing
// GitHub straight at a trigger got 401 and had to run a small proxy whose only
// job was renaming a header. That proxy is also where the body had to be
// translated — and now that a forge's own body is understood, the header is the
// last reason the proxy still exists.
//
// Every scheme below is the one that forge documents. Nothing weaker is accepted
// and nothing is inferred: an unsigned request stays unauthorized.
type webhookCredential struct {
	// deliveryKey identifies this exact delivery, for the replay ledger.
	deliveryKey string
	// legacyKey is the header as it was sent, when that differs from
	// deliveryKey. Deliveries claimed before the key was canonicalised are
	// recorded under this spelling, and a replay of one must still be refused.
	legacyKey string
}

// authorizeWebhook checks a delivery against the trigger's secret.
//
// The delivery key is always computed from the body, never taken from the
// header. GitLab's scheme sends a bare shared token, identical on every
// delivery: keyed on that, the first request would be accepted and every request
// after it refused as a replay for as long as the ledger kept the row.
func authorizeWebhook(secret string, body []byte, header http.Header) (webhookCredential, bool) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	digest := mac.Sum(nil)
	credential := webhookCredential{deliveryKey: hex.EncodeToString(digest)}

	// AgentHub's own header first, then the forges', all the same scheme:
	// hex HMAC-SHA256 over the raw body, with or without a `sha256=` prefix.
	for _, name := range []string{
		"X-AgentHub-Signature",
		"X-Hub-Signature-256", // GitHub
		"X-Gitea-Signature",   // Gitea, Forgejo
	} {
		sent := strings.TrimSpace(header.Get(name))
		if sent == "" {
			continue
		}
		provided, err := hex.DecodeString(strings.TrimPrefix(sent, "sha256="))
		if err != nil || !hmac.Equal(provided, digest) {
			return webhookCredential{}, false
		}
		if sent != credential.deliveryKey {
			credential.legacyKey = sent
		}
		return credential, true
	}

	// GitLab sends the secret itself. It is a weaker proof — nothing binds it to
	// the body — which is why the delivery key is computed rather than taken
	// from it, and why it is checked last.
	if token := strings.TrimSpace(header.Get("X-Gitlab-Token")); token != "" {
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
			return credential, true
		}
		return webhookCredential{}, false
	}
	return webhookCredential{}, false
}
