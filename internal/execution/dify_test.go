package execution

import (
	"strings"
	"testing"
)

// A workflow app answers with named outputs whose names its author chose, so
// there is no field to read. One output is the answer; several are kept together
// rather than one being picked and the rest silently dropped.
func TestDifyWorkflowAnswer(t *testing.T) {
	single := `{"workflow_run_id":"w1","data":{"id":"r1","status":"succeeded","outputs":{"result":"정리했습니다"},"total_tokens":128,"total_steps":4}}`
	text, usage, err := difyAnswer("workflow", []byte(single))
	if err != nil || text != "정리했습니다" {
		t.Fatalf("answer = %q, err = %v", text, err)
	}
	if usage["total_tokens"] != 128 || usage["total_steps"] != 4 {
		t.Errorf("the app's own counts were not kept: %#v", usage)
	}

	many := `{"data":{"status":"succeeded","outputs":{"summary":"요약","score":7}}}`
	text, _, err = difyAnswer("workflow", []byte(many))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"summary", "요약", "score"} {
		if !strings.Contains(text, want) {
			t.Errorf("several outputs must all survive, %q is missing:\n%s", want, text)
		}
	}
}

// A workflow that failed answers with HTTP 200 and says so in the body. Recording
// that as a result would be recording a failure as work done.
func TestDifyWorkflowFailureIsNotAResult(t *testing.T) {
	body := `{"data":{"status":"failed","error":"Node LLM failed: rate limited","outputs":{},"total_tokens":12}}`
	_, usage, err := difyAnswer("workflow", []byte(body))
	if err == nil {
		t.Fatal("a failed workflow must not be recorded as an answer")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("the app's own reason should survive: %v", err)
	}
	if usage["total_tokens"] != 12 {
		t.Errorf("what it spent before failing is still worth recording: %#v", usage)
	}
	// An empty success is not a success either.
	if _, _, err := difyAnswer("workflow", []byte(`{"data":{"status":"succeeded","outputs":{}}}`)); err == nil {
		t.Error("a workflow with no outputs must be reported rather than recorded as empty")
	}
}

// A chat app puts its answer somewhere else entirely, which is why the kind is
// stored with the app rather than guessed per call.
func TestDifyChatAnswer(t *testing.T) {
	body := `{"answer":"안녕하세요","conversation_id":"c1","metadata":{"usage":{"total_tokens":33,"prompt_tokens":20}}}`
	text, usage, err := difyAnswer("chat", []byte(body))
	if err != nil || text != "안녕하세요" {
		t.Fatalf("answer = %q, err = %v", text, err)
	}
	if usage["total_tokens"] != float64(33) {
		t.Errorf("usage = %#v", usage)
	}
	if _, _, err := difyAnswer("chat", []byte(`{"answer":"  "}`)); err == nil {
		t.Error("an empty answer must not be accepted")
	}
	if _, _, err := difyAnswer("chat", []byte("not json")); err == nil {
		t.Error("an unreadable answer must be reported")
	}
}
