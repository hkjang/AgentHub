package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// The webhook route is the only unauthenticated endpoint, so its signature check
// is the entire access control for it.
func TestWebhookSignatureVerification(t *testing.T) {
	secret := "trigger-secret"
	body := []byte(`{"issue":"INC-1023"}`)

	if !validSignature(secret, body, sign(secret, body)) {
		t.Fatal("a correct signature must be accepted")
	}
	if !validSignature(secret, body, hex.EncodeToString(hmacSum(secret, body))) {
		t.Fatal("the sha256= prefix must be optional")
	}
	for name, header := range map[string]string{
		"empty":            "",
		"whitespace":       "   ",
		"not hex":          "sha256=zzzz",
		"wrong secret":     sign("other-secret", body),
		"different body":   sign(secret, []byte(`{"issue":"INC-9999"}`)),
		"truncated digest": "sha256=" + hex.EncodeToString(hmacSum(secret, body))[:20],
	} {
		if validSignature(secret, body, header) {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func hmacSum(secret string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return mac.Sum(nil)
}

func TestValidateGoalFillsDefaultsAndRejectsBadLimits(t *testing.T) {
	goal := store.AgentGoal{AgentID: "agent-1"}
	if err := validateGoal(&goal); err != nil {
		t.Fatalf("an empty goal must be usable via defaults: %v", err)
	}
	if goal.MaxSteps == 0 || goal.CompletionStrategy == "" || goal.ConcurrencyPolicy == "" {
		t.Fatalf("defaults were not applied: %#v", goal)
	}

	for name, mutate := range map[string]func(*store.AgentGoal){
		"steps above the limit":    func(g *store.AgentGoal) { g.MaxSteps = 1000 },
		"duration below the floor": func(g *store.AgentGoal) { g.MaxDurationSeconds = 5 },
		"negative retries":         func(g *store.AgentGoal) { g.MaxRetries = -1 },
		"unknown strategy":         func(g *store.AgentGoal) { g.CompletionStrategy = "vibes" },
		"unknown concurrency":      func(g *store.AgentGoal) { g.ConcurrencyPolicy = "whenever" },
	} {
		candidate := store.DefaultAgentGoal("agent-1")
		mutate(&candidate)
		if err := validateGoal(&candidate); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

// A rule-based strategy with nothing to check would silently pass everything.
func TestRuleStrategyRequiresSuccessCriteria(t *testing.T) {
	goal := store.DefaultAgentGoal("agent-1")
	goal.CompletionStrategy = "rule"
	if err := validateGoal(&goal); err == nil {
		t.Fatal("rule completion without criteria must be rejected")
	}
	goal.SuccessCriteria = []string{"보고서 저장 완료"}
	if err := validateGoal(&goal); err != nil {
		t.Fatalf("rule completion with criteria must be accepted: %v", err)
	}
}

func TestExecutionModeAllowsOnlyKnownValues(t *testing.T) {
	for _, mode := range []string{"interactive", "task", "scheduled", "event", "service", "hybrid"} {
		if !validExecutionMode(mode) {
			t.Errorf("%q must be a valid execution mode", mode)
		}
	}
	for _, mode := range []string{"", "auto", "Interactive", "batch"} {
		if validExecutionMode(mode) {
			t.Errorf("%q must be rejected", mode)
		}
	}
}

// A screenshot stored as base64 has to come back as an image somebody can look
// at. Served the way every other artifact is served, it downloads a file of
// letters with a .png name — which looks like the feature works right up until
// somebody opens it.
func TestAStoredScreenshotIsServedAsAPicture(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake but binary")
	item := store.AgentArtifact{Type: "image", ContentType: "image/png", Content: base64.StdEncoding.EncodeToString(png)}
	raw, ok := decodedImage(item)
	if !ok || string(raw) != string(png) {
		t.Fatalf("decoded = %q, ok = %v", raw, ok)
	}
}

// SVG is a document that can carry script. Serving one inline in the portal's
// origin is exactly what the download rule exists to prevent, and it must not
// become an exception because somebody labelled it an image.
func TestAnSVGIsNeverServedInline(t *testing.T) {
	item := store.AgentArtifact{Type: "image", ContentType: "image/svg+xml",
		Content: base64.StdEncoding.EncodeToString([]byte(`<svg onload="alert(1)"/>`))}
	if _, ok := decodedImage(item); ok {
		t.Error("an SVG would have been served inline")
	}
}

// An older 'image' artifact was written as text, not base64. It must fall back to
// the download path rather than being served as a picture that does not decode.
func TestAnImageThatIsNotBase64FallsBackToDownload(t *testing.T) {
	if _, ok := decodedImage(store.AgentArtifact{Type: "image", ContentType: "image/png", Content: "not base64 at all!"}); ok {
		t.Error("non-base64 content was served as a picture")
	}
	if _, ok := decodedImage(store.AgentArtifact{Type: "report", ContentType: "text/markdown", Content: "aGk="}); ok {
		t.Error("a report was served as a picture")
	}
}
