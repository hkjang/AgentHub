package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deleting an agent cascades through twelve tables. Most of what goes is history,
// and deleting history is what somebody deleting an agent is asking for. Work in
// flight is not history: a task running right now, one parked at an approval
// somebody is about to give, one handed to a person finishing it in the runtime.
// Those disappeared mid-sentence with nobody told, under a dialog offering to
// delete "the definition and the runtime".
func TestDeletingAnAgentRefusesToTakeWorkWithIt(t *testing.T) {
	body, err := os.ReadFile("lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) deleteAgent(")
	if at < 0 {
		t.Fatal("deleteAgent is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\n}\n"); end >= 0 {
		handler = handler[:end]
	}
	if !strings.Contains(handler, "AgentWorkInFlight(") {
		t.Error("the agent is deleted without asking whether anything is running in it")
	}
	if !strings.Contains(handler, "work_in_flight") {
		t.Error("nothing refuses the deletion; counting the work and deleting it anyway is the same as not counting it")
	}
	// The check has to come before anything is destroyed. The runtime goes first
	// in this handler, and a deleted Pod is not put back by a later refusal.
	flight, destroy := strings.Index(handler, "AgentWorkInFlight("), strings.Index(handler, "spawner.Delete(")
	if destroy >= 0 && flight > destroy {
		t.Error("the runtime is deleted before the work in it is counted")
	}
	// A count that cannot be read must not be read as zero — but it also must not
	// stop somebody deleting an agent, so it is logged and allowed.
	if !strings.Contains(handler, "could not be counted") {
		t.Error("a failed count is not distinguished from an empty one")
	}
}

// And the confirmation has to describe what it destroys. It named the definition
// and the Pod, which is the part somebody pictures; the history went unmentioned.
func TestTheDeleteDialogSaysWhatItDestroys(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "Agents.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	at := strings.Index(string(body), `title="에이전트를 삭제할까요?"`)
	if at < 0 {
		t.Fatal("the delete confirmation is gone; this guard is reading nothing")
	}
	// To the end of this dialog, not to the first "/>" — that one is a <br/>
	// inside the message, and slicing there cut off the half being checked for.
	dialog := string(body)[at:]
	if end := strings.Index(dialog, "onCancel="); end >= 0 {
		dialog = dialog[:end]
	}
	for _, what := range []string{"기록", "산출물", "되돌릴 수 없습니다"} {
		if !strings.Contains(dialog, what) {
			t.Errorf("the confirmation does not mention %q; somebody agrees to lose the definition and loses the history too", what)
		}
	}
}
