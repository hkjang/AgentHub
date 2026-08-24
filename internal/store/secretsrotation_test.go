package store

import (
	"os"
	"strings"
	"testing"
)

// A secret left behind by an unfinished key rotation is a state somebody can
// fix, not a fault in this platform.
//
// Measured: attaching such a secret to a workspace answered
//
//	500 요청을 처리하지 못했습니다: secret "probe-secret" was encrypted with key
//	version 14 but the active version is 15
//
// — this platform's own key bookkeeping, in English, delivered as though the
// request had broken something. The reveal is what the console calls to check
// the caller owns the secret, so it is the first thing anybody meets after a
// rotation that did not finish.
func TestAStaleKeyIsAnAnswerNotAFault(t *testing.T) {
	body, err := os.ReadFile("secrets.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) RevealPersonalSecret(")
	if at < 0 {
		t.Fatal("the reveal is gone; this guard is reading nothing")
	}
	reveal := source[at:]
	if end := strings.Index(reveal, "\nfunc "); end >= 0 {
		reveal = reveal[:end]
	}
	if strings.Contains(reveal, "was encrypted with key version") {
		t.Error("a person is still shown this platform's key arithmetic, in English")
	}
	if !strings.Contains(reveal, "Conflict{Message:") {
		t.Error("a recoverable state is still reported as a server fault")
	}
	// And it has to say what to do about it.
	if !strings.Contains(reveal, "다시 저장하면") {
		t.Error("the message names the problem without naming the remedy")
	}
}

// A request naming something this platform does not have is the caller's
// mistake, and it was answered as a fault in the platform.
//
// Measured: creating an agent server with a kind nobody defined answered
// "500 요청을 처리하지 못했습니다: 알 수 없는 서버 종류입니다: nonsense" — a
// sentence that tells whoever mistyped a field that they broke something.
//
// The store already separated the two states a caller can fix: ErrNotFound and
// ErrConflict. These sat outside both and fell through to the fallback, which
// exists for faults.
func TestARefusalTheCallerCanFixIsNotAServerFault(t *testing.T) {
	// Every refusal below is about the request, not about what is stored.
	for _, item := range []struct{ file, needle string }{
		{"agentserver.go", `Invalid{Message: "알 수 없는 서버 종류입니다: " + item.Kind}`},
		{"agentserver.go", `Invalid{Message: "이름과 주소가 필요합니다"}`},
		{"control.go", `Invalid{Message: fmt.Sprintf("알 수 없는 기억 범위 %q", item.Scope)}`},
		{"directive.go", `Invalid{Message: "알 수 없는 지시 종류입니다: " + kind}`},
		{"mcp.go", `Invalid{Message: fmt.Sprintf("알 수 없는 도구 정책 모드 %q", policy.Mode)}`},
		{"operations.go", `Invalid{Message: "보관 기간은 최대 3650일입니다"}`},
	} {
		body, err := os.ReadFile(item.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), item.needle) {
			t.Errorf("%s: a refusal the caller can fix still reaches the fault path", item.file)
		}
	}
	// And the two that depend on what is stored stay conflicts: asking again with
	// the same request can succeed once the state changes.
	for _, item := range []struct{ file, needle string }{
		{"execution.go", `Conflict{Message: "인계된 작업은 완료 또는 취소로만 마무리할 수 있습니다"}`},
		{"agentversion.go", `Conflict{Message: fmt.Sprintf("v%d에 통과한 사전검증 결과가 없어 승격할 수 없습니다", version)}`},
	} {
		body, err := os.ReadFile(item.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), item.needle) {
			t.Errorf("%s: a state a person can change is reported as a bad request", item.file)
		}
	}
	// The sentinel must not end up in front of the message, for the same reason
	// Conflict carries its own sentence.
	body, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "func (i Invalid) Error() string { return i.Message }") {
		t.Error("the sentinel's own word would be printed in front of the sentence")
	}
}

// The notification bell reads two things on every poll and the table had nothing
// but a primary key.
//
// Measured on a running deployment: both queries were sequential scans over
// every notice ever written, and notifications_pkey had four index scans against
// agent_tasks_agent_idx's eleven thousand. Notices are swept only once read, so
// a person who stops clicking the bell keeps the table growing under both.
func TestTheNotificationBellHasAnIndexToReadFrom(t *testing.T) {
	body, err := os.ReadFile("migrations/065_notification_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	// The listing: one person's newest notices.
	// Shaped like the bell's own ordering, so the fifty rows come off the index
	// with no sort — measured at 300,000 notices: 11 ms to 0.34 ms, where an
	// index on (user_id, created_at) alone still cost 4.5 ms sorting.
	if !strings.Contains(sql, "notifications(user_id, (CASE WHEN read_at IS NULL THEN 0 ELSE 1 END), created_at DESC)") {
		t.Error("the bell's listing still sorts every notice it is given, or scans them all")
	}
	// The count: only what is still waiting.
	if !strings.Contains(sql, "notifications(user_id) WHERE read_at IS NULL") {
		t.Error("the unread count has no index shaped like the question it asks")
	}
	// And the sweep, which deletes by when a notice was read.
	if !strings.Contains(sql, "notifications(read_at) WHERE read_at IS NOT NULL") {
		t.Error("retention deletes by a column nothing indexes")
	}
	// The duplicate index on the highest-volume table, which every step insert
	// maintained for nothing.
	if !strings.Contains(sql, "DROP INDEX IF EXISTS agent_run_steps_created_idx") {
		t.Error("agent_run_steps still carries two identical indexes on created_at")
	}
}

// Two lookups on agent_runtimes ran without an index, on paths that run
// constantly rather than when somebody opens a screen.
//
// Measured on 200,000 runtime rows: proving which runtime a token belongs to —
// which every call from a Pod does — cost 13 ms of sequential scan, and asking
// for an agent's current runtime, which every task that needs one asks first,
// cost 14 ms of parallel scan. Both are answered from an index now, at 0.2 ms.
func TestTheRuntimeLookupsOnTheHotPathHaveIndexes(t *testing.T) {
	body, err := os.ReadFile("migrations/066_runtime_lookup_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.Contains(sql, "agent_runtimes(gateway_token_hash) WHERE gateway_token_hash IS NOT NULL") {
		t.Error("a runtime proving who it is still scans every runtime ever created")
	}
	// Filtered and ordered as the query is, so the newest row is the first entry
	// rather than the result of sorting everything the agent has run.
	if !strings.Contains(sql, "agent_runtimes(agent_id, created_at DESC) WHERE desired_state <> 'deleted'") {
		t.Error("finding an agent's current runtime still scans the table, or still sorts it")
	}
	// The queries these are for, so a rewrite that changes their shape fails here
	// rather than quietly losing the index.
	runtime, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtime), "WHERE agent_id=$1 AND desired_state<>'deleted' ORDER BY created_at DESC LIMIT 1") {
		t.Error("the agent's-current-runtime query no longer matches the index built for it")
	}
	tool, err := os.ReadFile("toolapproval.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tool), "WHERE gateway_token_hash=$1 AND desired_state<>'deleted'") {
		t.Error("the token lookup no longer matches the index built for it")
	}
}
