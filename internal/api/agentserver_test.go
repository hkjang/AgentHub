package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// What may be registered, and what a Goal may ask of it.
//
// Both refusals exist for the same reason: the work leaves this machine. A URL
// that is not a server, or a directory that climbs out of the workspace, is not
// a mistake this deployment finds out about — it is one the other machine acts
// on.

func TestAServerAddressMustBeOneThisPlatformCanSpeakTo(t *testing.T) {
	for _, address := range []string{"", "not a url", "file:///etc/passwd", "ftp://box/agent", "/api"} {
		if complaint := agentServerComplaint(store.AgentServer{Name: "서버", BaseURL: address}); complaint == "" {
			t.Errorf("%q was accepted as an agent server address", address)
		}
	}
	if complaint := agentServerComplaint(store.AgentServer{Name: "서버", BaseURL: "https://box.internal:8000"}); complaint != "" {
		t.Errorf("a real address was refused: %s", complaint)
	}
}

func TestAGoalCannotSendTheAgentOutOfItsWorkspace(t *testing.T) {
	for _, directory := range []string{"/etc", "../../root", "workspace/../../etc"} {
		goal := store.AgentGoal{Runner: store.RunnerAgentServer, AgentServerID: "dev", AgentServerDir: directory}
		if err := validateRunner(&goal, "claude"); err == nil {
			t.Errorf("a goal was allowed to work in %q on another machine", directory)
		}
	}
	fine := store.AgentGoal{Runner: store.RunnerAgentServer, AgentServerID: "dev", AgentServerDir: "workspace/project"}
	if err := validateRunner(&fine, "claude"); err != nil {
		t.Errorf("an ordinary working directory was refused: %v", err)
	}
}

func TestAGoalMustSayWhereItsWorkGoes(t *testing.T) {
	empty := store.AgentGoal{Runner: store.RunnerAgentServer}
	if err := validateRunner(&empty, "claude"); err == nil {
		t.Error("a goal with no server and no network was accepted; its work would go wherever placement happened to look first")
	}
	both := store.AgentGoal{Runner: store.RunnerAgentServer, AgentServerID: "dev", AgentServerZone: "secure"}
	if err := validateRunner(&both, "claude"); err == nil {
		t.Error("a goal both pinned to a server and asking for a network was accepted; one of the two would be ignored")
	}
}

// TestAGoalOnAnAgentServerNeedsNoRuntime is the property that separates this
// backend from the ones that run in a Pod. Requiring a runtime would make the
// platform start a container to hold a conversation happening somewhere else.
func TestAGoalOnAnAgentServerNeedsNoRuntime(t *testing.T) {
	goal := store.AgentGoal{Runner: store.RunnerAgentServer, AgentServerZone: "secure", StartOnDemand: false}
	if err := validateRunner(&goal, "claude"); err != nil {
		t.Errorf("a goal whose work runs elsewhere was told to start a runtime here: %v", err)
	}
}

// TestSomethingThatAnswersIsNotYetAnAgentServer is what registration is for. A
// proxy, a parked domain or the wrong service all answer a bare request; only
// the right thing offers the endpoint this platform is going to call.
func TestSomethingThatAnswersIsNotYetAnAgentServer(t *testing.T) {
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"paths": map[string]any{"/api/health": map[string]any{}}})
	}))
	defer wrong.Close()
	health, detail := probeAgentServer(context.Background(), store.AgentServer{BaseURL: wrong.URL})
	if health == "healthy" {
		t.Errorf("a server with no way to start a conversation was registered as working: %s", detail)
	}

	right := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"paths": map[string]any{"/api/conversations": map[string]any{}}})
	}))
	defer right.Close()
	if health, detail := probeAgentServer(context.Background(), store.AgentServer{BaseURL: right.URL}); health != "healthy" {
		t.Errorf("a real agent server was reported as %s: %s", health, detail)
	}
}

func TestAServerThatIsNotThereIsSaidToBeUnreachable(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	address := gone.URL
	gone.Close()
	health, detail := probeAgentServer(context.Background(), store.AgentServer{BaseURL: address})
	if health != "unreachable" {
		t.Errorf("a server that is not running was reported as %q", health)
	}
	// And in words that say what to do about it, because this is what an operator
	// reads at the moment registration fails.
	if !strings.Contains(detail, "확인") && !strings.Contains(detail, "찾지") {
		t.Errorf("the reason does not tell an operator anything: %s", detail)
	}
}

// TestAGoalThatWantsAPersonIsAccepted is the other half of the same rule. The
// backend can now hold each action while somebody answers, so refusing the
// policy would be the platform declining work it can do.
func TestAGoalThatWantsAPersonIsAccepted(t *testing.T) {
	goal := store.AgentGoal{Runner: store.RunnerAgentServer, AgentServerZone: "secure", ApprovalRequired: true}
	if err := validateRunner(&goal, "claude"); err != nil {
		t.Errorf("a goal asking a person to approve each action was refused: %v", err)
	}
}
