package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func forgeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func withHeader(name, value string) http.Header {
	header := http.Header{}
	header.Set(name, value)
	return header
}

// A site should be able to paste this address into GitHub or GitLab and be done.
func TestTheForgesOwnHeadersAreAccepted(t *testing.T) {
	secret := "trigger-secret"
	body := []byte(`{"action":"opened"}`)
	for name, header := range map[string]http.Header{
		"GitHub":   withHeader("X-Hub-Signature-256", "sha256="+forgeSignature(secret, body)),
		"Gitea":    withHeader("X-Gitea-Signature", forgeSignature(secret, body)),
		"GitLab":   withHeader("X-Gitlab-Token", secret),
		"AgentHub": withHeader("X-AgentHub-Signature", "sha256="+forgeSignature(secret, body)),
	} {
		if _, ok := authorizeWebhook(secret, body, header); !ok {
			t.Errorf("%s's own header was rejected", name)
		}
	}
}

func TestAWrongCredentialIsStillRefused(t *testing.T) {
	secret := "trigger-secret"
	body := []byte(`{"action":"opened"}`)
	for name, header := range map[string]http.Header{
		"nothing sent":        {},
		"GitHub wrong secret": withHeader("X-Hub-Signature-256", "sha256="+forgeSignature("other", body)),
		"GitHub other body":   withHeader("X-Hub-Signature-256", "sha256="+forgeSignature(secret, []byte(`{"action":"closed"}`))),
		"Gitea not hex":       withHeader("X-Gitea-Signature", "zzzz"),
		"GitLab wrong token":  withHeader("X-Gitlab-Token", "other"),
		"GitLab empty token":  withHeader("X-Gitlab-Token", "  "),
	} {
		if _, ok := authorizeWebhook(secret, body, header); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}

// GitLab sends the same token on every delivery. Keyed on what was sent, the
// first merge request would be reviewed and every one after it refused as a
// replay — a trigger that works once and then goes quiet without saying why.
func TestGitLabsSecondDeliveryIsNotItsOwnReplay(t *testing.T) {
	secret := "trigger-secret"
	first, _ := authorizeWebhook(secret, []byte(`{"object_attributes":{"iid":1}}`), withHeader("X-Gitlab-Token", secret))
	second, _ := authorizeWebhook(secret, []byte(`{"object_attributes":{"iid":2}}`), withHeader("X-Gitlab-Token", secret))
	if first.deliveryKey == second.deliveryKey {
		t.Fatal("two different merge requests share a delivery key; the second is refused as a replay")
	}
	if first.deliveryKey == secret {
		t.Fatal("the trigger's secret is written into the delivery ledger as the key")
	}
}

// The same body is one delivery however the header spelled it, and the spelling
// that a pre-upgrade ledger recorded is carried so that row still refuses.
func TestOneBodyIsOneDeliveryHoweverItWasSpelled(t *testing.T) {
	secret, body := "trigger-secret", []byte(`{"action":"opened"}`)
	bare := forgeSignature(secret, body)
	prefixed, _ := authorizeWebhook(secret, body, withHeader("X-AgentHub-Signature", "sha256="+bare))
	plain, _ := authorizeWebhook(secret, body, withHeader("X-AgentHub-Signature", bare))
	if prefixed.deliveryKey != plain.deliveryKey {
		t.Fatal("the same body sent with and without the prefix is claimed twice")
	}
	if prefixed.legacyKey != "sha256="+bare {
		t.Fatalf("the spelling an older ledger recorded is not carried: %q", prefixed.legacyKey)
	}
	if plain.legacyKey != "" {
		t.Fatalf("a delivery already keyed canonically claims a second row: %q", plain.legacyKey)
	}
}
