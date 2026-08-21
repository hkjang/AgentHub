package api

import (
	"testing"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// A runtime the namespace listing did not return is a runtime whose object is
// gone, which is exactly what the single-object read reports as Stopped. Sending
// it back to the API server to be told the same thing would put the per-agent
// round trip back for precisely the agents that need it least.
func TestAMissingRuntimeIsAnsweredFromTheListing(t *testing.T) {
	batch := map[string]appRuntime.Status{
		"agent-alice": {Phase: "Running", PodName: "agent-alice-0"},
	}
	running := store.Agent{Runtime: &store.Runtime{ID: "r1", CRDName: "agent-alice"}}
	if status, ok := statusFor(batch, running); !ok || status.Phase != "Running" || status.PodName != "agent-alice-0" {
		t.Errorf("a listed runtime = %#v (ok=%v)", status, ok)
	}
	gone := store.Agent{Runtime: &store.Runtime{ID: "r2", CRDName: "agent-bob"}}
	if status, ok := statusFor(batch, gone); !ok || status.Phase != "Stopped" {
		t.Errorf("a runtime absent from the listing = %#v (ok=%v)", status, ok)
	}

	// Without a batch — an older spawner, or a listing that failed — every agent
	// falls back to being asked individually rather than being reported Stopped.
	if _, ok := statusFor(nil, running); ok {
		t.Error("a missing batch was treated as an answer")
	}
	// An agent with no runtime object yet has no name to look up.
	if _, ok := statusFor(batch, store.Agent{Runtime: &store.Runtime{ID: "r3"}}); ok {
		t.Error("a runtime with no CRD name was answered from the listing")
	}
	if _, ok := statusFor(batch, store.Agent{}); ok {
		t.Error("an agent with no runtime was answered from the listing")
	}
}
