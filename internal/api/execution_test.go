package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
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

// Every write endpoint answered the same sentence to a mistyped field, an
// unknown one, a truncated body and an empty one — true of all four and useful
// for none. Whoever was writing a GitOps document had to bisect their own JSON to
// find the key the platform disliked, and the decoder had known all along.
func TestABadRequestSaysWhatWasWrongWithIt(t *testing.T) {
	type goal struct {
		Description string `json:"description"`
		MaxSteps    int    `json:"maxSteps"`
	}
	decode := func(body string) string {
		var dst goal
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.DisallowUnknownFields()
		return decodeComplaint(decoder.Decode(&dst))
	}
	for _, tc := range []struct{ name, body, want string }{
		{"wrong type", `{"maxSteps":"여섯"}`, "maxSteps 항목의 형식이 올바르지 않습니다(문자열 값을 받았습니다)."},
		{"unknown field", `{"maxSteps":6,"maxStepz":7}`, `받지 않는 항목입니다: "maxStepz". 이름을 확인해 주세요.`},
		{"empty body", ``, "요청 본문이 비어 있습니다."},
		{"truncated", `{"description":"x"`, "JSON이 중간에 끊겼습니다. 본문이 완전한지 확인해 주세요."},
		{"not json", `설명만 적었습니다`, "JSON을 해석하지 못했습니다(1번째 문자 부근)."},
	} {
		if got := decode(tc.body); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.name, got, tc.want)
		}
	}
	// The decoder prefixes the path with the Go type it was filling, and that name
	// appears nowhere in the API anybody is writing against.
	for field, want := range map[string]string{
		"AgentGoal.maxSteps":      "maxSteps",
		"Department.quota.total":  "quota.total",
		"maxSteps":                "maxSteps",
		"quota.total.maxRuntimes": "quota.total.maxRuntimes",
	} {
		if got := jsonFieldPath(field); got != want {
			t.Errorf("jsonFieldPath(%q) = %q, want %q", field, got, want)
		}
	}
}
