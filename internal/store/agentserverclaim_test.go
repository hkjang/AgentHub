package store

import (
	"os"
	"strings"
	"testing"
)

// Taking a place on a machine has to be one act.
//
// This is measured, not reasoned about: two tasks queued together were placed
// three milliseconds apart, both read a load of zero, and both went to the
// machine an operator had limited to one at a time. A unit test cannot make two
// transactions race, so this reads the claim and refuses the shape that lost.
func TestTakingAPlaceOnAServerIsNotARead(t *testing.T) {
	body, err := os.ReadFile("agentserver.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) ClaimAgentServer(")
	if at < 0 {
		t.Fatal("ClaimAgentServer is gone; this guard is reading nothing")
	}
	claim := source[at:]
	if end := strings.Index(claim, "\nfunc "); end >= 0 {
		claim = claim[:end]
	}
	// The server's row is locked before the load is counted. Without it two
	// placements onto the same machine both count a load that does not yet
	// include the other, and the limit an operator set is decoration.
	if !strings.Contains(claim, "FOR UPDATE") {
		t.Error("the claim counts a server's load without locking its row; two placements at once will both fit into the last place")
	}
	if !strings.Contains(claim, "s.pool.Begin(") {
		t.Error("the claim is not one transaction, so the count and the write can be separated by another placement")
	}
	// And the count must be of runs still in flight. Counting every run a server
	// ever held would make a machine permanently full.
	if !strings.Contains(claim, "finished_at IS NULL") {
		t.Error("the claim counts runs that have already finished")
	}
}
