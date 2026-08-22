package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Handing a finding to something that can fix it.
//
// Finding and fixing are two runtimes' work, and what was missing between them
// was the handover: a person read a finding on one screen and retyped it into a
// task on another, losing the file, the line and the suggested code on the way.
//
// This needs a finding to hand over and a coding agent to hand it to, so it says
// so rather than skipping quietly when either is absent.
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run FindingHandover -v
func TestAFindingHandedOverCarriesWhatTheReviewFound(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check the handover against a running deployment")
	}
	client := login(t, base)

	body, status := client.get("/api/v1/review-findings?status=all&limit=200")
	if status != http.StatusOK {
		t.Fatalf("the findings could not be read (%d)", status)
	}
	var page struct {
		Items []struct {
			ID         string `json:"id"`
			FilePath   string `json:"filePath"`
			StartLine  int    `json:"startLine"`
			Message    string `json:"message"`
			Suggestion string `json:"suggestion"`
			FixTaskID  string `json:"fixTaskId"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	var finding *struct {
		ID         string `json:"id"`
		FilePath   string `json:"filePath"`
		StartLine  int    `json:"startLine"`
		Message    string `json:"message"`
		Suggestion string `json:"suggestion"`
		FixTaskID  string `json:"fixTaskId"`
	}
	for index := range page.Items {
		if page.Items[index].FixTaskID == "" {
			finding = &page.Items[index]
			break
		}
	}
	if finding == nil {
		t.Skip("this deployment has no finding without a fix task; nothing to hand over")
	}

	// Two agents: one that cannot edit and one that can. The first is the check
	// that matters — handing a fix to a review engine would produce a task that
	// runs, reports something reasonable, and changes nothing.
	body, status = client.get("/api/v1/agents")
	if status != http.StatusOK {
		t.Fatalf("the agents could not be read (%d)", status)
	}
	var agents struct {
		Items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			RuntimeType string `json:"runtimeType"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &agents); err != nil {
		t.Fatal(err)
	}
	coder, reader := "", ""
	for _, agent := range agents.Items {
		switch agent.RuntimeType {
		case "qwencode", "opencode", "goose":
			if coder == "" {
				coder = agent.ID
			}
		case "opencodereview", "langflow", "nodered", "n8n":
			if reader == "" {
				reader = agent.ID
			}
		}
	}
	if coder == "" {
		t.Fatal("this deployment has no agent on a runtime that can edit files; the handover cannot be checked")
	}

	if reader != "" {
		raw, status := client.do(http.MethodPost, "/api/v1/review-findings/"+finding.ID+"/fix", map[string]string{"agentId": reader})
		if status != http.StatusBadRequest {
			t.Errorf("handing a fix to a runtime that cannot edit answered %d, want 400: %s", status, first(raw, 200))
		}
	}

	raw, status := client.do(http.MethodPost, "/api/v1/review-findings/"+finding.ID+"/fix", map[string]string{"agentId": coder})
	if status != http.StatusAccepted {
		t.Fatalf("the handover answered %d: %s", status, first(raw, 200))
	}
	var handover struct {
		Task struct {
			ID     string `json:"id"`
			Input  string `json:"input"`
			Source string `json:"source"`
		} `json:"task"`
		Finding struct {
			Status    string `json:"status"`
			FixTaskID string `json:"fixTaskId"`
		} `json:"finding"`
	}
	if err := json.Unmarshal([]byte(raw), &handover); err != nil {
		t.Fatal(err)
	}
	// Everything the review established has to travel with the task. An agent
	// that has to go and find the problem again may fix a different one.
	if !strings.Contains(handover.Task.Input, finding.FilePath) {
		t.Error("the task does not name the file the finding is in")
	}
	if !strings.Contains(handover.Task.Input, finding.Message) {
		t.Error("the task does not carry what the review actually said")
	}
	if finding.Suggestion != "" && !strings.Contains(handover.Task.Input, finding.Suggestion) {
		t.Error("the task does not carry the code the review suggested")
	}
	if handover.Task.Source != "review" {
		t.Errorf("the task's source is %q; a task nobody typed has to say where it came from", handover.Task.Source)
	}
	if handover.Finding.FixTaskID != handover.Task.ID {
		t.Error("the finding does not point at the task, so nothing connects the two afterwards")
	}
	// The finding is not fixed. Asking for a fix is not having one, and a
	// platform that closed it here would be reporting its own hope.
	if handover.Finding.Status != "open" {
		t.Errorf("the finding was marked %q by asking for a fix; nothing has checked that it is fixed", handover.Finding.Status)
	}

	// Asking twice would queue a second agent onto the same line of the same file.
	raw, status = client.do(http.MethodPost, "/api/v1/review-findings/"+finding.ID+"/fix", map[string]string{"agentId": coder})
	if status != http.StatusConflict {
		t.Errorf("asking for a second fix answered %d, want 409: %s", status, first(raw, 200))
	}
	t.Logf("handed %s:%d to an agent as task %s; the finding is still %s", finding.FilePath, finding.StartLine, handover.Task.ID, handover.Finding.Status)
}
