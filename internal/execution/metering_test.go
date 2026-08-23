package execution

import (
	"os"
	"strings"
	"testing"
)

// A run that says it was metered has to leave the spend where the report reads
// it.
//
// The usage report adds up steps: it joins agent_run_steps and sums their prompt
// and completion tokens. A backend that records tokens only on the run itself
// therefore contributes nothing to it — and because the run says metering=agent,
// the report does not count it among the runs it could not see either. The
// result is a confident zero, which is the one answer this platform's own
// comments say never to print.
//
// It was true of four backends at once when this was written. On the deployment
// it was found on, every agent-server agent read "in 0 out 0" beside runs whose
// own records said eighty-four tokens.
func TestABackendThatMetersRecordsTokensOnItsStep(t *testing.T) {
	// Each backend that claims real usage, and the file it lives in.
	for _, backend := range []struct{ name, file string }{
		{"에이전트 서버", "agentserver.go"},
		{"프로토콜 실행", "rpc.go"},
		{"에이전트 실행", "cli.go"},
		{"코드 리뷰", "review.go"},
		{"ACP 실행", "acp.go"},
		{"조사 실행", "investigate.go"},
	} {
		body, err := os.ReadFile(backend.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		// It claims real usage...
		if !strings.Contains(source, "store.MeteringAgent") {
			continue
		}
		// ...so the step it writes has to carry the split the report sums.
		//
		// Looked for on the step rather than anywhere in the file: this backend
		// reads "prompt_tokens" out of the server's own statistics, so a search for
		// the word alone finds it whether or not any of it reaches the step — which
		// is the exact bug, passing its own guard.
		if !stepCarriesTokens(source) {
			t.Errorf("%s 는 실사용량을 계량한다고 하면서 단계에 토큰을 남기지 않습니다 (%s) — 사용량 보고서는 단계를 더하므로 이 실행은 0원으로 보이고, 계량됐다고 표시돼 있어 '알 수 없음'으로도 세지 않습니다",
				backend.name, backend.file)
		}
	}
}

// stepCarriesTokens says whether the step this backend writes is given the token
// split, either in the literal or by assignment afterwards.
func stepCarriesTokens(source string) bool {
	if strings.Contains(source, "record.PromptTokens") && strings.Contains(source, "record.CompletionTokens") {
		return true
	}
	for _, literal := range strings.Split(source, "store.AgentRunStep{")[1:] {
		if end := strings.Index(literal, "\n\t}"); end >= 0 {
			literal = literal[:end]
		}
		if strings.Contains(literal, "PromptTokens:") && strings.Contains(literal, "CompletionTokens:") {
			return true
		}
	}
	return false
}
