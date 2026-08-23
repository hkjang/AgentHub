package execution

import (
	"strings"
	"testing"
)

// What is worth telling an administrator about.
//
// The sweep now finds things out on its own, which is only useful if what it
// finds reaches somebody. It is equally easy to make that useless: a deployment
// that has just registered its servers learns about all of them at once, and a
// notice for each would teach people to ignore the next one.

func TestLearningSomethingWorksIsNotARecovery(t *testing.T) {
	first := dependencyChange{Kind: "에이전트 서버", Name: "새 서버", Was: "unknown", Now: "healthy", Good: true}
	if worthAnnouncing(first) {
		t.Error("the first sweep of a newly registered server was announced as a recovery; there was nothing to recover from")
	}
	real := dependencyChange{Kind: "에이전트 서버", Name: "돌아온 서버", Was: "unreachable", Now: "healthy", Good: true}
	if !worthAnnouncing(real) {
		t.Error("a machine that came back was not announced, so the notice about it going down is never closed")
	}
}

func TestTroubleIsAlwaysWorthSaying(t *testing.T) {
	// Including the first time it is found. A server registered this morning that
	// has never worked is exactly what an operator wants to hear about.
	first := dependencyChange{Kind: "모델 엔드포인트", Name: "새 엔드포인트", Was: "unknown", Now: "unreachable"}
	if !worthAnnouncing(first) {
		t.Error("a dependency that has never worked was not announced")
	}
}

// TestTheNoticeIsWrittenForAPerson — the stored verdicts are the checks' own
// vocabulary, and a notification that says "model_missing" is written for the
// platform rather than for whoever has to act on it.
func TestTheNoticeIsWrittenForAPerson(t *testing.T) {
	for _, verdict := range []string{"ok", "healthy", "unreachable", "model_missing", "unauthorised", "wrong_path", "unknown"} {
		word := dependencyWord(verdict)
		if word == verdict {
			t.Errorf("%q is shown to an administrator as-is", verdict)
		}
		if strings.ContainsAny(word, "_") {
			t.Errorf("%q reads as an identifier: %q", verdict, word)
		}
	}
	// An unknown verdict is passed through rather than dropped: saying a word
	// nobody translated is better than saying nothing about a state that exists.
	if dependencyWord("something_new") != "something_new" {
		t.Error("a verdict nobody has translated is swallowed")
	}
}
