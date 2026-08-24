package store

import (
	"os"
	"strings"
	"testing"
)

// A secret left behind by an unfinished key rotation is a state somebody can
// fix, not a fault in this platform.
//
// Measured: attaching such a secret to a workspace answered
//
//	500 요청을 처리하지 못했습니다: secret "probe-secret" was encrypted with key
//	version 14 but the active version is 15
//
// — this platform's own key bookkeeping, in English, delivered as though the
// request had broken something. The reveal is what the console calls to check
// the caller owns the secret, so it is the first thing anybody meets after a
// rotation that did not finish.
func TestAStaleKeyIsAnAnswerNotAFault(t *testing.T) {
	body, err := os.ReadFile("secrets.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) RevealPersonalSecret(")
	if at < 0 {
		t.Fatal("the reveal is gone; this guard is reading nothing")
	}
	reveal := source[at:]
	if end := strings.Index(reveal, "\nfunc "); end >= 0 {
		reveal = reveal[:end]
	}
	if strings.Contains(reveal, "was encrypted with key version") {
		t.Error("a person is still shown this platform's key arithmetic, in English")
	}
	if !strings.Contains(reveal, "Conflict{Message:") {
		t.Error("a recoverable state is still reported as a server fault")
	}
	// And it has to say what to do about it.
	if !strings.Contains(reveal, "다시 저장하면") {
		t.Error("the message names the problem without naming the remedy")
	}
}
