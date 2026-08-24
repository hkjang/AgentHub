package execution

import (
	"os"
	"strings"
	"testing"
)

// OpenHands was an execution backend before it was a runtime type: it could only
// be pointed at a server somebody else installed and registered by URL. Now that
// the platform can start one, an agent whose runtime is an agent server has to
// work on its own runtime — sending that work to a stranger's machine would be a
// runtime a person chose and the platform ignored.
func TestAnAgentServerRuntimeIsWhereItsOwnWorkGoes(t *testing.T) {
	body, err := os.ReadFile("agentserver.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (o *Orchestrator) runAgentServer(")
	if at < 0 {
		t.Fatal("the backend is gone; this guard is reading nothing")
	}
	entry := source[at:]
	if end := strings.Index(entry, "\n\t\tPromptTokens:"); end >= 0 {
		entry = entry[:end]
	}
	own := strings.Index(entry, "ownRuntimeAsAgentServer(")
	registry := strings.Index(entry, "placeOnAgentServer(")
	if own < 0 {
		t.Fatal("an agent whose runtime is an agent server is still sent to the registry")
	}
	if registry >= 0 && own > registry {
		t.Error("the registry is consulted before the agent's own runtime")
	}
	// And the runtime has to be released, however the run ends. A Pod held open
	// by a finished task is a quota nobody gets back.
	if !strings.Contains(entry, "defer o.releaseRuntime(") {
		t.Error("the runtime this backend started is never released")
	}

	fn := source[strings.Index(source, "func (o *Orchestrator) ownRuntimeAsAgentServer("):]
	if end := strings.Index(fn, "\nfunc (o *Orchestrator) placeOnAgentServer("); end >= 0 {
		fn = fn[:end]
	}
	// Every other runtime type keeps going to the registry.
	if !strings.Contains(fn, "agent.RuntimeType != runtimetype.OpenHands") {
		t.Error("this path is taken for runtimes that are not agent servers")
	}
	// A Pod that is ready and has no address is not a server: a request to the
	// empty string fails as something else entirely.
	if !strings.Contains(fn, `strings.TrimSpace(instance.Endpoint) == ""`) {
		t.Error("a runtime with no address is used as though it had one")
	}
	// The registry's limits are about machines several agents share. A runtime's
	// capacity is its own Pod, and claiming a registry slot for it would refuse
	// work against a limit that is not about it.
	if strings.Contains(fn, "ClaimAgentServer(") {
		t.Error("a runtime of one's own is claimed against the shared registry's limit")
	}
}
