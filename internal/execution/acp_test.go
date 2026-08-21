package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/acp"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// What the platform answers when an agent asks permission and nobody is at the
// keyboard. This table is the security contract of the whole runner: get it
// wrong in the permissive direction and an unattended task deletes a file
// nobody agreed to.
func TestWhatAnUnattendedRunAllowsItselfToDo(t *testing.T) {
	cases := []struct {
		mode  string
		kind  string
		allow bool
	}{
		// Reading is always fine; it is why the agent was started.
		{"default", "read", true},
		{"default", "search", true},
		{"default", "fetch", true},
		{"plan", "read", true},
		// "Ask first" with nobody there means no.
		{"default", "edit", false},
		{"default", "execute", false},
		{"default", "delete", false},
		{"plan", "edit", false},
		{"plan", "execute", false},
		// The workspace, but not the world outside it.
		{"auto-edit", "edit", true},
		{"auto-edit", "move", true},
		{"auto-edit", "execute", false},
		{"auto-edit", "delete", false},
		// Chosen deliberately, and only these two.
		{"auto", "execute", true},
		{"yolo", "delete", true},
		// A kind this platform has never heard of is not a reason to say yes.
		{"default", "launch_missiles", false},
		{"auto-edit", "launch_missiles", false},
		{"", "edit", false},
	}
	for _, item := range cases {
		if got := acpAllows(item.mode, item.kind); got != item.allow {
			t.Errorf("mode %q, kind %q = %v, want %v", item.mode, item.kind, got, item.allow)
		}
	}
}

// A stop reason is the agent saying why it finished, and the difference between
// "try again" and "change the Goal" is what an operator reads off the run.
func TestStopReasonsBecomeSomethingAPersonCanActOn(t *testing.T) {
	goal := store.AgentGoal{MaxToolCalls: 3}
	cases := []struct {
		reason    string
		turn      *acpTurn
		wantFail  string
		retryable bool
	}{
		{reason: "end_turn", turn: &acpTurn{}},
		{reason: "", turn: &acpTurn{}},
		{reason: "max_tokens", turn: &acpTurn{}, wantFail: "컨텍스트"},
		{reason: "max_turn_requests", turn: &acpTurn{}, wantFail: "모델 호출"},
		{reason: "refusal", turn: &acpTurn{}, wantFail: "거부"},
		// Cancelled because the platform stopped it at the Goal's budget: saying so
		// is the difference between a mystery and a number to raise.
		{reason: "cancelled", turn: &acpTurn{toolCalls: 4}, wantFail: "한도(3)"},
		// Cancelled for some other reason is a bad moment, so it may be retried.
		{reason: "cancelled", turn: &acpTurn{toolCalls: 1}, wantFail: "중단", retryable: true},
		{reason: "invented_by_a_future_agent", turn: &acpTurn{}, wantFail: "알 수 없는"},
	}
	for _, item := range cases {
		err, retryable := acpStopFailure(item.reason, goal, item.turn)
		if item.wantFail == "" {
			if err != nil {
				t.Errorf("%q reported %v, want success", item.reason, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), item.wantFail) {
			t.Errorf("%q = %v, want one mentioning %q", item.reason, err, item.wantFail)
		}
		if retryable != item.retryable {
			t.Errorf("%q retryable = %v, want %v", item.reason, retryable, item.retryable)
		}
	}
}

// The turn's updates arrive on the client's read loop while the caller waits, so
// what a run records is assembled from a stream rather than read from a result.
func TestATurnAssemblesWhatTheAgentSaidAndDid(t *testing.T) {
	turn := &acpTurn{}
	turn.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", Content: acp.ContentBlock{Text: "먼저 "}})
	turn.update(acp.SessionUpdate{SessionUpdate: "agent_thought_chunk", Content: acp.ContentBlock{Text: "속마음"}})
	turn.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", Content: acp.ContentBlock{Text: "확인했습니다."}})
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1", Title: "read main.go", Kind: "read", Status: "pending"})
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "t1", Status: "completed"})
	turn.update(acp.SessionUpdate{SessionUpdate: "usage_update", Used: 1200, Size: 128000})

	if turn.answer() != "먼저 확인했습니다." {
		t.Errorf("answer = %q", turn.answer())
	}
	// The agent's private reasoning is counted, not stored: it is not the answer,
	// and a durable record is the wrong place for a model's scratch work.
	if turn.thoughts != 1 || strings.Contains(turn.answer(), "속마음") {
		t.Errorf("thoughts leaked into the answer: %q", turn.answer())
	}
	if turn.toolCalls != 1 || turn.records()[0].Status != "completed" {
		t.Errorf("tool call not tracked: %#v", turn.records())
	}
	if turn.contextUsed != 1200 || turn.contextSize != 128000 {
		t.Errorf("context usage = %d/%d", turn.contextUsed, turn.contextSize)
	}
}

// Spend is metered when the agent reports it, and only then. The protocol has no
// field for it — its usage_update is how full the context window is, not what was
// bought — so a real agent puts the numbers in its own extension, and a run whose
// agent says nothing is recorded as unmetered rather than credited with a guess.
func TestSpendIsCountedOnlyWhenTheAgentReportsIt(t *testing.T) {
	reported := &acpTurn{}
	reported.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk",
		Usage: acp.Usage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}})
	reported.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1",
		Usage: acp.Usage{InputTokens: 200, OutputTokens: 40, TotalTokens: 240}})
	if reported.totalTokens != 390 || reported.inputTokens != 320 || reported.outputTokens != 70 {
		t.Errorf("spend = %d (%d in / %d out)", reported.totalTokens, reported.inputTokens, reported.outputTokens)
	}

	// An agent that reports only the two halves still gets counted; one that
	// reports nothing leaves the run unmetered.
	halves := &acpTurn{}
	halves.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", Usage: acp.Usage{InputTokens: 10, OutputTokens: 5}})
	if halves.totalTokens != 15 {
		t.Errorf("halves = %d, want them added up", halves.totalTokens)
	}
	silent := &acpTurn{}
	silent.update(acp.SessionUpdate{SessionUpdate: "agent_message_chunk", Content: acp.ContentBlock{Text: "안녕"}})
	silent.update(acp.SessionUpdate{SessionUpdate: "usage_update", Used: 9000, Size: 128000})
	if silent.totalTokens != 0 {
		t.Errorf("context occupancy was counted as spend: %d", silent.totalTokens)
	}
}

// A permission answered for a tool the platform has not seen announced still has
// to appear on the run, and one already announced must not be duplicated.
func TestEveryDecisionLandsOnExactlyOneToolRecord(t *testing.T) {
	turn := &acpTurn{}
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1", Title: "write config", Kind: "edit"})
	turn.decide(permissionFor("t1", "write config", "edit"), "edit", false)
	turn.decide(permissionFor("t2", "rm -rf build", "delete"), "delete", false)

	records := turn.records()
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Decision != "denied" || records[1].Decision != "denied" || records[1].Title != "rm -rf build" {
		t.Errorf("decisions not recorded: %#v", records)
	}
	if turn.denied != 2 || turn.granted != 0 {
		t.Errorf("counted %d denied / %d granted", turn.denied, turn.granted)
	}
	if !strings.Contains(acpToolOutcome(records[1]), "거절됨") {
		t.Errorf("outcome = %q", acpToolOutcome(records[1]))
	}
}

func permissionFor(id, title, kind string) acp.PermissionRequest {
	var request acp.PermissionRequest
	request.ToolCall.ToolCallID = id
	request.ToolCall.Title = title
	request.ToolCall.Kind = kind
	request.Options = []acp.PermissionOption{
		{OptionID: "y", Kind: "allow_once"}, {OptionID: "n", Kind: "reject_once"},
	}
	return request
}

// A runtime that offers a backend must have something to run for it, and a
// command with no backend offered is a command nobody can reach. Either way round
// it passes every form in the console and fails at the moment a task starts.
func TestEveryRunnerARuntimeOffersHasACommand(t *testing.T) {
	for _, name := range runtimetype.Supported {
		for _, runner := range []string{runtimetype.RunnerACP, runtimetype.RunnerCLI, runtimetype.RunnerInvestigate} {
			supported := runtimetype.SupportsRunner(name, runner)
			command := runtimetype.RunnerCommand(name, runner)
			if supported && len(command) == 0 {
				t.Errorf("%s lists the %s runner but names no command to start", name, runner)
			}
			if !supported && len(command) > 0 {
				t.Errorf("%s names a %s command but does not list the runner, so nobody can choose it", name, runner)
			}
		}
	}
}

// An agent may announce a tool call with its kind and then ask permission without
// it. Judging the request alone would refuse a file edit under a mode that allows
// file edits, and the operator would have no way to see why.
//
// Found against BrowserCode, which announces `tool_call` with kind "edit" and
// then asks with kind "other".
func TestAPermissionIsJudgedByWhatTheAgentAlreadyDeclared(t *testing.T) {
	turn := &acpTurn{}
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1", Title: "write probe.txt", Kind: "edit"})

	vague := permissionFor("t1", "/tmp", "other")
	if got := acpKind(vague, turn); got != "edit" {
		t.Errorf("kind = %q, want the one the agent declared for this call", got)
	}
	if !acpAllows("auto-edit", acpKind(vague, turn)) {
		t.Error("a file edit was refused under a mode that allows file edits")
	}

	// The request's own kind wins when it says something: the agent may have
	// changed its mind about what this call is, and the later word is the one to
	// answer.
	explicit := permissionFor("t1", "run tests", "execute")
	if got := acpKind(explicit, turn); got != "execute" {
		t.Errorf("kind = %q, want the request's own", got)
	}

	// Nothing declared anywhere stays unknown rather than becoming something
	// convenient, so the strict modes still refuse it.
	unknown := permissionFor("t9", "mystery", "other")
	if got := acpKind(unknown, turn); got != "other" {
		t.Errorf("kind = %q, want it left alone", got)
	}
	if acpAllows("auto-edit", acpKind(unknown, turn)) {
		t.Error("an undeclared tool call was allowed")
	}
	// And a call the agent itself called "other" is not upgraded by this lookup:
	// Goose labels everything that way, and reading intent into it would be the
	// platform inventing a fact the agent declined to state.
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t2", Title: "rm -rf", Kind: "other"})
	if got := acpKind(permissionFor("t2", "rm -rf", "other"), turn); got != "other" {
		t.Errorf("kind = %q, want other", got)
	}
}

// The run record and the timeline event have to agree about what a tool call was.
// The platform judges by a resolved kind — the request's own, or the one the
// agent declared for that call — and the record keeps that same kind rather than
// the vaguer one on the request.
func TestTheRecordKeepsTheKindThePlatformJudgedBy(t *testing.T) {
	turn := &acpTurn{}
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1", Title: "write probe.txt", Kind: "edit"})
	request := permissionFor("t1", "/tmp", "other")
	turn.decide(request, acpKind(request, turn), true)

	records := turn.records()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Kind != "edit" {
		t.Errorf("recorded kind = %q, want the one the decision was made on", records[0].Kind)
	}
	if !strings.Contains(acpToolOutcome(records[0]), "edit") {
		t.Errorf("the step's text does not say what kind of call it was: %q", acpToolOutcome(records[0]))
	}
}

// Who answers the agent, and when. This is the whole of the new behaviour: a
// Goal that asks for human approval turns a refusal into a question, and one
// that does not keeps the platform answering alone.
func TestWhoAnswersTheAgent(t *testing.T) {
	cases := []struct {
		name      string
		goal      store.AgentGoal
		kind      string
		wantAsk   bool
		wantAllow bool
	}{
		{
			// Reading is why the agent was started. Nobody is woken for it, even
			// when the Goal wants a person for everything else.
			name: "reading never wakes anybody",
			goal: store.AgentGoal{ApprovalRequired: true, ApprovalMode: "default"},
			kind: "read", wantAllow: true,
		},
		{
			name: "a change goes to a person when the Goal asks for one",
			goal: store.AgentGoal{ApprovalRequired: true, ApprovalMode: "default"},
			kind: "edit", wantAsk: true,
		},
		{
			// The combination that used to be refused outright: permissive mode and
			// human approval together. The person wins.
			name: "even a permissive mode still asks when the Goal asks",
			goal: store.AgentGoal{ApprovalRequired: true, ApprovalMode: "yolo"},
			kind: "execute", wantAsk: true,
		},
		{
			name: "without the Goal asking, the mode decides alone",
			goal: store.AgentGoal{ApprovalMode: "auto-edit"},
			kind: "edit", wantAllow: true,
		},
		{
			name: "and refuses alone",
			goal: store.AgentGoal{ApprovalMode: "default"},
			kind: "execute", wantAllow: false,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			asked, allowed := permissionRoute(item.goal, item.kind)
			if asked != item.wantAsk {
				t.Errorf("asks a person = %v, want %v", asked, item.wantAsk)
			}
			if !asked && allowed != item.wantAllow {
				t.Errorf("allowed = %v, want %v", allowed, item.wantAllow)
			}
		})
	}
}

// The approval mode judges by the kind of tool, and Goose and BrowserCode report
// nearly everything as `other`. For those runs the mode has exactly two settings
// — refuse everything, or allow everything — and neither is what an operator
// wants. A named rule is the way out, so these pin what the names decide.
func TestANamedToolRuleDecidesBeforeTheMode(t *testing.T) {
	policy := store.ACPToolPolicy{Deny: []string{"rm -rf", "git push"}, Allow: []string{"npm test"}}
	for _, tc := range []struct {
		title   string
		allowed bool
		decided bool
	}{
		{"Run `npm test` in /workspace", true, true},
		{"rm -rf /workspace/build", false, true},
		{"Force-push with git push --force", false, true},
		{"Write /workspace/main.go", false, false}, // no rule: the mode still answers
	} {
		allowed, decided := namedToolDecision(policy, tc.title)
		if allowed != tc.allowed || decided != tc.decided {
			t.Errorf("%q → allowed=%v decided=%v, want %v/%v", tc.title, allowed, decided, tc.allowed, tc.decided)
		}
	}
}

// A rule that yolo can overrule is not a rule, and the approval mode is the
// setting people actually change.
func TestDenyHoldsAgainstEveryApprovalMode(t *testing.T) {
	goal := store.AgentGoal{ApprovalMode: "yolo", ToolPolicy: store.ACPToolPolicy{Deny: []string{"rm -rf"}}}
	if allowed, decided := namedToolDecision(goal.ToolPolicy, "rm -rf build"); decided && allowed {
		t.Error("yolo overruled a deny rule")
	}
	if !acpAllows("yolo", "execute") {
		t.Error("the mode itself changed; this test no longer proves the deny is what stopped it")
	}
}

// Deny and allow can both match the same title, and the answer must not depend on
// which list an operator happened to type it into first.
func TestDenyWinsOverAllow(t *testing.T) {
	policy := store.ACPToolPolicy{Deny: []string{"push"}, Allow: []string{"git"}}
	allowed, decided := namedToolDecision(policy, "git push origin main")
	if !decided || allowed {
		t.Errorf("allowed=%v decided=%v; deny must win", allowed, decided)
	}
}

// An agent that sends no title must not be silently governed by a policy that
// cannot see it. Matching an empty title against a substring rule would make
// every rule fire at once.
func TestAToolWithNoTitleIsLeftToTheMode(t *testing.T) {
	policy := store.ACPToolPolicy{Deny: []string{"rm"}, Allow: []string{"test"}}
	if _, decided := namedToolDecision(policy, "   "); decided {
		t.Error("an untitled tool call was decided by a name-matching policy")
	}
}

// A goal with no policy has to take exactly the path it took before this existed.
func TestNoPolicyChangesNothing(t *testing.T) {
	if _, decided := namedToolDecision(store.ACPToolPolicy{}, "anything at all"); decided {
		t.Error("an empty policy decided a request")
	}
}

// The wiring, not just the rule: a policy hit has to be consulted before the
// approval mode and before anybody is asked. The Orchestrator here has no store
// and no event sink, so a test that reaches either path panics rather than
// passing quietly.
func TestThePermissionPathConsultsTheToolPolicyFirst(t *testing.T) {
	o := &Orchestrator{}
	goal := store.AgentGoal{
		ApprovalMode: "yolo", ApprovalRequired: true,
		ToolPolicy: store.ACPToolPolicy{Deny: []string{"rm -rf"}, Allow: []string{"npm test"}},
	}
	run, agent := &store.AgentRun{}, store.Agent{}
	if allowed, by := o.answerPermission(context.Background(), run, agent, goal, "rt", permissionFor("t1", "rm -rf /workspace", "other"), "other"); allowed || by != "toolPolicy" {
		t.Errorf("deny: allowed=%v by=%q", allowed, by)
	}
	// approvalRequired would otherwise send this to a person, which needs a store.
	if allowed, by := o.answerPermission(context.Background(), run, agent, goal, "rt", permissionFor("t2", "Run `npm test`", "other"), "other"); !allowed || by != "toolPolicy" {
		t.Errorf("allow: allowed=%v by=%q", allowed, by)
	}
}

// A tool call reports its content again as it progresses. Appending each report
// would store the same screenshot several times and fill a run's artifact list
// with copies of one picture.
func TestAToolCallsPictureIsKeptOnceHoweverOftenItIsReported(t *testing.T) {
	turn := &acpTurn{}
	shot := acp.ContentBlock{Images: []acp.Image{{MimeType: "image/png", Data: "aGVsbG8="}}}
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t1", Title: "Take a screenshot", Kind: "other", Content: shot})
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "t1", Status: "in_progress", Content: shot})
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "t1", Status: "completed", Content: shot})
	turn.update(acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: "t2", Title: "Open the page", Kind: "other",
		Content: acp.ContentBlock{Images: []acp.Image{{MimeType: "image/png", Data: "d29ybGQ="}}}})

	pictures := turn.pictures()
	if len(pictures) != 2 {
		t.Fatalf("kept %d pictures: %#v", len(pictures), pictures)
	}
	if pictures[0].Title != "Take a screenshot" || pictures[1].Title != "Open the page" {
		t.Errorf("titles = %q, %q", pictures[0].Title, pictures[1].Title)
	}
}

// The file name says which step produced it, so a run with several screenshots
// is readable without opening each one.
func TestAPictureIsNamedAfterTheToolThatMadeIt(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Take a screenshot of /login", "01-take-a-screenshot-of-login.png"},
		{"", "01-screenshot.png"},
		{"페이지 열기", "01-screenshot.png"}, // nothing survives slugging; still named
	} {
		got := acpPictureName(acpPicture{Title: tc.title}, 0, ".png")
		if got != tc.want {
			t.Errorf("%q → %q, want %q", tc.title, got, tc.want)
		}
	}
}

// A run with zero tokens reads as free work. Usually it means the agent did the
// work in its own process and never said what it spent, and the two need
// different answers from whoever reads the usage report. Context occupancy is
// not spend: an agent that says how full its window is has said nothing about
// what was bought.
func TestARunSaysWhoCountedItsTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		turn *acpTurn
		want string
	}{
		{"agent reported spend", &acpTurn{totalTokens: 1200}, store.MeteringAgent},
		{"only context occupancy", &acpTurn{contextUsed: 9000, contextSize: 128000}, store.MeteringContextOnly},
		{"nothing at all", &acpTurn{}, store.MeteringUnmetered},
	} {
		if got := acpMetering(tc.turn); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.name, got, tc.want)
		}
	}
}
