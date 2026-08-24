package store

import (
	"os"
	"strings"
	"testing"
)

// The limit is only as good as the count it is compared against, and the count
// has to be taken inside the transaction that writes the row — the same place
// the other three dimensions are counted. A GPU limit checked against a number
// read outside the lock is two people each being told there is one card left.
func TestGPUsAreCountedWhereTheOtherDimensionsAre(t *testing.T) {
	for _, file := range []string{"runtimeclaim.go", "quotascope.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		// Every place that sums a profile's CPU must sum its GPUs too, or the
		// screen and the limit disagree about what is held.
		cpu := strings.Count(source, "sum(p.cpu_millis)")
		gpu := strings.Count(source, "sum(p.gpu_count)")
		if cpu != gpu {
			t.Errorf("%s counts CPU %d times and GPUs %d times", file, cpu, gpu)
		}
	}
	body, err := os.ReadFile("runtimeclaim.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	// And the profile being started has to contribute its own GPUs, or the
	// first request over the line is the one that is never refused.
	if !strings.Contains(source, "SELECT cpu_millis,memory_mb,gpu_count FROM runtime_profiles") {
		t.Error("the claim does not read the GPU count of the profile it is starting")
	}
	at := strings.Index(source, "func (s *Store) ClaimRuntimeCapacity(")
	if at < 0 {
		t.Fatal("the claim is gone; this guard is reading nothing")
	}
	claim := source[at:]
	if end := strings.Index(claim, "\n// heldRuntimes"); end >= 0 {
		claim = claim[:end]
	}
	if strings.Count(claim, "addGPUs") < 3 {
		t.Error("the GPUs being asked for do not reach both scopes' checks")
	}
}
