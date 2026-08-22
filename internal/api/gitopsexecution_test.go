package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An exported definition carries a definition, not a Goal.
//
// That is deliberate — a version captures the definition, and the Goal is edited
// and versioned separately — but it has a consequence nobody was told about. An
// agent created from a document has no execution settings of its own, so it runs
// on the defaults: the prose runner, the default approval mode, the default
// limits, no tool policy. A definition exported from an agent that ran headless
// CLI with a tool policy arrives in the other cluster as a prose agent with none,
// looking identical in the list.
//
// It is the thing somebody moving a definition between clusters is least likely
// to notice, so the answer says which of the two happened rather than leaving
// them to find out from a run.
func TestImportSaysWhatItDidNotBring(t *testing.T) {
	body, err := os.ReadFile("gitops.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) importAgent(")
	if at < 0 {
		t.Fatal("importAgent is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\nfunc "); end >= 0 {
		handler = handler[:end]
	}
	// Creating and updating say different things, because they do different
	// things: a new agent gets the defaults, an existing one keeps what it had.
	for _, answer := range []struct{ mode, execution string }{
		{`"mode": "created"`, `"execution": "defaults"`},
		{`"mode": "updated"`, `"execution": "kept"`},
	} {
		index := strings.Index(handler, answer.mode)
		if index < 0 {
			t.Errorf("the import no longer answers with %s", answer.mode)
			continue
		}
		line := handler[index:]
		if end := strings.Index(line, "\n"); end >= 0 {
			line = line[:end]
		}
		if !strings.Contains(line, answer.execution) {
			t.Errorf("%s does not say what happened to the execution settings; the two cases are indistinguishable to whoever imported the file", answer.mode)
		}
	}

	console, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "Agents.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	text := string(console)
	if !strings.Contains(text, "execution?:string") {
		t.Error("the console does not read what the import said about execution settings")
	}
	for _, phrase := range []string{"실행 설정(Goal)은 정의에 포함되지 않으므로", "기존 실행 설정(Goal)은 그대로 유지했습니다"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the console does not tell the person: %q", phrase)
		}
	}
}
