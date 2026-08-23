package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Handing a task to a server somebody else runs.
//
// The other headless backends start something: a Pod, a process, a fabric. This
// one starts nothing. The machine is registered capacity — a development box, a
// machine inside the secure network, one with a GPU — reached over its own API,
// and what it offers is a whole software-engineering agent with its own sandbox
// and its own tools.
//
// What stays here is everything that makes it an enterprise platform rather than
// a launcher: whether this task was allowed to run at all, which model it may
// call, what the run costs, whether the goal was met. The server is given the
// work and the gateway to call; it is not given the decision.

// agentServerRun is one conversation on such a server.
type agentServerRun struct {
	ConversationID string
	Answer         string
	Status         string
	Tokens         int
	Actions        int
}

// runAgentServer hands the task to a registered server and keeps what it said.
func (o *Orchestrator) runAgentServer(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel) ([]string, Outcome) {
	server, outcome := o.placeOnAgentServer(ctx, goal)
	if outcome.Status != "" {
		return nil, outcome
	}

	step := workflow.Step{ID: "agentserver", AgentID: agent.ID, AgentName: agent.Name}
	prompt := runnerInput(task, goal)
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, prompt)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		prompt = scanned
	}

	ctx, span := telemetry.Start(ctx, "agentserver.run",
		attribute.String("agenthub.agentserver.id", server.ID),
		attribute.String("agenthub.agentserver.zone", server.NetworkZone))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "agentserver.started", "에이전트 서버에 작업을 맡깁니다.", map[string]any{
		"server": server.Name, "zone": server.NetworkZone, "baseUrl": server.BaseURL,
	})

	client := &agentServerClient{base: strings.TrimRight(server.BaseURL, "/"), http: &http.Client{Timeout: 60 * time.Second}}
	result, convErr := client.hold(ctx, runNotes{orchestrator: o, run: run, agent: agent, goal: goal}, goal, prompt, model)
	elapsed := time.Since(startedAt).Milliseconds()

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepAgentServer,
		Title: "에이전트 서버 실행", Input: prompt, Status: "succeeded", DurationMs: elapsed,
		Output: result.Answer,
	}
	run.StepCount = 1
	// Real usage, as the server's own metrics report it. Left unmetered rather
	// than shown as zero when the server said nothing about it: a run billed at
	// zero and a run whose cost is unknown are different facts.
	run.TotalTokens += result.Tokens
	if result.Tokens > 0 {
		run.Metering = store.MeteringAgent
	} else {
		run.Metering = store.MeteringUnmetered
	}
	if convErr != nil {
		record.Status, record.Error = "failed", convErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("agent server step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "agentserver.failed", convErr.Error(), map[string]any{
			"server": server.Name, "conversationId": result.ConversationID,
		})
		// A server that stopped answering is infrastructure, not the agent failing
		// its goal, so the task is worth another attempt — possibly on a different
		// machine, since placement runs again.
		return nil, Outcome{Status: store.TaskFailed, Failure: convErr.Error(), Retryable: agentServerRetryable(convErr)}
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("agent server step could not be recorded", "run", run.ID, "error", storeErr)
	}

	answer := result.Answer
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}
	o.event(ctx, *run, "agentserver.completed", "에이전트 서버 실행이 끝났습니다.", map[string]any{
		"durationMs": elapsed, "server": server.Name, "conversationId": result.ConversationID,
		"actions": result.Actions, "totalTokens": result.Tokens, "status": result.Status,
	})
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// placeOnAgentServer chooses which machine the work goes to.
//
// A goal may pin one, and then it is used or the task fails — a pin that quietly
// sends work elsewhere is worse than a refusal, because the pin usually exists
// for a reason somebody could not express in a field. Otherwise the choice is
// made among the servers that are enabled, healthy and in the zone the goal
// asked for.
func (o *Orchestrator) placeOnAgentServer(ctx context.Context, goal store.AgentGoal) (store.AgentServer, Outcome) {
	if goal.AgentServerID != "" {
		server, err := o.store.AgentServerByID(ctx, goal.AgentServerID)
		if err != nil {
			return store.AgentServer{}, Outcome{Status: store.TaskFailed, Retryable: true,
				Failure: "이 Goal이 지정한 에이전트 서버를 읽지 못했습니다: " + err.Error()}
		}
		if !server.Enabled {
			return store.AgentServer{}, Outcome{Status: store.TaskFailed,
				Failure: "이 Goal이 지정한 에이전트 서버(" + server.Name + ")가 꺼져 있습니다."}
		}
		return server, Outcome{}
	}
	servers, err := o.store.AgentServers(ctx)
	if err != nil {
		return store.AgentServer{}, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "에이전트 서버 목록을 읽지 못했습니다: " + err.Error()}
	}
	chosen, why := chooseAgentServer(servers, goal)
	if why != "" {
		// Retryable: a site that registers a server, or one whose machine comes
		// back, should see the queued task run rather than have to start it again.
		return store.AgentServer{}, Outcome{Status: store.TaskFailed, Retryable: true, Failure: why}
	}
	return chosen, Outcome{}
}

// chooseAgentServer picks among the servers a goal will accept, or says why it
// cannot. Separated from the store so the decision itself can be checked: its
// mistakes are quiet ones — work in the wrong network, or on the machine that
// failed its last check while a working one sat idle.
func chooseAgentServer(servers []store.AgentServer, goal store.AgentGoal) (store.AgentServer, string) {
	zone := strings.TrimSpace(goal.AgentServerZone)
	candidates := []store.AgentServer{}
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		if zone != "" && !strings.EqualFold(server.NetworkZone, zone) {
			continue
		}
		// Unknown health is not the same as unhealthy — a server nobody has checked
		// yet may work — but a server that failed its last check is not sent work.
		if server.Health == "unreachable" || server.Health == "refused" {
			continue
		}
		candidates = append(candidates, server)
	}
	if len(candidates) == 0 {
		return store.AgentServer{}, agentServerShortage(zone)
	}
	// A checked server before an unchecked one, so a deployment that registered a
	// spare does not send the first task to it.
	best := candidates[0]
	for _, server := range candidates[1:] {
		if best.Health != "healthy" && server.Health == "healthy" {
			best = server
		}
	}
	return best, ""
}

func agentServerShortage(zone string) string {
	if zone != "" {
		return "'" + zone + "' 구역에서 쓸 수 있는 에이전트 서버가 없습니다. 관리 > 에이전트 서버에서 등록하고 연결을 확인해 주세요."
	}
	return "쓸 수 있는 에이전트 서버가 없습니다. 관리 > 에이전트 서버에서 등록하고 연결을 확인해 주세요."
}

// agentServerClient speaks the server's API.
type agentServerClient struct {
	base string
	http *http.Client
}

// agentServerNotes is where the client says what it saw.
//
// The conversation itself is HTTP and nothing else, so it is kept separable from
// the run it belongs to: what a live server actually does can then be checked
// against the real thing rather than against a stand-in written from the same
// assumptions as the code.
type agentServerNotes interface {
	activity(ctx context.Context, conversationID string, actions int, tools []string)
	trouble(ctx context.Context, conversationID string, err error)
	// decide answers the server's approval gate. It is on this interface rather
	// than inside the client because the answer is a platform decision — a person,
	// a policy — and none of that belongs to HTTP.
	decide(ctx context.Context, action pendingAction) (bool, error)
}

// pendingAction is what the agent is waiting to be allowed to do.
type pendingAction struct {
	ConversationID string
	Tool           string
	// What it would do, rendered for whoever answers. The full action can be long
	// and can carry a secret, so this is a trimmed reading of it, the same way a
	// gated tool call is shown.
	Detail string
	// The server's own risk word for the action, kept as it wrote it.
	Risk string
}

// runNotes writes what the client saw onto a run.
type runNotes struct {
	orchestrator *Orchestrator
	run          *store.AgentRun
	agent        store.Agent
	goal         store.AgentGoal
}

func (n runNotes) activity(ctx context.Context, conversationID string, actions int, tools []string) {
	n.orchestrator.event(ctx, *n.run, "agentserver.activity", "에이전트 서버에서 한 일입니다.", map[string]any{
		"conversationId": conversationID, "actions": actions, "tools": tools,
	})
}

// decide asks whoever the Goal says should answer.
//
// The server has an approval gate of its own; this is the platform answering it,
// which is the only arrangement in which there is one authority rather than two.
func (n runNotes) decide(ctx context.Context, action pendingAction) (bool, error) {
	return n.orchestrator.askAboutServerAction(ctx, n.run, n.agent, n.goal, action)
}

func (n runNotes) trouble(ctx context.Context, conversationID string, err error) {
	n.orchestrator.logger.Warn("agent server call did not go through",
		"run", n.run.ID, "conversation", conversationID, "error", err)
}

func (c *agentServerClient) call(ctx context.Context, method, path string, body any, into any) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	answer, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("에이전트 서버에 연결하지 못했습니다: %w", err)
	}
	defer answer.Body.Close()
	if answer.StatusCode >= 300 {
		return fmt.Errorf("에이전트 서버가 %s 로 답했습니다 (%s)", answer.Status, path)
	}
	if into == nil {
		return nil
	}
	if err := json.NewDecoder(answer.Body).Decode(into); err != nil {
		return fmt.Errorf("에이전트 서버의 답을 읽지 못했습니다: %w", err)
	}
	return nil
}

// hold starts the conversation and waits for it.
//
// The model is named in the request rather than in the server's environment. The
// environment variable this platform would once have trusted is read by the
// server at startup, which is a machine somebody else configured — so the run
// would go wherever that machine was pointed. Passing it per conversation is what
// makes the gateway the only endpoint this work can reach.
func (c *agentServerClient) hold(ctx context.Context, notes agentServerNotes, goal store.AgentGoal, prompt string, model resolvedModel) (agentServerRun, error) {
	var result agentServerRun
	start := agentServerStart(goal, prompt, model)
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/conversations", start, &created); err != nil {
		return result, err
	}
	if created.ID == "" {
		return result, errors.New("에이전트 서버가 대화 ID를 돌려주지 않았습니다")
	}
	result.ConversationID = created.ID

	// Stop the conversation on the way out however this ends. A task that was
	// cancelled here and left running there would keep spending the site's model
	// budget with nobody watching it.
	defer func() {
		if result.Status == "finished" {
			return
		}
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := c.call(stop, http.MethodPost, "/api/conversations/"+created.ID+"/pause", nil, nil); err != nil {
			notes.trouble(stop, created.ID, err)
		}
	}()

	deadline := time.Now().Add(agentServerTimeout(goal))
	// Whether this platform has refused something. It changes what an idle
	// conversation means: the agent stops where it was refused and goes idle, so
	// without this the run would poll a finished conversation until its deadline
	// and report a timeout for a decision somebody made deliberately.
	refused := false
	for {
		if time.Now().After(deadline) {
			return result, errors.New("에이전트 서버가 제한 시간 안에 끝내지 못했습니다")
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		var info struct {
			ExecutionStatus string `json:"execution_status"`
			Stats           struct {
				UsageToMetrics map[string]struct {
					AccumulatedTokenUsage struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"accumulated_token_usage"`
				} `json:"usage_to_metrics"`
			} `json:"stats"`
		}
		if err := c.call(ctx, http.MethodGet, "/api/conversations/"+created.ID, nil, &info); err != nil {
			return result, err
		}
		result.Status = info.ExecutionStatus
		result.Tokens = 0
		for _, usage := range info.Stats.UsageToMetrics {
			result.Tokens += usage.AccumulatedTokenUsage.PromptTokens + usage.AccumulatedTokenUsage.CompletionTokens
		}
		switch info.ExecutionStatus {
		case "finished":
			var final struct {
				Response string `json:"response"`
			}
			if err := c.call(ctx, http.MethodGet, "/api/conversations/"+created.ID+"/agent_final_response", nil, &final); err != nil {
				return result, err
			}
			result.Answer = strings.TrimSpace(final.Response)
			result.Actions = c.recordEvents(ctx, notes, created.ID)
			if result.Answer == "" {
				return result, errors.New("에이전트 서버가 답을 남기지 않았습니다")
			}
			return result, nil
		case "error":
			result.Actions = c.recordEvents(ctx, notes, created.ID)
			return result, errors.New("에이전트 서버에서 실행이 실패했습니다")
		case "idle":
			if refused {
				result.Actions = c.recordEvents(ctx, notes, created.ID)
				return result, errors.New("승인되지 않은 작업이 있어 에이전트 서버 실행이 중단됐습니다")
			}
		case "waiting_for_confirmation":
			// The agent is holding its own question. Whoever the Goal says answers it,
			// and the answer goes back to the server; until then this loop waits, so
			// the run's own deadline is the time somebody has to decide.
			allowed, err := c.answerConfirmation(ctx, notes, created.ID)
			if err != nil {
				return result, err
			}
			if !allowed {
				refused = true
			}
		case "stuck":
			// The server's own word for an agent going in circles. Without this the
			// run would poll a conversation that has stopped making progress until
			// the deadline, and report a timeout for something the server already
			// diagnosed.
			result.Actions = c.recordEvents(ctx, notes, created.ID)
			return result, errors.New("에이전트 서버가 같은 일을 반복해 멈춰 있다고 판단했습니다")
		case "stopped", "paused":
			result.Actions = c.recordEvents(ctx, notes, created.ID)
			return result, errors.New("에이전트 서버에서 실행이 멈췄습니다: " + info.ExecutionStatus)
		}
	}
}

// answerConfirmation asks the platform about the action the agent is waiting on,
// and tells the server what was decided.
//
// The pending action is read from the server rather than guessed: what a person
// is being asked to allow has to be the thing that will actually run.
func (c *agentServerClient) answerConfirmation(ctx context.Context, notes agentServerNotes, conversationID string) (bool, error) {
	action, err := c.pendingAction(ctx, conversationID)
	if err != nil {
		return false, err
	}
	allowed, err := notes.decide(ctx, action)
	if err != nil {
		return false, err
	}
	answer := map[string]any{"accept": allowed}
	if !allowed {
		answer["reason"] = "이 배포의 승인 정책이 허용하지 않았습니다."
	}
	if err := c.call(ctx, http.MethodPost, "/api/conversations/"+conversationID+"/events/respond_to_confirmation", answer, nil); err != nil {
		return false, err
	}
	return allowed, nil
}

// pendingAction reads what the agent is waiting to be allowed to do.
func (c *agentServerClient) pendingAction(ctx context.Context, conversationID string) (pendingAction, error) {
	var found struct {
		Items []struct {
			Kind         string          `json:"kind"`
			ToolName     string          `json:"tool_name"`
			SecurityRisk string          `json:"security_risk"`
			Action       json.RawMessage `json:"action"`
		} `json:"items"`
	}
	// Newest first: the action being waited on is the last one the agent asked for.
	path := "/api/conversations/" + conversationID + "/events/search?limit=20&sort_order=TIMESTAMP_DESC"
	if err := c.call(ctx, http.MethodGet, path, nil, &found); err != nil {
		return pendingAction{}, err
	}
	for _, item := range found.Items {
		if item.Kind != "ActionEvent" {
			continue
		}
		return pendingAction{
			ConversationID: conversationID,
			Tool:           item.ToolName,
			Detail:         trimAction(string(item.Action)),
			Risk:           item.SecurityRisk,
		}, nil
	}
	// The server says it is waiting and does not say for what. Refusing is the
	// only safe reading: approving an action nobody can see is not approval.
	return pendingAction{}, errors.New("에이전트 서버가 무엇을 기다리는지 알려주지 않았습니다")
}

// trimAction renders an action for whoever answers. It is trimmed because the
// full action can be long and can carry a secret, the same reason a gated tool
// call is shown trimmed.
func trimAction(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > 400 {
		return raw[:400] + "…"
	}
	return raw
}

// recordEvents brings what the agent did onto this run's timeline.
//
// The server keeps its own record and this platform does not replace it. What it
// keeps here is what somebody reading the run needs: which tools were used, in
// what order — so a run on a machine this deployment does not own is still
// answerable to an auditor who only has this console. It returns how many actions
// the agent took, and is deliberately not fatal: a run that succeeded must not be
// reported as failed because its history could not be copied.
func (c *agentServerClient) recordEvents(ctx context.Context, notes agentServerNotes, conversationID string) int {
	actions, tools, truncated := 0, []string{}, false
	page := ""
	for read := 0; ; read++ {
		if read >= agentServerEventPages {
			truncated = true
			break
		}
		path := "/api/conversations/" + conversationID + "/events/search?limit=" + strconv.Itoa(agentServerEventPage)
		if page != "" {
			path += "&page_id=" + url.QueryEscape(page)
		}
		var found struct {
			Items []struct {
				Kind     string `json:"kind"`
				ToolName string `json:"tool_name"`
			} `json:"items"`
			NextPageID string `json:"next_page_id"`
		}
		if err := c.call(ctx, http.MethodGet, path, nil, &found); err != nil {
			notes.trouble(ctx, conversationID, err)
			break
		}
		for _, item := range found.Items {
			switch item.Kind {
			case "ActionEvent":
				actions++
			case "ObservationEvent":
				if item.ToolName != "" {
					tools = append(tools, item.ToolName)
				}
			}
		}
		if found.NextPageID == "" {
			break
		}
		page = found.NextPageID
	}
	if truncated {
		// Said rather than swallowed: a count that quietly stopped at a limit reads
		// as "this is everything the agent did", which it is not.
		tools = append(tools, "…(이후 생략)")
	}
	if actions > 0 || len(tools) > 0 {
		notes.activity(ctx, conversationID, actions, tools)
	}
	return actions
}

// How much of a conversation's history is copied here. The server keeps all of
// it and this platform does not replace that record; what it keeps is enough for
// somebody reading the run to see what the agent did. The page size is the
// server's own maximum — asking for more is a 500, which is how this number was
// found.
const (
	agentServerEventPage  = 100
	agentServerEventPages = 10
)

// agentServerStart is the request that begins a conversation.
//
// The model is named here rather than left to the server's environment. The
// variable this platform would once have trusted is read by the server when it
// starts, on a machine somebody else configured — so the run would go wherever
// that machine was pointed. Naming it per conversation is what makes this
// deployment's gateway the only endpoint the work can reach.
func agentServerStart(goal store.AgentGoal, prompt string, model resolvedModel) map[string]any {
	workingDir := strings.TrimSpace(goal.AgentServerDir)
	if workingDir == "" {
		workingDir = "workspace/project"
	}
	// Stop before each action when the Goal wants a person to answer. Left at the
	// server's own default otherwise, which is never to ask — a gate nobody
	// switched on must not start holding work.
	policy := "NeverConfirm"
	if goal.ApprovalRequired {
		policy = "AlwaysConfirm"
	}
	return map[string]any{
		"workspace":           map[string]any{"kind": "LocalWorkspace", "working_dir": workingDir},
		"confirmation_policy": map[string]any{"kind": policy},
		"initial_message": map[string]any{"role": "user",
			"content": []map[string]any{{"type": "text", "text": prompt}}},
		"max_iterations": agentServerIterations(goal),
		"agent": map[string]any{"kind": "Agent", "tools": agentServerTools(), "llm": map[string]any{
			"model": model.ModelName, "api_key": model.APIKey, "base_url": model.BaseURL,
			"usage_id": "agent",
		}},
	}
}

// agentServerTools is what the agent is allowed to do on that machine.
//
// Named rather than left to the server. An agent started without a tool list
// gets only "finish" and "think" — it can talk and stop, and a Goal handed to it
// would come back with an answer and no work done. And the other direction
// matters more: which tools exist on somebody else's machine is a platform
// decision, so the browser set is deliberately absent — a browser inside a
// sandbox this deployment does not run is a bigger surface than a shell, and it
// should be switched on deliberately rather than inherited.
func agentServerTools() []map[string]any {
	return []map[string]any{
		{"name": "terminal", "params": map[string]any{}},
		{"name": "file_editor", "params": map[string]any{}},
		{"name": "task_tracker", "params": map[string]any{}},
	}
}

// agentServerIterations bounds how many turns the agent may take there, from the
// same Goal field that bounds every other backend. A server left on its own
// default would run five hundred turns against this deployment's gateway.
func agentServerIterations(goal store.AgentGoal) int {
	if goal.MaxSteps > 0 {
		return goal.MaxSteps
	}
	return 10
}

func agentServerTimeout(goal store.AgentGoal) time.Duration {
	if goal.MaxDurationSeconds > 0 {
		return time.Duration(goal.MaxDurationSeconds) * time.Second
	}
	return 30 * time.Minute
}

// agentServerRetryable separates a machine that is having trouble from work that
// was refused. The first is worth another attempt, possibly on another server;
// the second would fail the same way every time.
func agentServerRetryable(err error) bool {
	text := err.Error()
	switch {
	case strings.Contains(text, "연결하지 못했습니다"), strings.Contains(text, "제한 시간"):
		return true
	case strings.Contains(text, "500"), strings.Contains(text, "502"), strings.Contains(text, "503"), strings.Contains(text, "504"):
		return true
	}
	return false
}

// askAboutServerAction is the platform answering the server's gate.
//
// The Goal decides who answers. When it asks for a person, the request is
// recorded as an approval like any other gated call — the same queue, the same
// notification, the same audit — and the conversation waits, bounded by the
// run's own deadline. A task nobody is watching must not hold a machine
// somewhere else open forever.
func (o *Orchestrator) askAboutServerAction(ctx context.Context, run *store.AgentRun, agent store.Agent, goal store.AgentGoal, action pendingAction) (bool, error) {
	if !goal.ApprovalRequired {
		// The gate was not asked for. It is on anyway only if somebody configured
		// the server that way, and the platform's answer is its approval mode.
		return acpAllows(approvalMode(goal), "execute"), nil
	}
	what := strings.TrimSpace(action.Tool)
	if what == "" {
		what = "도구"
	}
	pending, err := o.store.CreateToolApproval(ctx, store.ToolApproval{
		AgentID: agent.ID, OwnerID: agent.OwnerID,
		ServerName: agent.Name, ToolName: what,
		Arguments: action.Detail,
	}, "에이전트 서버에서 실행 중 승인을 요청했습니다: "+what)
	if err != nil {
		return false, err
	}
	o.event(ctx, *run, "agentserver.permission.asked", "사람의 승인을 기다립니다: "+what, map[string]any{
		"tool": what, "risk": action.Risk, "approvalId": pending.ApprovalID,
		"conversationId": action.ConversationID, "action": action.Detail,
	})

	ticker := time.NewTicker(agentServerApprovalPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, errors.New("승인을 기다리는 동안 실행 시간이 끝났습니다")
		case <-ticker.C:
			decided, statusErr := o.store.ToolApprovalDecision(ctx, pending.ID)
			if statusErr != nil {
				return false, statusErr
			}
			switch decided {
			case "approved":
				o.event(ctx, *run, "agentserver.permission.answered", "요청을 승인했습니다: "+what,
					map[string]any{"tool": what, "allowed": true, "approvalId": pending.ApprovalID})
				return true, nil
			case "rejected", "expired", "cancelled":
				o.event(ctx, *run, "agentserver.permission.answered", "요청을 승인하지 않았습니다: "+what,
					map[string]any{"tool": what, "allowed": false, "decision": decided, "approvalId": pending.ApprovalID})
				return false, nil
			}
		}
	}
}

// How often the platform looks for a decision. Often enough that a person who
// answers does not sit watching a spinner, rarely enough that a run waiting
// overnight is not a query every second.
const agentServerApprovalPoll = 3 * time.Second
