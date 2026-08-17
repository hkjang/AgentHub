package api

import "testing"

// The exported name becomes a download filename, and an agent may be named in
// any language with any punctuation.
func TestSafeFileNameStaysUsable(t *testing.T) {
	cases := map[string]string{
		"payment-agent": "payment-agent",
		"결제 점검 에이전트":    "결제-점검-에이전트",
		"a/b\\c:d*e?":   "a-b-c-d-e",
		"  ":            "agent",
		"":              "agent",
		"...":           "agent",
		"Ops_Agent v2":  "Ops_Agent-v2",
	}
	for name, want := range cases {
		if got := safeFileName(name); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

// The plain filename parameter of Content-Disposition cannot carry non-ASCII,
// so a Korean name still needs a usable fallback.
func TestAsciiFileNameFallsBackWithoutLosingUsability(t *testing.T) {
	if got := asciiFileName("결제 점검"); got != "agent" {
		t.Errorf("asciiFileName(korean) = %q, want the fallback", got)
	}
	if got := asciiFileName("payment 점검 agent"); got != "payment----agent" {
		t.Errorf("asciiFileName(mixed) = %q", got)
	}
	for _, r := range asciiFileName("결제-agent") {
		if r > 127 {
			t.Fatalf("asciiFileName kept a non-ASCII rune: %q", r)
		}
	}
}

func TestSafeFileNameNeverEmptyOrPathLike(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "/", "..", "결제"} {
		got := safeFileName(name)
		if got == "" {
			t.Errorf("safeFileName(%q) is empty", name)
		}
		for _, r := range got {
			if r == '/' || r == '\\' || r == '.' {
				t.Errorf("safeFileName(%q) = %q, which is path-like", name, got)
			}
		}
	}
}
