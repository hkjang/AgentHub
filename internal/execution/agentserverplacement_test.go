package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// Where work goes when a Goal did not name one machine.
//
// This is the decision that makes a pool of servers usable, and the one whose
// mistakes are quiet: work that runs in the wrong network, or on the machine
// that failed its last check while a working one sat idle, looks exactly like
// work that ran correctly.

func serverList() []store.AgentServer {
	return []store.AgentServer{
		{ID: "dev", Name: "개발", NetworkZone: "dev", Enabled: true, Health: "healthy"},
		{ID: "secure-cold", Name: "보안망-예비", NetworkZone: "secure", Enabled: true, Health: "unknown"},
		{ID: "secure-warm", Name: "보안망", NetworkZone: "secure", Enabled: true, Health: "healthy"},
		{ID: "secure-down", Name: "보안망-고장", NetworkZone: "secure", Enabled: true, Health: "unreachable"},
		{ID: "off", Name: "꺼둔 것", NetworkZone: "secure", Enabled: false, Health: "healthy"},
	}
}

func TestWorkStaysInTheZoneItWasAskedFor(t *testing.T) {
	chosen, why := chooseAgentServer(serverList(), store.AgentGoal{AgentServerZone: "secure"})
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.NetworkZone != "secure" {
		t.Errorf("work asked for the secure network and was sent to %q (%s)", chosen.NetworkZone, chosen.Name)
	}
}

func TestAServerThatFailedItsLastCheckIsNotChosen(t *testing.T) {
	// Every other candidate removed, so a chooser that ignores health has only the
	// broken machine to pick and must refuse instead.
	only := []store.AgentServer{{ID: "secure-down", Name: "보안망-고장", NetworkZone: "secure", Enabled: true, Health: "unreachable"}}
	chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"})
	if why == "" {
		t.Errorf("work was sent to %s, which did not answer its last check", chosen.Name)
	}
}

func TestADisabledServerIsNotChosen(t *testing.T) {
	only := []store.AgentServer{{ID: "off", Name: "꺼둔 것", NetworkZone: "secure", Enabled: false, Health: "healthy"}}
	if chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"}); why == "" {
		t.Errorf("work was sent to %s, which an operator had turned off", chosen.Name)
	}
}

func TestACheckedServerComesBeforeAnUncheckedOne(t *testing.T) {
	// The unchecked one first in the list, so a chooser that simply takes the head
	// picks it. An unchecked server may work — it is not excluded — but a site
	// that registered a spare should not have the first task land on it.
	servers := []store.AgentServer{
		{ID: "secure-cold", Name: "보안망-예비", NetworkZone: "secure", Enabled: true, Health: "unknown"},
		{ID: "secure-warm", Name: "보안망", NetworkZone: "secure", Enabled: true, Health: "healthy"},
	}
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "secure"})
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "secure-warm" {
		t.Errorf("the first task went to %s, which nobody has checked", chosen.Name)
	}
}

func TestAZoneWithNoServerIsRefusedRatherThanRedirected(t *testing.T) {
	chosen, why := chooseAgentServer(serverList(), store.AgentGoal{AgentServerZone: "gpu"})
	if why == "" {
		t.Fatalf("work meant for the gpu network was sent to %s in %q", chosen.Name, chosen.NetworkZone)
	}
	// And the refusal says which network it could not satisfy, because an operator
	// reading it has to know what to register.
	if !strings.Contains(why, "gpu") {
		t.Errorf("the refusal does not name the network that was asked for: %s", why)
	}
}
