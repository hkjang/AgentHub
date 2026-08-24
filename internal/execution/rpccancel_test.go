package execution

import (
	"os"
	"strings"
	"testing"
)

// An agent waiting on a model that never answers writes nothing, and the loop
// that reads it blocked on that silence: a cancelled task went on running, its
// stop button doing nothing, and its own time limit was never reached either —
// because the limit was only checked when a line came in.
//
// Measured on a real cluster: a Pi task cancelled while its gateway hung left
// the process running in the Pod and the run saying running, minutes later.
func TestTheProtocolLoopHearsMoreThanTheAgent(t *testing.T) {
	body, err := os.ReadFile("rpc.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "go o.deliverDirectives(")
	if at < 0 {
		t.Fatal("the protocol loop is gone; this guard is reading nothing")
	}
	loop := source[at:]
	if end := strings.Index(loop, "\n// deliverDirectives"); end >= 0 {
		loop = loop[:end]
	}

	// Scanning still happens — it just cannot be what the loop waits on. It
	// belongs on a goroutine that feeds a channel, so the loop can wait on the
	// agent, the clock and the cancellation at once.
	scanAt := strings.Index(loop, "for lines.Scan()")
	if scanAt < 0 {
		t.Fatal("nothing reads the agent any more")
	}
	before := loop[:scanAt]
	if goAt := strings.LastIndex(before, "go func()"); goAt < 0 || scanAt-goAt > 200 {
		t.Error("the loop waits on the agent's output directly, so silence stops it hearing anything else")
	}
	if !strings.Contains(loop, "case next, open := <-spoken:") {
		t.Error("the loop does not take the agent's lines from a channel")
	}
	for _, want := range []string{"case <-ctx.Done():", "case <-limit.C:"} {
		if !strings.Contains(loop, want) {
			t.Errorf("the loop cannot hear %s", want)
		}
	}
	// Leaving the agent talking to nobody is what left a process running in a
	// Pod after somebody pressed stop. Anchored on the return the cancelled path
	// makes, because the reader goroutine has a ctx.Done() of its own.
	returnAt := strings.Index(loop, "return result, ctx.Err()")
	if returnAt < 0 {
		t.Fatal("the loop never returns on cancellation")
	}
	from := returnAt - 300
	if from < 0 {
		from = 0
	}
	if !strings.Contains(loop[from:returnAt], "session.Stdin.Close()") {
		t.Error("a cancelled run does not tell the agent it is over")
	}
	// The time limit is a timer rather than a comparison made on arrival.
	if strings.Contains(loop, "time.Now().After(deadline)") {
		t.Error("the time limit is still only checked when a line arrives")
	}
}
