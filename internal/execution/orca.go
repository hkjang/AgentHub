package execution

import (
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

// Handing a task to an execution fabric.
//
// Every other backend runs one agent. This one hands the work to a layer that
// coordinates several, each in its own git checkout, and keeps the task and
// dispatch state that says which did what. What AgentHub keeps is unchanged and
// is not negotiable: who may run this, against which model, under which policy,
// what it cost, what the trail says, and whether the task passed. The fabric
// owns coordination inside the task and nothing outside it.
//
// The provenance is the reason to prefer this over spawning agents ourselves.
// The fabric records which terminal created which task, in which worktree, on
// which branch, and refuses to pretend work was orchestrated when it was not —
// so a run's timeline can say where each piece of work happened rather than
// that some happened.

// orcaEnvelope is the CLI's answer. Every command returns this shape, so a
// failure is a code and a message rather than a parse error.
type orcaEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// orcaWorktree, orcaTerminal, orcaRun and orcaTask are the parts of the fabric's
// replies the platform stores. Fields it does not use are left unnamed: the CLI
// carries far more, and naming a field is a promise to keep reading it.
type orcaWorktree struct {
	Worktree struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		Branch string `json:"branch"`
	} `json:"worktree"`
}

type orcaTerminal struct {
	Terminal struct {
		Handle     string `json:"handle"`
		WorktreeID string `json:"worktreeId"`
	} `json:"terminal"`
}

type orcaRun struct {
	Run struct {
		ID string `json:"id"`
	} `json:"run"`
}

type orcaTask struct {
	Task struct {
		ID                      string `json:"id"`
		RunID                   string `json:"run_id"`
		CreatedByTerminalHandle string `json:"created_by_terminal_handle"`
		Status                  string `json:"status"`
	} `json:"task"`
}

// runOrca hands the task to the fabric in this runtime and records what it did.
func (o *Orchestrator) runOrca(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime) ([]string, Outcome) {
	if acquired == nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true,
			Failure: "실행 패브릭을 띄울 Runtime이 없습니다. Goal의 '작업 시 Runtime 시작'을 켜고 Kubernetes 연결을 확인해 주세요."}
	}
	base := runtimetype.RunnerCommand(agent.RuntimeType, runtimetype.RunnerOrca)
	if len(base) == 0 {
		return nil, Outcome{Status: store.TaskFailed,
			Failure: runtimetype.Describe(agent.RuntimeType).Label + " 런타임은 실행 패브릭이 아닙니다."}
	}
	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime을 읽지 못했습니다: " + err.Error()}
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		return nil, Outcome{Status: store.TaskFailed, Retryable: true, Failure: "Runtime 사양을 만들지 못했습니다: " + err.Error()}
	}

	prompt := runnerInput(task, goal)
	step := workflow.Step{ID: "orca", AgentID: agent.ID, AgentName: agent.Name}
	if o.flowInspector != nil {
		scanned, scanErr := o.flowInspector.Outbound(ctx, step, prompt)
		if scanErr != nil {
			return nil, Outcome{Status: store.TaskFailed, Failure: scanErr.Error(), Retryable: !errors.Is(scanErr, workflow.ErrBlocked)}
		}
		prompt = scanned
	}

	ctx, span := telemetry.Start(ctx, "orca.run", attribute.String("agenthub.runtime.id", acquired.runtimeID))
	defer span.End()
	startedAt := time.Now()
	o.event(ctx, *run, "orca.started", "실행 패브릭에 작업을 넘깁니다.", map[string]any{"runtimeId": acquired.runtimeID})

	fabric := &orcaSession{orchestrator: o, spec: spec, base: base, run: run, runtimeType: agent.RuntimeType}
	record := store.AgentRunStep{
		RunID: run.ID, Sequence: 1, Type: store.StepOrca,
		Title: "실행 패브릭", Input: prompt, Status: "succeeded",
	}
	run.StepCount = 1

	summary, fabricErr := fabric.dispatch(ctx, task, agent, prompt)
	record.DurationMs = time.Since(startedAt).Milliseconds()
	record.Output = summary
	if fabricErr != nil {
		record.Status, record.Error = "failed", fabricErr.Error()
		if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
			o.logger.Error("orca step could not be recorded", "run", run.ID, "error", storeErr)
		}
		o.event(ctx, *run, "orca.failed", fabricErr.Error(), map[string]any{"runtimeId": acquired.runtimeID})
		return nil, Outcome{Status: store.TaskFailed, Failure: fabricErr.Error(), Retryable: fabric.retryable}
	}
	if _, storeErr := o.store.AppendRunStep(ctx, record); storeErr != nil {
		o.logger.Error("orca step could not be recorded", "run", run.ID, "error", storeErr)
	}
	o.event(ctx, *run, "orca.completed", summary, map[string]any{
		"durationMs": record.DurationMs, "orcaRunId": fabric.runID, "orcaTaskId": fabric.taskID,
		"worktree": fabric.worktreePath, "branch": fabric.branch,
	})
	// The fabric does not report tokens: what it spends is spent by the agents it
	// starts, through their own runtimes. Saying zero would look free.
	run.Metering = store.MeteringUnmetered
	return []string{summary}, Outcome{}
}

// orcaSession is one task's conversation with the fabric.
type orcaSession struct {
	orchestrator *Orchestrator
	spec         appRuntime.Spec
	base         []string
	run          *store.AgentRun
	runtimeType  string

	runID        string
	taskID       string
	terminal     string
	worktreePath string
	branch       string
	retryable    bool
}

// call runs one orca command and reads its envelope.
//
// The CLI answers JSON for every command, including failures, so a refusal is a
// code and a sentence rather than an exit status somebody has to interpret. A
// command that does not answer JSON at all is a broken runtime, not a refusal,
// and is reported as one.
func (s *orcaSession) call(ctx context.Context, into any, args ...string) error {
	command := append(append([]string(nil), s.base...), args...)
	result, err := s.orchestrator.spawner.Exec(ctx, s.spec, appRuntime.ExecRequest{Command: command})
	if err != nil {
		s.retryable = true
		return fmt.Errorf("실행 패브릭에 명령을 보내지 못했습니다: %w", err)
	}
	return s.readEnvelope(result.Stdout, result.Stderr, into)
}

// readEnvelope turns one CLI answer into a result or an error.
//
// Kept apart from the call so the shapes the real fabric produces can be checked
// without a runtime: its refusals are the contract, and they are what the
// runner's order of operations exists to avoid.
func (s *orcaSession) readEnvelope(stdout, stderr string, into any) error {
	var envelope orcaEnvelope
	document := strings.TrimSpace(stdout)
	start := strings.Index(document, "{")
	if start < 0 {
		return errors.New("실행 패브릭이 응답하지 않았습니다: " + trimmed(stderr+stdout, 300))
	}
	if decodeErr := json.NewDecoder(strings.NewReader(document[start:])).Decode(&envelope); decodeErr != nil {
		return errors.New("실행 패브릭의 응답을 읽지 못했습니다: " + trimmed(document, 300))
	}
	if !envelope.OK {
		return fmt.Errorf("실행 패브릭이 거절했습니다(%s): %s", envelope.Error.Code, envelope.Error.Message)
	}
	if into != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, into); err != nil {
			return errors.New("실행 패브릭의 결과를 해석하지 못했습니다: " + err.Error())
		}
	}
	return nil
}

// dispatch creates the checkout, the terminal, the Run and the Task, and records
// each as it happens.
//
// Order matters and is the fabric's, not ours: an orchestration command needs a
// sender terminal — without one it answers `no_active_sender_terminal` — and a
// Run must be bound before tasks exist, or `task-list` answers `run_required`
// rather than an empty list. Those two refusals are why this is a sequence and
// not four independent calls.
func (s *orcaSession) dispatch(ctx context.Context, task store.AgentTask, agent store.Agent, prompt string) (string, error) {
	// The runtime's own workspace is what the fabric checks out from. The
	// descriptor names it because every runtime mounts it in the same place.
	workspace := strings.TrimSpace(runtimetype.Describe(s.runtimeType).Workspace)
	if workspace == "" {
		workspace = "/workspace"
	}
	var repo struct {
		Repo struct {
			ID string `json:"id"`
		} `json:"repo"`
	}
	if err := s.call(ctx, &repo, "repo", "add", "--path", workspace, "--json"); err != nil {
		return "", err
	}

	name := orcaWorkspaceName(task)
	var worktree orcaWorktree
	if err := s.call(ctx, &worktree, "worktree", "create", "--repo", "id:"+repo.Repo.ID, "--name", name, "--json"); err != nil {
		return "", err
	}
	s.worktreePath, s.branch = worktree.Worktree.Path, worktree.Worktree.Branch

	var terminal orcaTerminal
	if err := s.call(ctx, &terminal, "terminal", "create", "--worktree", "worktree:"+worktree.Worktree.ID,
		"--command", "bash -lc 'echo agenthub-coordinator; sleep 86400'", "--json"); err != nil {
		return "", err
	}
	s.terminal = terminal.Terminal.Handle

	var created orcaRun
	if err := s.call(ctx, &created, "orchestration", "run-create", "--from", s.terminal,
		"--objective", trimmed(task.Title, 200), "--json"); err != nil {
		return "", err
	}
	s.runID = created.Run.ID

	var fabricTask orcaTask
	if err := s.call(ctx, &fabricTask, "orchestration", "task-create", "--from", s.terminal,
		"--task-title", trimmed(task.Title, 200), "--spec", prompt, "--json"); err != nil {
		return "", err
	}
	s.taskID = fabricTask.Task.ID

	if err := s.orchestrator.store.SaveOrcaDispatch(ctx, store.OrcaDispatch{
		RunID: s.run.ID, OrcaRunID: s.runID, OrcaTaskID: s.taskID,
		Terminal: s.terminal, Worktree: s.worktreePath, Branch: s.branch,
		Role: agent.Name, Status: "dispatched",
	}); err != nil {
		s.orchestrator.logger.Error("an orca dispatch could not be recorded", "run", s.run.ID, "error", err)
	}
	return fmt.Sprintf("실행 패브릭에 작업을 만들었습니다 — Run %s, Task %s, 작업 사본 %s (%s)",
		s.runID, s.taskID, s.worktreePath, s.branch), nil
}

// orcaWorkspaceName is the checkout's name in the fabric.
//
// It carries the task's own id rather than its title: two tasks with the same
// title must not land in the same checkout, and a title is a person's sentence
// which may contain anything at all.
func orcaWorkspaceName(task store.AgentTask) string {
	id := strings.ReplaceAll(task.ID, "-", "")
	if len(id) > 12 {
		id = id[:12]
	}
	return "agenthub-" + id
}
