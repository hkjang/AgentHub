package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A trigger filters events by payload containment, and the console offers
// `{"agentId":"…"}` as the way to watch one agent. An event published without an
// agent id can therefore never match such a filter — the subscription is accepted,
// saved, shown as enabled, and silent.
//
// approval.decided was published with the decision, the action and the reason:
// enough to read, and nothing to filter on. Every other publisher already carried
// the agent, which is why nobody noticed this one did not.
func TestEveryPublishedEventCanBeFilteredByAgent(t *testing.T) {
	publisher := regexp.MustCompile(`(?:PublishEvent|publishEvent|\.publish)\(`)
	// A forwarder takes an event somebody else built; a builder makes one. Only
	// the builders can be asked what is in the payload.
	forwards := regexp.MustCompile(`(?m)^func .*(?:PlatformEvent|payload map\[string\]any)`)
	signature := regexp.MustCompile(`(?m)^func .*$`)
	root := filepath.Join("..", "..", "internal")
	sites := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		source := string(body)
		for _, at := range publisher.FindAllStringIndex(source, -1) {
			// The declaration of a publisher is not a use of one.
			line := source[strings.LastIndex(source[:at[0]], "\n")+1 : at[0]]
			if strings.HasPrefix(line, "func ") {
				continue
			}
			enclosing := signature.FindAllString(source[:at[0]], -1)
			if len(enclosing) > 0 && forwards.MatchString(enclosing[len(enclosing)-1]) {
				continue
			}
			sites++
			// Both sides of the call: a payload is as often built just above it as
			// written inside it.
			//
			// This is a heuristic and worth knowing as one: a neighbour's agent id
			// inside the window would satisfy a publish that has none of its own.
			// Reading the payload literal instead would mean parsing Go, which is
			// more machinery than the question is worth — but a guard nobody knows
			// is approximate is a guard somebody will trust too far.
			window := source[max(0, at[0]-700):min(len(source), at[1]+700)]
			if !strings.Contains(window, "agentId") && !strings.Contains(window, "AgentID") &&
				!strings.Contains(window, "approvalEventPayload") {
				t.Errorf("%s publishes an event whose payload has no agent id; a trigger filtered by agent can never match it", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sites < 4 {
		t.Fatalf("only %d publishing site(s) found; this guard is not reading the tree", sites)
	}
}

// And the approval event in particular, because it is built from a record rather
// than written out at the call site: the fields it borrows have to survive.
func TestTheApprovalEventNamesWhatWasDecided(t *testing.T) {
	for _, item := range []struct {
		name     string
		approval store.Approval
		wants    map[string]string
	}{
		{
			name: "an agent asking to change something",
			approval: store.Approval{
				ID: "ap-1", ResourceType: "task", ResourceID: "task-1", Action: "agent.action", Reason: "재시작",
				Payload: json.RawMessage(`{"taskId":"task-1","runId":"run-1","agentId":"agent-1","detail":"…"}`),
			},
			wants: map[string]string{"agentId": "agent-1", "taskId": "task-1", "runId": "run-1", "approvalId": "ap-1"},
		},
		{
			// The runtime spawn approval keeps the agent in its resource id rather
			// than its payload, and a filter cannot be expected to know that.
			name: "a person asking to start a runtime",
			approval: store.Approval{
				ID: "ap-2", ResourceType: "agent", ResourceID: "agent-2", Action: "spawn",
				Payload: json.RawMessage(`{"agentName":"수집기"}`),
			},
			wants: map[string]string{"agentId": "agent-2", "resourceType": "agent"},
		},
		{
			// A record with no payload at all must still publish something readable.
			name:     "an approval that carries nothing",
			approval: store.Approval{ID: "ap-3", ResourceType: "task", ResourceID: "task-3"},
			wants:    map[string]string{"approvalId": "ap-3", "resourceId": "task-3"},
		},
	} {
		var fields map[string]any
		if err := json.Unmarshal(approvalEventPayload(item.approval, "approved"), &fields); err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if fields["decision"] != "approved" {
			t.Errorf("%s: the decision itself is missing: %v", item.name, fields)
		}
		for key, want := range item.wants {
			if fields[key] != want {
				t.Errorf("%s: %s is %v, want %q — a trigger filtering on it will never match", item.name, key, fields[key], want)
			}
		}
	}
}
