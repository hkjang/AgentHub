package api

import (
	"context"
	"os"
	"testing"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// The flow picker reads the runtime's own list, so this checks the request the
// console depends on against a real runtime. Same variables as
// TestLiveFlowRun in internal/execution, and skipped the same way.
func TestLiveFlowList(t *testing.T) {
	endpoint, token := os.Getenv("AGENTHUB_LIVE_LANGFLOW"), os.Getenv("AGENTHUB_LIVE_LANGFLOW_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("set AGENTHUB_LIVE_LANGFLOW and AGENTHUB_LIVE_LANGFLOW_TOKEN to run this against a real runtime")
	}
	items, truncated, err := fetchFlows(context.Background(), appRuntime.Connection{Endpoint: endpoint, Token: token, RuntimeType: runtimetype.Langflow})
	if err != nil {
		t.Fatalf("the flow list could not be read: %v", err)
	}
	t.Logf("%d flows (truncated=%v)", len(items), truncated)
	for _, item := range items {
		if item.ID == "" || item.Name == "" {
			t.Errorf("a flow arrived without an id or a name: %#v", item)
		}
	}
	if len(items) == 0 {
		t.Skip("the runtime holds no flows, so there is nothing to check the shape against")
	}
	// A wrong token must not return a list.
	if _, _, err := fetchFlows(context.Background(), appRuntime.Connection{Endpoint: endpoint, Token: token + "-wrong"}); err == nil {
		t.Error("a wrong runtime token was accepted")
	}
}
