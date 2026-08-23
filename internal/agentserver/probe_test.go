package agentserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSomethingThatAnswersIsNotYetAnAgentServer is what registration is for. A
// proxy, a parked domain or the wrong service all answer a bare request; only
// the right thing offers the endpoint this platform is going to call.
func TestSomethingThatAnswersIsNotYetAnAgentServer(t *testing.T) {
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"paths": map[string]any{"/api/health": map[string]any{}}})
	}))
	defer wrong.Close()
	health, detail := Probe(context.Background(), wrong.URL)
	if health == Healthy {
		t.Errorf("a server with no way to start a conversation was registered as working: %s", detail)
	}

	right := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"paths": map[string]any{"/api/conversations": map[string]any{}}})
	}))
	defer right.Close()
	if health, detail := Probe(context.Background(), right.URL); health != Healthy {
		t.Errorf("a real agent server was reported as %s: %s", health, detail)
	}
}

func TestAServerThatIsNotThereIsSaidToBeUnreachable(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	address := gone.URL
	gone.Close()
	health, detail := Probe(context.Background(), address)
	if health != Unreachable {
		t.Errorf("a server that is not running was reported as %q", health)
	}
	// And in words that say what to do about it, because this is what an operator
	// reads at the moment registration fails.
	if !strings.Contains(detail, "확인") && !strings.Contains(detail, "찾지") {
		t.Errorf("the reason does not tell an operator anything: %s", detail)
	}
}
