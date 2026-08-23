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
	chosen, why := chooseAgentServer(serverList(), store.AgentGoal{AgentServerZone: "secure"}, nil)
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
	chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"}, nil)
	if why == "" {
		t.Errorf("work was sent to %s, which did not answer its last check", chosen.Name)
	}
}

func TestADisabledServerIsNotChosen(t *testing.T) {
	only := []store.AgentServer{{ID: "off", Name: "꺼둔 것", NetworkZone: "secure", Enabled: false, Health: "healthy"}}
	if chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"}, nil); why == "" {
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
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "secure"}, nil)
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "secure-warm" {
		t.Errorf("the first task went to %s, which nobody has checked", chosen.Name)
	}
}

func TestAZoneWithNoServerIsRefusedRatherThanRedirected(t *testing.T) {
	chosen, why := chooseAgentServer(serverList(), store.AgentGoal{AgentServerZone: "gpu"}, nil)
	if why == "" {
		t.Fatalf("work meant for the gpu network was sent to %s in %q", chosen.Name, chosen.NetworkZone)
	}
	// And the refusal says which network it could not satisfy, because an operator
	// reading it has to know what to register.
	if !strings.Contains(why, "gpu") {
		t.Errorf("the refusal does not name the network that was asked for: %s", why)
	}
}

// TestWorkSpreadsAcrossTheMachinesThatCanTakeIt is what a pool is for. Without
// it a site with four servers runs everything on whichever one was registered
// first, and the other three are decoration.
func TestWorkSpreadsAcrossTheMachinesThatCanTakeIt(t *testing.T) {
	servers := []store.AgentServer{
		{ID: "busy", Name: "바쁜 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"},
		{ID: "idle", Name: "한가한 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"},
	}
	load := map[string]int{"busy": 3}
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, load)
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "idle" {
		t.Errorf("work went to %s, which is already holding %d runs while another machine sat idle", chosen.Name, load["busy"])
	}
}

// TestAServerAtItsLimitIsNotGivenMore is the other half: a number an operator
// sets and the platform ignores is worse than no field at all.
func TestAServerAtItsLimitIsNotGivenMore(t *testing.T) {
	servers := []store.AgentServer{
		{ID: "small", Name: "작은 서버", NetworkZone: "dev", Enabled: true, Health: "healthy", Capacity: 2},
		{ID: "unlimited", Name: "한도 없는 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"},
	}
	// The capped machine is emptier, so a chooser that only looks at load picks it.
	load := map[string]int{"small": 2, "unlimited": 5}
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, load)
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "unlimited" {
		t.Errorf("a server allowed %d at once was given a third run", servers[0].Capacity)
	}
}

// TestCapacityNobodySetIsNotReadAsNoRoom keeps the default honest. Zero means
// the operator did not say, and refusing work over it would be the platform
// inventing a limit nobody asked for.
func TestCapacityNobodySetIsNotReadAsNoRoom(t *testing.T) {
	servers := []store.AgentServer{{ID: "plain", Name: "일반 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"}}
	if _, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, map[string]int{"plain": 9}); why != "" {
		t.Errorf("a server with no stated limit was treated as full: %s", why)
	}
}

// TestBusyIsNotTheSameAsMissing is what an operator reads at the moment nothing
// runs. One says register a machine; the other says wait.
func TestBusyIsNotTheSameAsMissing(t *testing.T) {
	servers := []store.AgentServer{{ID: "small", Name: "작은 서버", NetworkZone: "dev", Enabled: true, Health: "healthy", Capacity: 1}}
	_, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, map[string]int{"small": 1})
	if why == "" {
		t.Fatal("a full server was chosen anyway")
	}
	if strings.Contains(why, "등록") {
		t.Errorf("a busy pool is reported as an empty one, which tells an operator to register a machine they already have: %s", why)
	}
}
