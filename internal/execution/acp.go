package execution

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/hkjang/AgentHub/internal/acp"
	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Running a task by talking to the agent instead of parsing what it printed.
//
// The CLI runner works, and it works by knowing one agent: its flags, the shape
// of its JSON, the exit codes it uses to say which budget stopped it. None of
// that knowledge transfers. The next agent names its budget flags differently,
// reports usage differently, and explains itself in its own words, so every
// agent added that way is another parser to write and another set of exit codes
// to keep true.
//
// The Agent Client Protocol is the industry's answer to that: a JSON-RPC
// conversation over the agent's stdio, with a session, streamed updates, and —
// the part that matters most here — a permission request the client answers
// before the agent touches anything. That last one is why this runner exists at
// all. Under the CLI runner an unattended task picks an approval mode up front
// and then the agent decides for itself; here the platform is asked, every time,
// and can answer according to the Goal and write down what it answered. A run
// that changed files ends with a record of which changes it was allowed to make.
//
// Token usage is metered when the agent reports it. The protocol has nowhere to
// put spend — its `usage_update` is context occupancy, how full the window is —
// so agents report the real numbers in their own extension field. When one does,
// the run is metered like any other work; when one does not, it is recorded as
// unmetered rather than credited with a number that is not one.

const (
	// acpStartupGrace is how long the agent has to answer `initialize`. An agent
	// that is not there, or that printed a stack trace and exited, must fail the
	// run quickly rather than hold a worker until the task's own deadline.
	acpStartupGrace = 60 * time.Second
	// acpApprovalPoll is how often a waiting turn asks whether somebody decided.
	// The agent is holding its question open, so this is a person's timescale
	// rather than a machine's.
	acpApprovalPoll = 2 * time.Second
	// acpTextLimit bounds what one turn may record, for the same reason the flow
	// runner bounds its response: an agent that streams its whole context back
	// would otherwise put it in the worker's memory and then in the run record.
	acpTextLimit = 200_000
)

// acpToolKinds a read-only session may use. They are the protocol's own kinds,
// and the list is the answer to "what cannot change anything".
var acpReadOnlyKinds = map[string]bool{
	"read": true, "search": true, "fetch": true, "think": true, "other": false,
}

// acpEditKinds additionally change the workspace but not the world outside it.
var acpEditKinds = map[string]bool{"edit": true, "move": true}

// runACP drives one task as a protocol conversation and returns the agent's
// answer as the transcript, which the evaluator then judges like any other.
func (o *Orchestrator) runACP(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "에이전트를 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	step := workflow.Step{ID: "acp", AgentID: agent.ID, AgentName: agent.Name}
	prompt := runnerInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, prompt)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		prompt = scanned
	}

	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime을 읽지 못했습니다: " + err.Error()}
	}
	descriptor := runtimetype.Describe(agent.RuntimeType)
	command := runtimetype.RunnerCommand(agent.RuntimeType, runtimetype.RunnerACP)
	if len(command) == 0 {
		// Not retryable: the runtime is simply not one that speaks the protocol,
		// and the answer is to change the Goal rather than to try again.
		return nil, Outcome{Status: store.TaskFailed,
			Failure: fmt.Sprintf("%s 런타임에는 ACP로 대화할 에이전트가 없습니다.", descriptor.Label)}
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	ctx, span := telemetry.Start(ctx, "acp.run",
		attribute.String("agenthub.runtime.id", acquired.runtimeID),
		attribute.String("agenthub.acp.approval_mode", approvalMode(goal)))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "acp.started", "런타임의 에이전트와 ACP로 연결합니다.", map[string]any{
		"runtimeId": acquired.runtimeID, "approvalMode": approvalMode(goal),
		"maxToolCalls": goal.MaxToolCalls,
	})

	turn, runErr := o.acpTurn(ctx, run, agent, goal, acquired, spec, descriptor, command, prompt)
	elapsed := time.Since(startedAt).Milliseconds()
	telemetry.Fail(span, runErr)

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepACP,
		Title: "ACP 실행", Input: prompt, Output: turn.answer(), Status: "succeeded", DurationMs: elapsed,
		PromptTokens: turn.inputTokens, CompletionTokens: turn.outputTokens,
	}
	run.StepCount = 1
	run.ToolCalls += turn.toolCalls
	// Metered only when the agent actually reported spend. An agent that reports
	// nothing leaves this at zero, and the run says so rather than implying the
	// work was free.
	run.TotalTokens += turn.totalTokens
	run.Metering = acpMetering(turn)
	if runErr != nil {
		record.Status, record.Error = "failed", runErr.Error()
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("acp step could not be recorded", "run", run.ID, "error", storeErr)
	}
	// Every tool the agent ran, and every permission answered on the operator's
	// behalf, goes on the run's timeline. This is the reason to prefer this runner:
	// under the CLI runner the same work happens and leaves no such record.
	o.recordACPTools(ctx, run, turn)
	o.saveACPPictures(ctx, run, task, agent, turn)

	if runErr != nil {
		o.event(ctx, *run, "acp.failed", runErr.Error(), map[string]any{
			"runtimeId": acquired.runtimeID, "toolCalls": turn.toolCalls, "denied": turn.denied,
		})
		return nil, Outcome{Status: store.TaskFailed, Failure: runErr.Error(), Retryable: turn.retryable}
	}

	answer := turn.answer()
	if strings.TrimSpace(answer) == "" {
		return nil, Outcome{Status: store.TaskFailed,
			Failure: "에이전트가 대화를 끝냈지만 답변이 비어 있습니다.", Retryable: true}
	}
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}

	o.event(ctx, *run, "acp.completed", "ACP 실행이 끝났습니다.", map[string]any{
		"durationMs": elapsed, "stopReason": turn.stopReason,
		"toolCalls": turn.toolCalls, "granted": turn.granted, "denied": turn.denied,
		"contextTokens": turn.contextUsed, "contextWindow": turn.contextSize,
		"totalTokens": turn.totalTokens,
		// Said plainly rather than left to be inferred from a zero: the protocol
		// itself carries no spend, so whether this run was metered depends on
		// whether the agent volunteered it.
		"metered": turn.totalTokens > 0,
	})
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// acpTurn is what one conversation produced.
type acpTurn struct {
	mu           sync.Mutex
	text         strings.Builder
	thoughts     int
	tools        []acpToolRecord
	toolCalls    int
	granted      int
	denied       int
	contextUsed  int
	contextSize  int
	inputTokens  int
	outputTokens int
	totalTokens  int
	stopReason   string
	retryable    bool
	// Pictures the agent produced, by the tool call that produced them. Keyed
	// rather than appended because a tool call reports its content again as it
	// progresses, and appending would store the same screenshot several times.
	images     map[string][]acp.Image
	imageOrder []string
	// What each tool call was asked to run, as text. The title is usually the
	// tool's name; the command is in here.
	inputs map[string]string
}

// acpToolRecord is one thing the agent did, and what the platform said about it.
type acpToolRecord struct {
	ID       string
	Title    string
	Kind     string
	Status   string
	Decision string
}

func (t *acpTurn) answer() string { return t.text.String() }

// acpTurn's updates arrive on the client's read loop while the caller waits on
// `session/prompt`, so everything they touch is guarded.
func (t *acpTurn) update(u acp.SessionUpdate) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Spend rides along with whatever the agent was saying at the time, so it is
	// read from every update rather than from one kind of them.
	t.inputTokens += u.Usage.InputTokens
	t.outputTokens += u.Usage.OutputTokens
	t.totalTokens += u.Usage.Total()
	switch u.SessionUpdate {
	case "agent_message_chunk":
		if t.text.Len() < acpTextLimit {
			t.text.WriteString(u.Content.Text)
		}
	case "agent_thought_chunk":
		// Counted, not kept: the agent's private reasoning is not the answer, and
		// storing it would put a model's scratch work in a durable record.
		t.thoughts++
	case "tool_call":
		t.toolCalls++
		t.tools = append(t.tools, acpToolRecord{ID: u.ToolCallID, Title: u.Title, Kind: u.Kind, Status: u.Status})
		t.keepInput(u)
		t.keepImages(u)
	case "tool_call_update":
		for index := range t.tools {
			if t.tools[index].ID == u.ToolCallID && u.Status != "" {
				t.tools[index].Status = u.Status
			}
		}
		t.keepInput(u)
		t.keepImages(u)
	case "usage_update":
		t.contextUsed, t.contextSize = u.Used, u.Size
	}
}

// acpInputLimit bounds what is remembered of one tool call's arguments. A rule
// is a short phrase; an agent that sends a megabyte of context does not make the
// rule more likely to match, only the memory larger.
const acpInputLimit = 8 * 1024

// keepInput remembers what a tool call was asked to run.
//
// An agent announces the call before it asks permission, and the permission
// request carries only an id, a title and a kind. The title is usually the
// tool's *name* — BrowserCode says `browser_execute`, Goose says
// `developer__shell` — while the command an operator actually wants to allow or
// refuse is in the arguments. A policy that could only read the title would
// never match a rule anybody would think to write.
//
// The caller already holds the lock.
func (t *acpTurn) keepInput(u acp.SessionUpdate) {
	if u.ToolCallID == "" || len(u.RawInput) == 0 {
		return
	}
	text := string(u.RawInput)
	if text == "{}" || text == "null" {
		// The announcement arrives before the arguments are filled in; keeping the
		// empty one would overwrite the real arguments that follow.
		return
	}
	if len(text) > acpInputLimit {
		text = text[:acpInputLimit]
	}
	if t.inputs == nil {
		t.inputs = map[string]string{}
	}
	t.inputs[u.ToolCallID] = text
}

// inputFor is what the platform judges by, alongside the title.
func (t *acpTurn) inputFor(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inputs[id]
}

// keepImages holds on to what a tool call produced as a picture. A browser agent
// says "the page shows an error" and attaches the page; keeping only the sentence
// keeps the claim and throws away the evidence for it.
//
// The caller already holds the lock.
func (t *acpTurn) keepImages(u acp.SessionUpdate) {
	if len(u.Content.Images) == 0 {
		return
	}
	if t.images == nil {
		t.images = map[string][]acp.Image{}
	}
	id := u.ToolCallID
	if id == "" {
		id = fmt.Sprintf("untitled-%d", len(t.imageOrder))
	}
	if _, seen := t.images[id]; !seen {
		t.imageOrder = append(t.imageOrder, id)
	}
	t.images[id] = u.Content.Images
}

// pictures is what the turn produced, in the order the calls happened, with the
// title of the call that made each one.
func (t *acpTurn) pictures() []acpPicture {
	t.mu.Lock()
	defer t.mu.Unlock()
	titles := map[string]string{}
	for _, record := range t.tools {
		titles[record.ID] = record.Title
	}
	out := []acpPicture{}
	for _, id := range t.imageOrder {
		for _, image := range t.images[id] {
			out = append(out, acpPicture{ToolCallID: id, Title: titles[id], Image: image})
		}
	}
	return out
}

type acpPicture struct {
	ToolCallID string
	Title      string
	Image      acp.Image
}

// decide records the answer against the tool call it belongs to. The kind is the
// one the platform judged by rather than the one on the request, so the run
// record and the timeline event say the same thing about the same call.
func (t *acpTurn) decide(request acp.PermissionRequest, kind string, allowed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	decision := "denied"
	if allowed {
		decision = "granted"
		t.granted++
	} else {
		t.denied++
	}
	for index := range t.tools {
		if t.tools[index].ID == request.ToolCall.ToolCallID {
			t.tools[index].Decision = decision
			return
		}
	}
	t.tools = append(t.tools, acpToolRecord{
		ID: request.ToolCall.ToolCallID, Title: request.ToolCall.Title,
		Kind: kind, Decision: decision,
	})
}

// kindFor is what the agent said this tool call was, if it said so before asking
// about it.
//
// It exists because a permission request does not always carry the kind the same
// agent already declared for the same call: BrowserCode announces `tool_call`
// with kind "edit" and then asks permission with kind "other". Answering the
// second one on its own would refuse a file edit under a mode that allows file
// edits, for no reason the operator could see. This is not guessing — it is the
// agent's own declaration about the same tool call id, made moments earlier.
func (t *acpTurn) kindFor(toolCallID string) string {
	if toolCallID == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, item := range t.tools {
		if item.ID == toolCallID && item.Kind != "" && item.Kind != "other" {
			return item.Kind
		}
	}
	return ""
}

// acpTurn's counters are read after the conversation ends, but the read loop may
// still be draining, so reading goes through the same lock.
func (t *acpTurn) records() []acpToolRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]acpToolRecord(nil), t.tools...)
}

// acpTurn runs the whole conversation: start the agent, negotiate, open a
// session, send the task, and stay on the line answering its questions.
func (o *Orchestrator) acpTurn(ctx context.Context, run *store.AgentRun, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime, spec appRuntime.Spec, descriptor runtimetype.Descriptor, command []string, prompt string) (*acpTurn, error) {
	turn := &acpTurn{retryable: true}
	session, err := o.spawner.ExecStream(ctx, spec, appRuntime.ExecRequest{Command: command})
	if err != nil {
		return turn, errors.New("Runtime에서 ACP 에이전트를 시작하지 못했습니다: " + err.Error())
	}
	defer session.Close()

	client := acp.New(session.Stdout, session.Stdin)
	client.Update = turn.update
	client.Permission = func(request acp.PermissionRequest) acp.PermissionOutcome {
		kind := acpKind(request, turn)
		allowed, by := o.answerPermission(ctx, run, agent, goal, acquired.runtimeID, request, kind, turn.inputFor(request.ToolCall.ToolCallID))
		turn.decide(request, kind, allowed)
		o.event(ctx, *run, "acp.permission", acpPermissionMessage(request, allowed), map[string]any{
			"tool": request.ToolCall.Title, "kind": kind,
			"decision": map[bool]string{true: "granted", false: "denied"}[allowed],
			"mode":     approvalMode(goal), "answeredBy": by,
		})
		if allowed {
			return acp.Allow(request.Options)
		}
		return acp.Deny(request.Options)
	}
	// The read loop lives as long as the conversation. Ending the context ends it,
	// and every call still waiting fails rather than hanging.
	loopCtx, endLoop := context.WithCancel(ctx)
	defer endLoop()
	go client.Run(loopCtx)

	handshake, cancelHandshake := context.WithTimeout(ctx, acpStartupGrace)
	defer cancelHandshake()
	capabilities, err := client.Initialize(handshake)
	if err != nil {
		return turn, fmt.Errorf("ACP 핸드셰이크가 실패했습니다: %s%s", err.Error(), acpStderrSuffix(session))
	}
	if capabilities.ProtocolVersion > acp.ProtocolVersion {
		// Not fatal: the protocol's rule is that the agent answers with a version it
		// can speak, and a newer agent that agreed to talk is still talking.
		o.logger.Info("acp agent reports a newer protocol", "run", run.ID, "version", capabilities.ProtocolVersion)
	}

	sessionID, err := acpOpenSession(ctx, client, capabilities, descriptor.Workspace)
	if err != nil {
		return turn, fmt.Errorf("ACP 세션을 열지 못했습니다: %s%s", acpAuthHint(capabilities, err), acpStderrSuffix(session))
	}

	// The Goal's tool budget is enforced here rather than handed to the agent,
	// because the protocol has no budget to hand it. Going over cancels the turn,
	// which the protocol defines as ending it cleanly.
	if goal.MaxToolCalls > 0 {
		stopWatching := o.watchACPBudget(ctx, client, sessionID, turn, goal.MaxToolCalls)
		defer stopWatching()
	}

	stopReason, err := client.Prompt(ctx, sessionID, prompt)
	turn.stopReason = stopReason
	if err != nil {
		return turn, fmt.Errorf("ACP 실행이 실패했습니다: %s%s", err.Error(), acpStderrSuffix(session))
	}
	if failure, retryable := acpStopFailure(stopReason, goal, turn); failure != nil {
		turn.retryable = retryable
		return turn, failure
	}
	turn.retryable = false
	return turn, nil
}

// acpOpenSession opens the session, authenticating first if the agent insists.
//
// Most agents here need no round trip: the runtime already has the model
// credentials in its environment, and an agent that can read them opens a session
// straight away. But the protocol allows an agent to refuse until the client has
// chosen one of the authentication methods it advertised, and a real one does
// exactly that when its credentials are missing. Trying the advertised method
// once turns "authentication required" into either a session or a message naming
// what to configure.
//
// No MCP servers are passed: the operator already wrote the agent's bound servers
// into its settings, pointed at the in-Pod policy gateway, and handing the same
// list to the session would give it two copies of every tool.
func acpOpenSession(ctx context.Context, client *acp.Client, capabilities acp.InitializeResult, workspace string) (string, error) {
	sessionID, err := client.NewSession(ctx, workspace, nil)
	if err == nil || len(capabilities.AuthMethods) == 0 {
		return sessionID, err
	}
	if authErr := client.Authenticate(ctx, capabilities.AuthMethods[0].ID); authErr != nil {
		// The original refusal is what a person needs: the agent said why it would
		// not start, and the failed retry only says the retry failed too.
		return "", err
	}
	return client.NewSession(ctx, workspace, nil)
}

// watchACPBudget cancels the turn once the Goal's tool budget is spent. It polls
// because the protocol has no budget of its own and the counter is raised by the
// read loop; the interval is short enough to stop within a tool call or two and
// long enough to cost nothing.
func (o *Orchestrator) watchACPBudget(ctx context.Context, client *acp.Client, sessionID string, turn *acpTurn, limit int) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				turn.mu.Lock()
				over := turn.toolCalls > limit
				turn.mu.Unlock()
				if over {
					client.Cancel(sessionID)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// acpStopFailure turns the protocol's stop reason into either nothing or a
// sentence a person can act on, and says whether trying again could differ.
func acpStopFailure(stopReason string, goal store.AgentGoal, turn *acpTurn) (error, bool) {
	switch stopReason {
	case "end_turn", "":
		return nil, false
	case "max_tokens":
		return errors.New("에이전트가 모델의 컨텍스트 한도에 도달해 중단했습니다. 작업을 나누거나 컨텍스트가 더 큰 모델을 지정해 주세요."), false
	case "max_turn_requests":
		return errors.New("에이전트가 한 턴에 허용된 모델 호출 수를 넘겨 중단했습니다."), false
	case "refusal":
		return errors.New("에이전트가 이 작업의 수행을 거부했습니다."), false
	case "cancelled":
		if goal.MaxToolCalls > 0 && turn.toolCalls > goal.MaxToolCalls {
			return fmt.Errorf("도구 호출이 Goal의 한도(%d)를 넘어 실행을 중단했습니다.", goal.MaxToolCalls), false
		}
		return errors.New("ACP 실행이 중단되었습니다."), true
	}
	return fmt.Errorf("에이전트가 알 수 없는 이유로 끝났습니다: %s", stopReason), false
}

// acpAllows is what the platform answers when nobody is at the keyboard.
//
// The approval mode on the Goal is the same one the CLI runner uses, because it
// is the same question and a second setting would only be a second place to get
// it wrong. What differs is who enforces it: there the agent is told the mode and
// trusted with it; here the platform answers each request itself, so the mode is
// a policy rather than a hint.
//
// "default" is deliberately the strict one. An unattended run has nobody to ask,
// and the honest reading of "ask before acting" with nobody there is no.
func acpAllows(mode, kind string) bool {
	switch mode {
	case "yolo", "auto":
		return true
	case "auto-edit":
		return acpReadOnlyKinds[kind] || acpEditKinds[kind]
	default: // "default", "plan", anything unrecognised
		return acpReadOnlyKinds[kind]
	}
}

// answerPermission decides who answers the agent, and returns the answer with
// who gave it.
//
// The mode alone was the whole policy until now, and it left one combination
// refused rather than resolved: a Goal that wanted a person to approve
// state-changing work could not also let the agent act, because the platform had
// no way to ask. It does have a way — the same one the MCP gateway uses to hold a
// tool call open while somebody decides — and an agent waiting on a JSON-RPC
// reply is exactly the shape that needs.
//
// So when the Goal requires approval, anything that is not read-only goes to a
// person. The approval mode still decides everything else, and still decides
// alone when nobody asked to be consulted.
func (o *Orchestrator) answerPermission(ctx context.Context, run *store.AgentRun, agent store.Agent, goal store.AgentGoal, runtimeID string, request acp.PermissionRequest, kind, arguments string) (bool, string) {
	// A named rule is read before anything else, because it was written about
	// this tool while the mode was written about tools in general.
	if named, decided := namedToolDecision(goal.ToolPolicy, request.ToolCall.Title, arguments); decided {
		return named, "toolPolicy"
	}
	asksPerson, allowed := permissionRoute(goal, kind)
	if !asksPerson {
		return allowed, "policy"
	}
	granted, err := o.askAboutTool(ctx, run, agent, runtimeID, request, kind)
	if err != nil {
		// The question could not be put to anybody. Refusing is the only safe
		// answer, and the reason goes on the run rather than into a log nobody
		// reads.
		o.event(ctx, *run, "acp.permission.unavailable", "승인을 요청하지 못해 거절했습니다: "+err.Error(),
			map[string]any{"tool": request.ToolCall.Title, "kind": kind})
		return false, "unavailable"
	}
	return granted, "person"
}

// namedToolDecision applies the goal's tool policy to one request's title, and
// says whether it decided at all.
//
// Deny wins over allow, and both win over the approval mode. That order is the
// only one that makes a "never" mean never: a rule an operator can overrule by
// changing an unrelated dropdown is not a rule, and the dropdown is the setting
// people actually change.
//
// Matching is a case-insensitive substring of the tool's title, because the title
// is prose the agent wrote — "Run `npm test` in /workspace", not a symbol. An
// exact match would silently never fire, which is the worst way for a security
// rule to fail: it looks configured.
func namedToolDecision(policy store.ACPToolPolicy, title, arguments string) (allowed, decided bool) {
	if policy.Empty() {
		return false, false
	}
	subject := strings.ToLower(strings.TrimSpace(title + " " + arguments))
	if strings.TrimSpace(subject) == "" {
		// Nothing to match against. The mode still gets to answer; refusing here
		// would turn any agent that omits titles into an agent that can do nothing.
		return false, false
	}
	if matchesAny(policy.Deny, subject) {
		return false, true
	}
	if matchesAny(policy.Allow, subject) {
		return true, true
	}
	return false, false
}

func matchesAny(patterns []string, subject string) bool {
	for _, pattern := range patterns {
		trimmed := strings.ToLower(strings.TrimSpace(pattern))
		if trimmed != "" && strings.Contains(subject, trimmed) {
			return true
		}
	}
	return false
}

// permissionRoute decides who answers one request: the platform from the Goal's
// approval mode, or a person.
//
// Reading is why the agent was started, so it is never escalated — a Goal that
// wakes somebody to approve a file read is a Goal nobody will leave switched on.
// Everything else goes to a person when the Goal asked for one, and to the mode
// when it did not.
func permissionRoute(goal store.AgentGoal, kind string) (asksPerson bool, allowed bool) {
	if acpReadOnlyKinds[kind] {
		return false, true
	}
	if !goal.ApprovalRequired {
		return false, acpAllows(approvalMode(goal), kind)
	}
	return true, false
}

// askAboutTool records the request as an approval and waits for a decision,
// leaving the agent holding its own question. It is bounded by the run's own
// deadline: a task with nobody watching must not hold a Pod open forever.
func (o *Orchestrator) askAboutTool(ctx context.Context, run *store.AgentRun, agent store.Agent, runtimeID string, request acp.PermissionRequest, kind string) (bool, error) {
	what := strings.TrimSpace(request.ToolCall.Title)
	if what == "" {
		what = kind
	}
	pending, err := o.store.CreateToolApproval(ctx, store.ToolApproval{
		RuntimeID: runtimeID, AgentID: agent.ID, OwnerID: agent.OwnerID,
		ServerName: agent.Name, ToolName: what,
		Arguments: "종류: " + kind,
	}, "에이전트가 실행 중 승인을 요청했습니다: "+what)
	if err != nil {
		return false, err
	}
	o.event(ctx, *run, "acp.permission.asked", "사람의 승인을 기다립니다: "+what, map[string]any{
		"tool": what, "kind": kind, "approvalId": pending.ApprovalID,
	})

	ticker := time.NewTicker(acpApprovalPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// The run's deadline, not a deadline of its own: whatever time the Goal
			// was given is the time somebody has to answer.
			return false, errors.New("승인을 기다리는 동안 실행 시간이 끝났습니다")
		case <-ticker.C:
			decided, statusErr := o.store.ToolApprovalStatus(ctx, pending.ID, runtimeID)
			if statusErr != nil {
				return false, statusErr
			}
			switch decided.Status {
			case "approved":
				return true, nil
			case "rejected", "expired", "cancelled":
				return false, nil
			}
		}
	}
}

// acpKind is what the platform judges by: the kind on the request, or the one
// the agent already declared for the same tool call when the request carries
// nothing useful.
func acpKind(request acp.PermissionRequest, turn *acpTurn) string {
	kind := request.ToolCall.Kind
	if kind != "" && kind != "other" {
		return kind
	}
	if declared := turn.kindFor(request.ToolCall.ToolCallID); declared != "" {
		return declared
	}
	return kind
}

func acpPermissionMessage(request acp.PermissionRequest, allowed bool) string {
	name := strings.TrimSpace(request.ToolCall.Title)
	if name == "" {
		name = request.ToolCall.Kind
	}
	if name == "" {
		name = "도구"
	}
	if allowed {
		return "에이전트의 요청을 승인했습니다: " + name
	}
	return "에이전트의 요청을 거절했습니다: " + name
}

// acpAuthHint explains a refusal the agent gave before it would open a session.
// An agent that needs credentials says so through `authMethods`, and repeating
// only the JSON-RPC error would leave a person guessing at the one thing they
// have to fix.
func acpAuthHint(capabilities acp.InitializeResult, err error) string {
	message := err.Error()
	if len(capabilities.AuthMethods) == 0 {
		return message
	}
	names := make([]string, 0, len(capabilities.AuthMethods))
	for _, method := range capabilities.AuthMethods {
		names = append(names, method.Name)
	}
	return fmt.Sprintf("%s (에이전트가 요구하는 인증: %s — Runtime의 모델 자격증명을 확인해 주세요)",
		message, strings.Join(names, ", "))
}

// acpStderrSuffix appends what the agent complained about. An agent that fails to
// start says why on stderr and nothing at all on the protocol stream, so without
// this the run would record a timeout and lose the reason.
func acpStderrSuffix(session *appRuntime.Session) string {
	text := strings.TrimSpace(session.Stderr())
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" || len(lines) == 1 {
		last = text
	}
	if len(last) > 400 {
		last = last[:400] + "…"
	}
	return " — " + last
}

// recordACPTools writes each tool call as its own step, after the turn's step, so
// a run reads in the order it happened.
func (o *Orchestrator) recordACPTools(ctx context.Context, run *store.AgentRun, turn *acpTurn) {
	for index, tool := range turn.records() {
		status := "succeeded"
		if tool.Decision == "denied" || tool.Status == "failed" {
			status = "failed"
		}
		title := tool.Title
		if strings.TrimSpace(title) == "" {
			title = tool.Kind
		}
		record := store.AgentRunStep{
			RunID: run.ID, Sequence: index + 2, Type: store.StepTool,
			Title: title, Output: acpToolOutcome(tool), Status: status,
		}
		if _, err := o.store.AppendRunStep(ctx, record); err != nil {
			o.logger.Error("acp tool step could not be recorded", "run", run.ID, "error", err)
			return
		}
		run.StepCount++
	}
}

func acpToolOutcome(tool acpToolRecord) string {
	parts := make([]string, 0, 3)
	if tool.Kind != "" {
		parts = append(parts, "종류: "+tool.Kind)
	}
	switch tool.Decision {
	case "granted":
		parts = append(parts, "승인됨")
	case "denied":
		parts = append(parts, "거절됨")
	}
	if tool.Status != "" {
		parts = append(parts, "상태: "+tool.Status)
	}
	return strings.Join(parts, " · ")
}

// acpMetering says who counted this run's tokens. The distinction the protocol
// forces is between spend and occupancy: `usage_update` says how full the context
// is, which is not what was bought, so a run that reported only that is recorded
// as knowing the shape of the work and not its cost.
func acpMetering(turn *acpTurn) string {
	switch {
	case turn.totalTokens > 0:
		return store.MeteringAgent
	case turn.contextSize > 0 || turn.contextUsed > 0:
		return store.MeteringContextOnly
	}
	return store.MeteringUnmetered
}

// How many pictures one run may keep, and what a single one may weigh. A browser
// agent takes a screenshot at every step, and a run that ran for an hour would
// otherwise put a hundred of them in the database.
const (
	acpPictureLimit = 12
	acpPictureBytes = 180 * 1024
)

// acpImageTypes is what may be stored. A screenshot is a raster image; an SVG is
// a document that can carry script, and storing one as a picture would put
// agent-authored markup where a person expects to look at a picture.
var acpImageTypes = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
}

// saveACPPictures stores what the agent showed. A failure to store one is
// reported on the run and never fails it: the work was still done, and losing the
// evidence is not a reason to throw away the result too.
func (o *Orchestrator) saveACPPictures(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, turn *acpTurn) {
	pictures := turn.pictures()
	if len(pictures) == 0 {
		return
	}
	kept, skipped := 0, 0
	for _, picture := range pictures {
		if kept >= acpPictureLimit {
			skipped += len(pictures) - kept
			break
		}
		extension, ok := acpImageTypes[strings.ToLower(strings.TrimSpace(picture.Image.MimeType))]
		if !ok {
			skipped++
			continue
		}
		// The wire carries base64 and the column holds text, so what is stored is
		// what arrived. Decoding here is only to know the real size and to refuse
		// something that is not an image at all.
		decoded, err := base64.StdEncoding.DecodeString(picture.Image.Data)
		if err != nil || len(decoded) == 0 || len(decoded) > acpPictureBytes {
			skipped++
			continue
		}
		saved, err := o.store.CreateArtifact(ctx, store.AgentArtifact{
			RunID: run.ID, TaskID: task.ID, AgentID: agent.ID, OwnerID: task.OwnerID,
			Name: acpPictureName(picture, kept, extension), Type: "image",
			ContentType: strings.ToLower(strings.TrimSpace(picture.Image.MimeType)),
			Content:     picture.Image.Data,
		})
		if err != nil {
			o.logger.Warn("acp picture could not be stored", "run", run.ID, "error", err)
			skipped++
			continue
		}
		kept++
		o.event(ctx, *run, "artifact.created", saved.Name, map[string]any{
			"artifactId": saved.ID, "type": saved.Type, "sizeBytes": saved.SizeBytes,
			"toolCallId": picture.ToolCallID,
		})
	}
	if skipped > 0 {
		// Said out loud rather than dropped quietly: a run whose screenshots are
		// missing should say they were, not look like a run that took none.
		o.event(ctx, *run, "acp.pictures.skipped",
			fmt.Sprintf("에이전트가 만든 이미지 %d개를 보관하지 않았습니다(한 실행당 %d개, 개당 %dKB 제한).", skipped, acpPictureLimit, acpPictureBytes/1024),
			map[string]any{"kept": kept, "skipped": skipped})
	}
}

// acpPictureName names the file after the tool call that produced it, so a run
// with several screenshots says which step each one is from.
func acpPictureName(picture acpPicture, index int, extension string) string {
	slug := strings.ToLower(strings.TrimSpace(picture.Title))
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_', r == '/', r == '.':
			return '-'
		}
		return -1
	}, slug)
	slug = strings.Trim(strings.ReplaceAll(slug, "--", "-"), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		slug = "screenshot"
	}
	return fmt.Sprintf("%02d-%s%s", index+1, slug, extension)
}
