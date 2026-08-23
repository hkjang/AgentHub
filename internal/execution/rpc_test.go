package execution

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// These lines are what the agent actually wrote.
//
// Captured by running the published agent in the image this platform ships,
// through the platform's own wrapper, against a gateway stub — the same path a
// task takes, minus the model's opinions. A fixture somebody typed would only
// prove the reader can read what its author expected.
func rpcFixture(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("testdata/pi-rpc-turn.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// readRPCStream is the reading half of the conversation, exercised without a
// runtime: the events are the contract, and they can be replayed.
func readRPCStream(t *testing.T, stream string) (rpcResult, bool) {
	t.Helper()
	var result rpcResult
	settled := false
	lines := bufio.NewScanner(strings.NewReader(stream))
	lines.Buffer(make([]byte, 0, 64*1024), rpcMaxLine)
	for lines.Scan() {
		var event rpcEvent
		if err := json.Unmarshal(lines.Bytes(), &event); err != nil {
			continue
		}
		switch event.Type {
		case "turn_end":
			result.Turns++
			result.ToolCalls += len(event.ToolResults)
			if text := rpcText(event); text != "" {
				result.Answer = text
			}
			if event.Message.Usage.TotalTokens > result.Tokens {
				result.Tokens = event.Message.Usage.TotalTokens
			}
			result.StopReason = event.Message.StopReason
		case "agent_settled":
			settled = true
		}
	}
	return result, settled
}

func TestOneTurnIsReadFromTheAgentsOwnEvents(t *testing.T) {
	result, settled := readRPCStream(t, rpcFixture(t))
	if !settled {
		t.Fatal("the stream never said the agent had settled; a runner would wait for ever")
	}
	if result.Answer == "" {
		t.Fatal("the agent's answer was not read out of its events")
	}
	if result.Turns != 1 {
		t.Errorf("read %d turn(s) from a one-turn conversation", result.Turns)
	}
	// The usage is the agent's own, per message, which is what lets a run through
	// this backend be metered rather than described as unmetered.
	if result.Tokens <= 0 {
		t.Error("the conversation reports no tokens, so the run would be recorded as free")
	}
	if result.StopReason == "" {
		t.Error("why the turn ended was dropped")
	}
}

// `agent_end` is not the end. It carries willRetry, so a turn ending is not the
// work ending — a runner that stopped there would cut off a retry the agent was
// about to make.
func TestAgentEndIsNotTheEnd(t *testing.T) {
	stream := rpcFixture(t)
	if !strings.Contains(stream, `"type":"agent_end"`) {
		t.Fatal("this fixture is meant to contain an agent_end")
	}
	// Everything up to and including agent_end, with the settle removed.
	cut := strings.Index(stream, `{"type":"agent_settled"`)
	if cut < 0 {
		t.Fatal("this fixture is meant to contain agent_settled")
	}
	_, settled := readRPCStream(t, stream[:cut])
	if settled {
		t.Fatal("a stream that ended at agent_end was read as settled")
	}
}

// A line that is not an event is the agent's own noise. Treating it as a failure
// would end a run because something printed a warning.
func TestNoiseInTheStreamIsNotAFailure(t *testing.T) {
	stream := "starting up…\n" + rpcFixture(t) + "\nwarning: something\n"
	result, settled := readRPCStream(t, stream)
	if !settled || result.Answer == "" {
		t.Fatalf("noise around the events broke the reading: settled=%v answer=%q", settled, result.Answer)
	}
}

// The endpoint is checked rather than believed, because the environment variable
// this platform would once have trusted turns out to mean nothing to these
// agents — they go to a vendor unless their own configuration says otherwise.
func TestTheReportedEndpointIsComparedWithTheGateway(t *testing.T) {
	if !sameEndpoint("http://gateway/v1", "http://gateway/v1/") {
		t.Error("a trailing slash raised a false alarm about an agent talking to a vendor")
	}
	if sameEndpoint("https://api.openai.com/v1", "http://gateway/v1") {
		t.Error("an agent pointed at a vendor was accepted as pointed at the gateway")
	}
	if sameEndpoint("", "http://gateway/v1") {
		t.Error("an agent that reported nothing was accepted as pointed at the gateway")
	}
}

// Failing to hold the conversation is worth another attempt; the agent failing
// its task is not.
func TestOnlyTheConversationFailingIsRetried(t *testing.T) {
	for _, one := range []struct {
		message string
		retry   bool
	}{
		{"에이전트가 끝났다고 알리기 전에 연결이 끊겼습니다: ", true},
		{"에이전트의 출력을 읽지 못했습니다: broken pipe", true},
		{"에이전트에 작업을 전달하지 못했습니다: closed", true},
		{"에이전트가 아무 답도 남기지 않고 끝났습니다: ", false},
		{"에이전트가 제한 시간 안에 끝내지 못했습니다", false},
	} {
		if got := rpcRetryable(errorString(one.message)); got != one.retry {
			t.Errorf("%q retryable=%v, want %v", one.message, got, one.retry)
		}
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// The line the platform sends has to be the line the agent was seen accepting.
//
// The two halves of steering were established separately: the agent takes a
// steer and a follow_up mid-conversation, and the platform records and claims
// what somebody said. This is the join, and a join nobody checks is where two
// working halves stop adding up.
//
// The expected strings are what was typed at the agent by hand, and answered
// with {"command":"steer","success":true} and {"command":"follow_up","success":true}.
func TestTheDirectiveLineIsWhatTheAgentAccepted(t *testing.T) {
	for _, one := range []struct {
		directive store.RunDirective
		want      string
	}{
		{store.RunDirective{Kind: "steer", Message: "실제로는 repository layer를 먼저 분리해"},
			`{"message":"실제로는 repository layer를 먼저 분리해","type":"steer"}`},
		{store.RunDirective{Kind: "follow_up", Message: "그 다음 테스트를 추가해"},
			`{"message":"그 다음 테스트를 추가해","type":"follow_up"}`},
	} {
		line, err := directiveLine(one.directive)
		if err != nil {
			t.Fatal(err)
		}
		if string(line) != one.want {
			t.Errorf("the platform sends %s\nthe agent was seen accepting %s", line, one.want)
		}
		// One line. The protocol is newline-delimited, so a message with a newline
		// in it would be read as two commands — the second of them nonsense.
		if strings.Contains(string(line), "\n") {
			t.Errorf("the line carries a newline and would be read as two commands: %s", line)
		}
	}

	// A message with a newline is what a person typing into a text box produces,
	// and it must survive as one line.
	line, err := directiveLine(store.RunDirective{Kind: "steer", Message: "먼저 이것\n그 다음 저것"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "\n") {
		t.Errorf("a message with a newline was not escaped into one line: %s", line)
	}
	// And it is still the same command, not a different one.
	var decoded map[string]string
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "steer" || !strings.Contains(decoded["message"], "그 다음 저것") {
		t.Errorf("the escaped line no longer carries the directive: %v", decoded)
	}
}
