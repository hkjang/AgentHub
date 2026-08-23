package execution

import (
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// checked stamps a health answer with a time, because an answer with no time is
// an answer nothing is maintaining — and placement treats that as unknown.
func checked(server store.AgentServer) store.AgentServer {
	when := time.Now().Add(-time.Minute)
	server.CheckedAt = &when
	return server
}

// Where work goes when a Goal did not name one machine.
//
// This is the decision that makes a pool of servers usable, and the one whose
// mistakes are quiet: work that runs in the wrong network, or on the machine
// that failed its last check while a working one sat idle, looks exactly like
// work that ran correctly.

func serverList() []store.AgentServer {
	return []store.AgentServer{
		checked(store.AgentServer{ID: "dev", Name: "개발", NetworkZone: "dev", Enabled: true, Health: "healthy"}),
		checked(store.AgentServer{ID: "secure-cold", Name: "보안망-예비", NetworkZone: "secure", Enabled: true, Health: "unknown"}),
		checked(store.AgentServer{ID: "secure-warm", Name: "보안망", NetworkZone: "secure", Enabled: true, Health: "healthy"}),
		checked(store.AgentServer{ID: "secure-down", Name: "보안망-고장", NetworkZone: "secure", Enabled: true, Health: "unreachable"}),
		checked(store.AgentServer{ID: "off", Name: "꺼둔 것", NetworkZone: "secure", Enabled: false, Health: "healthy"}),
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
	only := []store.AgentServer{checked(store.AgentServer{ID: "secure-down", Name: "보안망-고장", NetworkZone: "secure", Enabled: true, Health: "unreachable"})}
	chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"}, nil)
	if why == "" {
		t.Errorf("work was sent to %s, which did not answer its last check", chosen.Name)
	}
}

func TestADisabledServerIsNotChosen(t *testing.T) {
	only := []store.AgentServer{checked(store.AgentServer{ID: "off", Name: "꺼둔 것", NetworkZone: "secure", Enabled: false, Health: "healthy"})}
	if chosen, why := chooseAgentServer(only, store.AgentGoal{AgentServerZone: "secure"}, nil); why == "" {
		t.Errorf("work was sent to %s, which an operator had turned off", chosen.Name)
	}
}

func TestACheckedServerComesBeforeAnUncheckedOne(t *testing.T) {
	// The unchecked one first in the list, so a chooser that simply takes the head
	// picks it. An unchecked server may work — it is not excluded — but a site
	// that registered a spare should not have the first task land on it.
	servers := []store.AgentServer{
		checked(store.AgentServer{ID: "secure-cold", Name: "보안망-예비", NetworkZone: "secure", Enabled: true, Health: "unknown"}),
		checked(store.AgentServer{ID: "secure-warm", Name: "보안망", NetworkZone: "secure", Enabled: true, Health: "healthy"}),
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
		checked(store.AgentServer{ID: "busy", Name: "바쁜 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"}),
		checked(store.AgentServer{ID: "idle", Name: "한가한 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"}),
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
		checked(store.AgentServer{ID: "small", Name: "작은 서버", NetworkZone: "dev", Enabled: true, Health: "healthy", Capacity: 2}),
		checked(store.AgentServer{ID: "unlimited", Name: "한도 없는 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"}),
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
	servers := []store.AgentServer{checked(store.AgentServer{ID: "plain", Name: "일반 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"})}
	if _, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, map[string]int{"plain": 9}); why != "" {
		t.Errorf("a server with no stated limit was treated as full: %s", why)
	}
}

// TestBusyIsNotTheSameAsMissing is what an operator reads at the moment nothing
// runs. One says register a machine; the other says wait.
func TestBusyIsNotTheSameAsMissing(t *testing.T) {
	servers := []store.AgentServer{checked(store.AgentServer{ID: "small", Name: "작은 서버", NetworkZone: "dev", Enabled: true, Health: "healthy", Capacity: 1})}
	_, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, map[string]int{"small": 1})
	if why == "" {
		t.Fatal("a full server was chosen anyway")
	}
	if strings.Contains(why, "등록") {
		t.Errorf("a busy pool is reported as an empty one, which tells an operator to register a machine they already have: %s", why)
	}
}

// An answer that has gone stale is not an answer.
//
// Health used to be recorded once, when somebody pressed a button, and kept
// forever — so a machine verified in March and one verified an hour ago looked
// identical to the code that chooses between them. A sweep now refreshes it,
// which only helps if the choosing stops treating an old answer as current.

func aged(server store.AgentServer, age time.Duration) store.AgentServer {
	when := time.Now().Add(-age)
	server.CheckedAt = &when
	return server
}

func TestAServerThatFailedMonthsAgoIsNotShutOutForever(t *testing.T) {
	// The only machine in the zone, last seen failing a long time ago. Excluding
	// it on that answer means a pool that recovered stays empty until somebody
	// notices and presses a button.
	servers := []store.AgentServer{
		aged(store.AgentServer{ID: "old", Name: "옛 서버", NetworkZone: "dev", Enabled: true, Health: "unreachable"}, 30*24*time.Hour),
	}
	if _, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, nil); why != "" {
		t.Errorf("a machine whose only bad answer is a month old was refused work: %s", why)
	}
}

func TestAServerThatFailedJustNowIsShutOut(t *testing.T) {
	servers := []store.AgentServer{
		aged(store.AgentServer{ID: "down", Name: "죽은 서버", NetworkZone: "dev", Enabled: true, Health: "unreachable"}, time.Minute),
	}
	if chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, nil); why == "" {
		t.Errorf("work was sent to %s, which failed its check a minute ago", chosen.Name)
	}
}

// TestAnOldHealthyAnswerDoesNotOutrankAnEmptyMachine is the other direction, and
// the one that actually decides where work goes: preferring a machine because of
// an answer from months ago is preferring a memory.
func TestAnOldHealthyAnswerDoesNotOutrankAnEmptyMachine(t *testing.T) {
	servers := []store.AgentServer{
		aged(store.AgentServer{ID: "stale", Name: "오래된 확인", NetworkZone: "dev", Enabled: true, Health: "healthy"}, 90*24*time.Hour),
		aged(store.AgentServer{ID: "fresh", Name: "최근 확인", NetworkZone: "dev", Enabled: true, Health: "healthy"}, time.Minute),
	}
	load := map[string]int{"stale": 0, "fresh": 0}
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, load)
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "fresh" {
		t.Errorf("work went to %s, whose last check is three months old, over a machine checked a minute ago", chosen.Name)
	}
}

// TestAStaleServerIsStillSubjectToItsLimit — the freshness rule is about health,
// and it must not become a way around capacity.
func TestAStaleServerIsStillSubjectToItsLimit(t *testing.T) {
	servers := []store.AgentServer{
		aged(store.AgentServer{ID: "old", Name: "옛 서버", NetworkZone: "dev", Enabled: true, Health: "healthy", Capacity: 1}, 90*24*time.Hour),
	}
	if chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, map[string]int{"old": 1}); why == "" {
		t.Errorf("a full machine was chosen because its health was stale: %s", chosen.Name)
	}
}

// TestAServerNobodyHasCheckedIsUsableButNotPreferred keeps the two ends of the
// same rule together. A machine just registered has no answer at all: it may
// work, so it is not excluded, and nothing is known about it, so it does not
// outrank one that answered a minute ago. It also must not crash the choosing —
// a server row with no check time is the ordinary state right after somebody
// registers one.
func TestAServerNobodyHasCheckedIsUsableButNotPreferred(t *testing.T) {
	unchecked := store.AgentServer{ID: "new", Name: "새 서버", NetworkZone: "dev", Enabled: true, Health: "unknown"}
	if _, why := chooseAgentServer([]store.AgentServer{unchecked}, store.AgentGoal{AgentServerZone: "dev"}, nil); why != "" {
		t.Errorf("a newly registered machine was refused work: %s", why)
	}
	servers := []store.AgentServer{
		unchecked,
		aged(store.AgentServer{ID: "known", Name: "확인된 서버", NetworkZone: "dev", Enabled: true, Health: "healthy"}, time.Minute),
	}
	chosen, why := chooseAgentServer(servers, store.AgentGoal{AgentServerZone: "dev"}, nil)
	if why != "" {
		t.Fatalf("nothing was chosen: %s", why)
	}
	if chosen.ID != "known" {
		t.Errorf("the first task went to %s, which nobody has checked, over one seen working a minute ago", chosen.Name)
	}
}
