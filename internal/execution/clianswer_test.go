package execution

import (
	"strings"
	"testing"
)

// An agent that cannot reach the model writes the failure where its answer
// belongs and still reports success — exit 0, is_error false. The task was then
// recorded as completed with the error text as its result: a run that did
// nothing wearing the badge of one that worked.
//
// Observed against a real runtime on a real cluster.
func TestAnAnswerThatIsAnAPIErrorIsNotAnAnswer(t *testing.T) {
	stdout := `[{"type":"result","subtype":"success","is_error":false,"session_id":"s1","num_turns":1,
		"result":"[API Error: Streaming request received a non-SSE response (HTTP 200, Content-Type: application/json)]"}]`
	_, err := parseCLIRun(stdout, "", 0)
	if err == nil {
		t.Fatal("an agent that could not call the model was recorded as succeeding")
	}
	if !strings.Contains(err.Error(), "API Error") {
		t.Errorf("the failure does not carry the agent's own words: %v", err)
	}
}

// An agent that quotes the words while doing its job is doing its job. Refusing
// that run is the opposite mistake, and just as invisible.
func TestAnAnswerThatMentionsAnErrorIsStillAnAnswer(t *testing.T) {
	stdout := `[{"type":"result","subtype":"success","is_error":false,"session_id":"s1","num_turns":2,
		"result":"로그에서 [API Error: rate limited] 를 찾았고, 재시도 설정을 고쳤습니다."}]`
	run, err := parseCLIRun(stdout, "", 0)
	if err != nil {
		t.Fatalf("an answer that merely mentions an error was refused: %v", err)
	}
	if !strings.Contains(run.Result, "재시도 설정") {
		t.Errorf("the answer was not kept: %q", run.Result)
	}
}

// The ordinary case stays ordinary.
func TestAPlainAnswerIsUnaffected(t *testing.T) {
	stdout := `[{"type":"result","subtype":"success","is_error":false,"session_id":"s1","num_turns":1,
		"result":"안녕하세요."}]`
	run, err := parseCLIRun(stdout, "", 0)
	if err != nil {
		t.Fatalf("a plain answer failed: %v", err)
	}
	if run.Result != "안녕하세요." {
		t.Errorf("the answer changed: %q", run.Result)
	}
}
