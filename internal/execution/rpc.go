package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Speaking to an agent while it works.
//
// The other headless backend starts a command and waits for it. This one keeps a
// process open and holds a conversation: JSON lines in, JSON lines out, for as
// long as the work takes. What that buys is that the work can be spoken to —
// redirected, asked a follow-up, interrupted, asked what it is doing — instead of
// only started and read afterwards.
//
// The backend is named for the shape rather than for the agent that prompted it.
// Commands acknowledged and then a stream of events is what several agents offer,
// and a backend named after one of them would be copied for the next.

// rpcEvent is one line from the agent. Only what the platform stores is named:
// an agent that adds a field must not break a run, and one that removes a field
// this relies on has to fail where somebody can read it.
type rpcEvent struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage      rpcUsage `json:"usage"`
		StopReason string   `json:"stopReason"`
	} `json:"message"`
	Usage       rpcUsage `json:"usage"`
	WillRetry   bool     `json:"willRetry"`
	ToolResults []struct {
		Name string `json:"name"`
	} `json:"toolResults"`
	Data struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			BaseURL  string `json:"baseUrl"`
		} `json:"model"`
		SessionID    string `json:"sessionId"`
		MessageCount int    `json:"messageCount"`
	} `json:"data"`
}

type rpcUsage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cacheRead"`
	CacheWrite  int `json:"cacheWrite"`
	TotalTokens int `json:"totalTokens"`
}

// rpcResult is what one conversation produced.
type rpcResult struct {
	Answer     string
	Tokens     int
	Turns      int
	ToolCalls  int
	SessionID  string
	Provider   string
	BaseURL    string
	StopReason string
}

// runRPC hands the task to a long-lived agent process and keeps what it said.
func (o *Orchestrator) runRPC(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "에이전트를 실행할 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	command := runtimetype.RunnerCommand(agent.RuntimeType, runtimetype.RunnerRPC)
	if len(command) == 0 {
		return nil, Outcome{Status: store.TaskFailed,
			Failure: runtimetype.Describe(agent.RuntimeType).Label + " 런타임은 프로토콜 실행을 지원하지 않습니다."}
	}
	step := workflow.Step{ID: "rpc", AgentID: agent.ID, AgentName: agent.Name}
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
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	// The provider and model name this deployment's gateway. They are passed
	// rather than left to the agent's own default, which is a vendor.
	command = append(append([]string(nil), command...), "--provider", rpcProvider, "--model", model.ModelName)
	// Project-local settings and skills are the platform's decision, not the
	// agent's: a repository that can turn on its own extensions is a repository
	// that can run whatever it likes inside this Pod.
	command = append(command, "--no-approve")

	ctx, span := telemetry.Start(ctx, "rpc.run",
		attribute.String("agenthub.runtime.id", acquired.runtimeID),
		attribute.String("agenthub.rpc.model", model.ModelName))
	defer span.End()

	startedAt := time.Now()
	o.event(ctx, *run, "rpc.started", "에이전트와 프로토콜로 연결합니다.", map[string]any{
		"runtimeId": acquired.runtimeID, "model": model.ModelName,
	})
	session, err := o.spawner.ExecStream(ctx, spec, appRuntime.ExecRequest{Command: command})
	if err != nil {
		o.event(ctx, *run, "rpc.failed", err.Error(), map[string]any{"runtimeId": acquired.runtimeID})
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "Runtime에서 에이전트를 시작하지 못했습니다: " + err.Error()}
	}
	defer session.Close()

	result, convErr := o.speakRPC(ctx, run, session, prompt, goal)
	elapsed := time.Since(startedAt).Milliseconds()

	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepRPC,
		Title: "에이전트 실행", Input: prompt, Status: "succeeded", DurationMs: elapsed,
		Output: result.Answer,
	}
	run.StepCount = 1
	// Real usage, reported per message by the agent itself, so this is metered
	// like any other work — and left unmetered rather than shown as zero when the
	// agent said nothing about it.
	run.TotalTokens += result.Tokens
	if result.Tokens > 0 {
		run.Metering = store.MeteringAgent
	} else {
		run.Metering = store.MeteringUnmetered
	}
	if convErr != nil {
		record.Status, record.Error = "failed", convErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("rpc step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "rpc.failed", convErr.Error(), map[string]any{
			"runtimeId": acquired.runtimeID, "stderr": trimmed(session.Stderr(), 400),
		})
		return nil, Outcome{Status: store.TaskFailed, Failure: convErr.Error(), Retryable: rpcRetryable(convErr)}
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("rpc step could not be recorded", "run", run.ID, "error", storeErr)
	}

	// Where the agent was actually pointed, asked of the agent rather than
	// assumed from what it was configured with. The environment variable this
	// platform would once have trusted turns out to mean nothing to these agents,
	// so the endpoint is checked rather than believed.
	if result.BaseURL != "" && !sameEndpoint(result.BaseURL, model.BaseURL) {
		o.logger.Error("an agent was pointed somewhere other than this deployment's gateway",
			"run", run.ID, "reported", result.BaseURL, "expected", model.BaseURL)
		o.event(ctx, *run, "rpc.endpoint_mismatch", "에이전트가 이 배포의 게이트웨이가 아닌 곳을 보고 있습니다.", map[string]any{
			"reported": result.BaseURL, "expected": model.BaseURL,
		})
	}

	answer := result.Answer
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Inbound(ctx, step, answer)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		answer = scanned
	}
	o.event(ctx, *run, "rpc.completed", "에이전트 실행이 끝났습니다.", map[string]any{
		"durationMs": elapsed, "turns": result.Turns, "toolCalls": result.ToolCalls,
		"totalTokens": result.Tokens, "sessionId": result.SessionID, "stopReason": result.StopReason,
	})
	o.saveMemory(ctx, *run, task, answer)
	return []string{answer}, Outcome{}
}

// rpcProvider is the name the runtime's configuration gives this deployment's
// gateway. It is a constant because two spellings would mean an agent silently
// falling back to a vendor's default provider.
const rpcProvider = "agenthub"

// speakRPC holds the conversation.
//
// The protocol is a command, an acknowledgement, then events until the agent
// says it has settled. `agent_end` is not that: it carries `willRetry`, so a turn
// ending is not the work ending, and a runner that stopped there would cut off a
// retry the agent was about to make.
func (o *Orchestrator) speakRPC(ctx context.Context, run *store.AgentRun, session *appRuntime.Session, prompt string, goal store.AgentGoal) (rpcResult, error) {
	var result rpcResult
	command, err := json.Marshal(map[string]any{"type": "prompt", "message": prompt})
	if err != nil {
		return result, err
	}
	if _, err := session.Stdin.Write(append(command, '\n')); err != nil {
		return result, fmt.Errorf("에이전트에 작업을 전달하지 못했습니다: %w", err)
	}

	// What somebody says to this run while it is going, delivered between events.
	//
	// A person at a browser cannot reach a conversation held by a worker process,
	// so the platform carries it: the API records what they said and this puts it
	// into the conversation. Between events rather than at any moment, because a
	// line written into the middle of another would corrupt the conversation it
	// is meant to steer.
	directives, stopDirectives := context.WithCancel(ctx)
	defer stopDirectives()
	go o.deliverDirectives(directives, run, session)

	deadline := time.Now().Add(rpcTimeout(goal))
	lines := bufio.NewScanner(session.Stdout)
	lines.Buffer(make([]byte, 0, 64*1024), rpcMaxLine)
	accepted := false
	for lines.Scan() {
		if time.Now().After(deadline) {
			return result, errors.New("에이전트가 제한 시간 안에 끝내지 못했습니다")
		}
		var event rpcEvent
		if err := json.Unmarshal(lines.Bytes(), &event); err != nil {
			// A line that is not an event is the agent's own noise, not a failure.
			continue
		}
		switch event.Type {
		case "response":
			if event.Command == "prompt" {
				if event.Success != nil && !*event.Success {
					return result, errors.New("에이전트가 작업을 받아들이지 않았습니다: " + trimmed(event.Error, 200))
				}
				accepted = true
			}
			if event.Command == "steer" || event.Command == "follow_up" {
				// The agent answering is what makes a directive delivered. A
				// refusal recorded as a delivery would tell the person their words
				// landed when the agent turned them down.
				if event.Success != nil && !*event.Success {
					o.logger.Warn("an agent refused a directive", "run", run.ID, "kind", event.Command, "error", event.Error)
					o.event(ctx, *run, "rpc.directive_refused", "에이전트가 지시를 받아들이지 않았습니다: "+trimmed(event.Error, 160), map[string]any{
						"kind": event.Command,
					})
				}
			}
			if event.Command == "get_state" {
				result.SessionID = event.Data.SessionID
				result.Provider, result.BaseURL = event.Data.Model.Provider, event.Data.Model.BaseURL
			}
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
			// Settled is the end. Ask where it was pointed before letting go.
			if _, err := session.Stdin.Write([]byte(`{"type":"get_state"}` + "\n")); err == nil {
				o.readOneRPCResponse(lines, &result)
			}
			_ = session.Stdin.Close()
			if result.Answer == "" {
				return result, errors.New("에이전트가 아무 답도 남기지 않고 끝났습니다: " + trimmed(session.Stderr(), 200))
			}
			return result, nil
		}
	}
	if err := lines.Err(); err != nil {
		return result, fmt.Errorf("에이전트의 출력을 읽지 못했습니다: %w", err)
	}
	if !accepted {
		return result, errors.New("에이전트가 시작하지 못했습니다: " + trimmed(session.Stderr(), 300))
	}
	// The stream ended without the agent saying it had settled, which is the
	// process dying rather than the work finishing.
	return result, errors.New("에이전트가 끝났다고 알리기 전에 연결이 끊겼습니다: " + trimmed(session.Stderr(), 200))
}

// deliverDirectives says what people have asked to say, until the run ends.
//
// It writes rather than reads: the conversation's answers come back on the same
// stream everything else does, so a directive's acknowledgement is read by the
// loop that reads everything, and this half only has to put the line in.
//
// The interval is a compromise nobody escapes: a person redirecting an agent
// wants it heard now, and a query per second per running task is a cost the
// database pays for every task whether or not anybody is watching. Two seconds
// is close enough to now for somebody typing, and cheap enough to leave on.
func (o *Orchestrator) deliverDirectives(ctx context.Context, run *store.AgentRun, session *appRuntime.Session) {
	ticker := time.NewTicker(rpcDirectiveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pending, err := o.store.TakeRunDirectives(ctx, run.ID)
			if err != nil {
				// An unreadable database is not a reason to end a run. The
				// directive stays unclaimed and the next tick tries again.
				o.logger.Warn("directives for a running agent could not be read", "run", run.ID, "error", err)
				continue
			}
			for _, directive := range pending {
				line, marshalErr := json.Marshal(map[string]any{"type": directive.Kind, "message": directive.Message})
				if marshalErr != nil {
					continue
				}
				if _, writeErr := session.Stdin.Write(append(line, '\n')); writeErr != nil {
					// The conversation is gone. Saying so on the directive is what
					// tells the person their words never arrived — the delivered
					// timestamp alone would claim they did.
					_ = o.store.RecordDirectiveOutcome(context.WithoutCancel(ctx), directive.ID, "전달하지 못했습니다: "+writeErr.Error())
					return
				}
				o.event(ctx, *run, "rpc.directive", directiveNote(directive), map[string]any{
					"kind": directive.Kind, "directiveId": directive.ID,
				})
			}
		}
	}
}

// rpcDirectiveInterval is how long somebody's words may wait.
const rpcDirectiveInterval = 2 * time.Second

// directiveNote is what the run's timeline says about one.
func directiveNote(directive store.RunDirective) string {
	if directive.Kind == "follow_up" {
		return "이어서 할 일을 전달했습니다: " + trimmed(directive.Message, 120)
	}
	return "진행 방향을 바꿔 전달했습니다: " + trimmed(directive.Message, 120)
}

// readOneRPCResponse reads until the state answer, so the endpoint check does not
// depend on it being the very next line.
func (o *Orchestrator) readOneRPCResponse(lines *bufio.Scanner, result *rpcResult) {
	for i := 0; i < rpcStateLookahead && lines.Scan(); i++ {
		var event rpcEvent
		if err := json.Unmarshal(lines.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "response" && event.Command == "get_state" {
			result.SessionID = event.Data.SessionID
			result.Provider, result.BaseURL = event.Data.Model.Provider, event.Data.Model.BaseURL
			return
		}
	}
}

const (
	// rpcMaxLine bounds one event. A message with a large file in it is still one
	// line, and the default scanner limit would end the conversation mid-run.
	rpcMaxLine = 8 << 20
	// rpcStateLookahead is how many lines the state answer may be behind.
	rpcStateLookahead = 20
)

// rpcTimeout is the Goal's own limit, so a bound set in the console is the bound
// that applies rather than one written here.
func rpcTimeout(goal store.AgentGoal) time.Duration {
	if goal.MaxDurationSeconds > 0 {
		return time.Duration(goal.MaxDurationSeconds) * time.Second
	}
	return 30 * time.Minute
}

// rpcText is the assistant's words in one event.
func rpcText(event rpcEvent) string {
	parts := []string{}
	for _, item := range event.Message.Content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// rpcRetryable distinguishes the agent failing its task from the platform
// failing to hold the conversation.
func rpcRetryable(err error) bool {
	text := err.Error()
	return strings.Contains(text, "연결이 끊겼습니다") || strings.Contains(text, "출력을 읽지 못했습니다") ||
		strings.Contains(text, "전달하지 못했습니다")
}

// sameEndpoint compares two endpoints the way a person would, so a trailing
// slash does not raise a false alarm about an agent talking to a vendor.
func sameEndpoint(reported, expected string) bool {
	trim := func(value string) string { return strings.TrimRight(strings.TrimSpace(value), "/") }
	return trim(reported) == trim(expected)
}
