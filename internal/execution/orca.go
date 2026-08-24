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

	summary, fabricErr := fabric.dispatch(ctx, task, goal, prompt)
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
	worktreeName string
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
	return s.readEnvelope(result.Stdout, result.Stderr, result.ExitCode, into)
}

// readEnvelope turns one CLI answer into a result or an error.
//
// Kept apart from the call so the shapes the real fabric produces can be checked
// without a runtime: its refusals are the contract, and they are what the
// runner's order of operations exists to avoid.
func (s *orcaSession) readEnvelope(stdout, stderr string, exitCode int, into any) error {
	var envelope orcaEnvelope
	document := strings.TrimSpace(stdout)
	start := strings.Index(document, "{")
	if start < 0 {
		// A fabric that printed nothing because its container was stopped is not
		// a fabric that answered badly. The other three in-Pod backends learned
		// this two releases ago; this is the fourth.
		if killed := killedContainer(exitCode); killed != "" {
			s.retryable = true
			return errors.New("실행 패브릭이 " + killed + trimmedSuffix(stderr+stdout))
		}
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
func (s *orcaSession) dispatch(ctx context.Context, task store.AgentTask, goal store.AgentGoal, prompt string) (string, error) {
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
	s.worktreeName = name
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

	s.record(ctx, store.OrcaDispatch{
		RunID: s.run.ID, OrcaRunID: s.runID, OrcaTaskID: s.taskID,
		Terminal: s.terminal, Worktree: s.worktreePath, Branch: s.branch,
		Role: "coordinator", Status: "bound",
	})

	workers, workerErr := s.fanOut(ctx, goal)
	head := fmt.Sprintf("실행 패브릭에 작업을 만들었습니다 — Run %s, Task %s, 작업 사본 %s (%s)",
		s.runID, s.taskID, s.worktreePath, s.branch)
	if workerErr != nil {
		return head, workerErr
	}
	if len(workers) == 0 {
		return head + ". 워커는 아직 없습니다 — Goal에 에이전트를 지정하거나 런타임 터미널에서 직접 붙이세요.", nil
	}
	outcomes, waitErr := s.waitForWorkers(ctx, workers, goal)
	report := fmt.Sprintf("%s. 워커 %d개를 각자의 작업 사본에 붙였습니다", head, len(workers))
	if len(outcomes) > 0 {
		report += " — " + strings.Join(outcomes, " · ")
	}
	if waitErr != nil {
		return report, waitErr
	}
	// Every worker having failed is not a finished task. The run said completed
	// while its only worker read `failed — agent_prompt_stalled`, which is the
	// dispatch reported as the work all over again, one layer further in.
	if failed := orcaAllFailed(outcomes); failed {
		return report, errors.New("워커가 모두 실패했습니다 — " + strings.Join(outcomes, " · "))
	}
	return report + ".", nil
}

// orcaSuccess are the states that mean a worker did the work. Anything else —
// including a word this platform has not seen — is not success: a run that
// passes on an unfamiliar state is a run that passes on anything.
var orcaSuccess = map[string]bool{
	"completed": true, "succeeded": true, "success": true, "done": true, "finished": true,
}

// orcaAllFailed reports whether not one worker got there.
func orcaAllFailed(outcomes []string) bool {
	if len(outcomes) == 0 {
		return false
	}
	for _, line := range outcomes {
		state := line
		if at := strings.Index(state, ": "); at >= 0 {
			state = state[at+2:]
		}
		if at := strings.Index(state, " —"); at >= 0 {
			state = state[:at]
		}
		if orcaSuccess[strings.ToLower(strings.TrimSpace(state))] {
			return false
		}
	}
	return true
}

// orcaWorkerPoll is how often the fabric is asked what its workers are doing.
const orcaWorkerPoll = 5 * time.Second

// orcaWorkerLimit is how long the run waits for them. The Goal's own time limit
// when it has one — the work belongs to the task, not to this backend — and an
// hour when it does not, because waiting for ever is how a worker slot is lost.
func orcaWorkerLimit(goal store.AgentGoal) time.Duration {
	if goal.MaxDurationSeconds > 0 {
		return time.Duration(goal.MaxDurationSeconds) * time.Second
	}
	return time.Hour
}

// fanOut starts one worker per agent the Goal names, each in its own checkout.
//
// This is what the fabric is for: the same task, worked several ways at once,
// compared afterwards. A Goal that names nobody is not an error — the task and
// the checkout are still recorded and a person can attach workers from the
// runtime's own terminal — because an agent needs an account on the host that
// this platform cannot create.
func (s *orcaSession) fanOut(ctx context.Context, goal store.AgentGoal) ([]orcaWorkerRef, error) {
	started := []orcaWorkerRef{}
	for _, name := range OrcaAgentNames(goal.OrcaAgents) {
		var worker struct {
			Dispatch struct {
				ID string `json:"id"`
			} `json:"dispatch"`
			Worktree struct {
				Path   string `json:"path"`
				Branch string `json:"branch"`
			} `json:"worktree"`
		}
		// The sender must be the coordinator terminal bound to this Task's Run —
		// any other handle is answered `consumer_fenced` — which is why the
		// session keeps the handle it created rather than looking one up.
		err := s.call(ctx, &worker, "orchestration", "worker-start",
			"--from", s.terminal, "--task", s.taskID, "--agent", name,
			"--worktree", "new-child", "--name", s.workerName(name), "--json")
		if err != nil {
			s.record(ctx, store.OrcaDispatch{
				RunID: s.run.ID, OrcaRunID: s.runID, OrcaTaskID: s.taskID,
				Role: name, Status: "refused", Detail: trimmed(err.Error(), 400),
			})
			return started, orcaWorkerFailure(name, err)
		}
		// `ok` means the fabric accepted the dispatch, not that the worker is
		// running: a worker whose agent is not on the host dies in its own
		// terminal seconds later. What became of it is read from the fabric's
		// worker listing while the run waits, because the answer to worker-start
		// does not carry a dispatch id this platform could find — it looked for
		// `dispatch.id`, got nothing, and every worker read "dispatched" for ever.
		s.record(ctx, store.OrcaDispatch{
			RunID: s.run.ID, OrcaRunID: s.runID, OrcaTaskID: s.taskID,
			Worktree: worker.Worktree.Path, Branch: worker.Worktree.Branch,
			Role: name, Status: "dispatched",
		})
		started = append(started, orcaWorkerRef{
			Agent: name, WorkerName: s.workerName(name),
			Worktree: worker.Worktree.Path, Branch: worker.Worktree.Branch,
		})
	}
	return started, nil
}

// orcaWorkerRef is one worker the fabric accepted, kept so the run can wait for
// it rather than reporting the dispatch as the work.
type orcaWorkerRef struct {
	Agent string
	// WorkerName is the checkout this platform asked the fabric to make. The
	// fabric's listing carries it inside the worktree id, which is how a state
	// there is attributed to an agent named here — the two commands do not share
	// a vocabulary, and this is the one field both of them agree on.
	WorkerName string
	Worktree   string
	Branch     string
}

// orcaWorkerListing is the fabric's answer about every worker it holds.
//
// Its field names are not the ones worker-show uses — this one answers in
// camelCase and that one in snake_case — which is worth writing down rather than
// discovering twice.
type orcaWorkerListing struct {
	Workers []struct {
		DispatchID     string `json:"dispatchId"`
		TaskID         string `json:"taskId"`
		WorkerState    string `json:"workerState"`
		DispatchStatus string `json:"dispatchStatus"`
		Resource       struct {
			WorktreeID string `json:"worktreeId"`
		} `json:"resource"`
	} `json:"workers"`
}

// orcaEnded are the states that mean a worker is over. Anything else — a word
// this platform has not seen — is treated as still going.
//
// It was the other way round for one release, and a live run showed why that is
// wrong: the fabric reported `ready`, which this platform had never met, so the
// wait called the worker settled and the run called it a failure. Guessing that
// an unfamiliar word means "finished" produces a wrong verdict immediately;
// guessing it means "still going" costs at most the wait, which the Goal's time
// limit already bounds and which reports the worker as unfinished rather than
// failed.
var orcaEnded = map[string]bool{
	"completed": true, "succeeded": true, "success": true, "done": true, "finished": true,
	"failed": true, "error": true, "errored": true,
	"cancelled": true, "canceled": true, "stopped": true, "aborted": true,
	"timeout": true, "timed_out": true, "expired": true,
}

// waitForWorkers waits until every worker has settled, and says what each did.
//
// Handing a task to the fabric and calling that done reported the dispatch as
// the work: the run said "워커 N개를 붙였습니다" and completed while the agents
// were still typing — so a worker that failed two minutes later did so behind a
// task marked successful. Waiting is what makes the run's verdict about the
// work.
//
// The wait ends when the Goal's time runs out or the task is cancelled, and both
// are reported as themselves rather than as a worker failure.
func (s *orcaSession) waitForWorkers(ctx context.Context, workers []orcaWorkerRef, goal store.AgentGoal) ([]string, error) {
	if len(workers) == 0 {
		return nil, nil
	}
	deadline := time.NewTimer(orcaWorkerLimit(goal))
	defer deadline.Stop()
	ticker := time.NewTicker(orcaWorkerPoll)
	defer ticker.Stop()

	settled := make(map[string]string, len(workers))
	details := make(map[string]string, len(workers))
	for {
		states := s.workerStates(ctx)
		for _, worker := range workers {
			if _, done := settled[worker.Agent]; done {
				continue
			}
			state, found := states[worker.WorkerName]
			if !found || !orcaEnded[strings.ToLower(strings.TrimSpace(state.status))] {
				continue
			}
			// The listing says which workers are done and not why. The reason is
			// on the record for one worker, which can now be asked for by an id
			// that exists — the listing is where this platform first got one.
			detail := state.detail
			if state.dispatchID != "" {
				if _, shown := s.workerStatus(ctx, state.dispatchID); shown != "" {
					detail = shown
				}
			}
			settled[worker.Agent], details[worker.Agent] = state.status, detail
			s.record(ctx, store.OrcaDispatch{
				RunID: s.run.ID, OrcaRunID: s.runID, OrcaTaskID: s.taskID,
				Terminal: state.dispatchID, Worktree: worker.Worktree, Branch: worker.Branch,
				Role: worker.Agent, Status: state.status, Detail: state.detail,
			})
		}
		if len(settled) == len(workers) {
			return orcaWorkerSummary(workers, settled, details), nil
		}
		select {
		case <-ctx.Done():
			// The same sentence every other in-Pod backend gives, rather than
			// "context deadline exceeded" — which is what this said until a live
			// run produced exactly that.
			return orcaWorkerSummary(workers, settled, details),
				errors.New(runtimeExecFailure("워커 실행", ctx.Err(), goal))
		case <-deadline.C:
			return orcaWorkerSummary(workers, settled, details),
				errors.New("워커가 최대 실행 시간 안에 끝나지 않았습니다. 런타임 터미널에서 `orca status --json` 으로 이어서 확인할 수 있습니다")
		case <-ticker.C:
		}
	}
}

// orcaWorkerSummary is one line per worker, in the fabric's own words.
func orcaWorkerSummary(workers []orcaWorkerRef, settled, details map[string]string) []string {
	lines := make([]string, 0, len(workers))
	for _, worker := range workers {
		state, ok := settled[worker.Agent]
		if !ok {
			lines = append(lines, worker.Agent+": 아직 끝나지 않았습니다")
			continue
		}
		line := worker.Agent + ": " + state
		if detail := strings.TrimSpace(details[worker.Agent]); detail != "" {
			line += " — " + trimmed(detail, 200)
		}
		lines = append(lines, line)
	}
	return lines
}

// workerStatus asks the fabric what became of a dispatch it just accepted.
//
// Accepting a dispatch and running a worker are different events, and the second
// one can fail on its own — most obviously when the agent is not installed on the
// host, where the launch dies in the worker's terminal with a shell error and the
// fabric records `agent_prompt_stalled`. This is the difference between the
// platform reporting what happened and reporting what it asked for.
func (s *orcaSession) workerStatus(ctx context.Context, dispatchID string) (string, string) {
	if dispatchID == "" {
		return "dispatched", ""
	}
	var shown struct {
		Dispatch struct {
			Status      string `json:"status"`
			LastFailure string `json:"last_failure"`
		} `json:"dispatch"`
		// worker-show answers in snake_case where worker-list answers in camel.
		// The worker's own record is where the reason lives: `agent_prompt_stalled`
		// on a dispatch that says only "failed".
		Worker struct {
			State     string `json:"state"`
			LastError string `json:"last_error"`
		} `json:"worker"`
	}
	if err := s.call(ctx, &shown, "orchestration", "worker-show", "--dispatch", dispatchID, "--json"); err != nil {
		// Not knowing is not the same as failing, and saying "failed" here would
		// fail a task because one inspection call did not answer.
		return "dispatched", "상태를 확인하지 못했습니다: " + trimmed(err.Error(), 200)
	}
	status, detail := orcaWorkerOutcome(shown.Dispatch.Status, shown.Dispatch.LastFailure)
	if strings.TrimSpace(detail) == "" {
		// The dispatch says it failed; the worker record says what of. Both are
		// in the same answer and only one of them is a sentence.
		detail = strings.TrimSpace(shown.Worker.LastError)
	}
	return status, detail
}

// orcaWorkerOutcome reads one worker-show record.
//
// Kept apart from the call so the decision can be checked against the records a
// live fabric actually produced. A record with no status means the fabric has not
// settled it yet, which is "dispatched" — not "failed", and not "running" either.
func orcaWorkerOutcome(status, lastFailure string) (string, string) {
	if strings.TrimSpace(status) == "" {
		return "dispatched", ""
	}
	return status, lastFailure
}

// orcaWorkerFailure says what to do about a refusal rather than repeating its
// code.
//
// `agent_unconfigured` is the one an operator will actually meet: the fabric
// starts a vendor's coding agent, and that agent needs an account registered on
// the host through the vendor's own interactive login. No image carries that and
// this platform cannot perform it, so the honest answer names the command and
// the place rather than the error.
func orcaWorkerFailure(name string, err error) error {
	if strings.Contains(err.Error(), "agent_unconfigured") {
		return fmt.Errorf("이 호스트에 %s 계정이 등록돼 있지 않아 워커를 시작하지 못했습니다. "+
			"런타임 터미널에서 `orca account add --agent %s` 로 먼저 로그인해 주세요 — 벤더 로그인이라 플랫폼이 대신 할 수 없습니다.", name, name)
	}
	if strings.Contains(err.Error(), "consumer_fenced") {
		return fmt.Errorf("%s 워커를 시작할 권한이 이 터미널에 없습니다. 이 작업의 코디네이터 터미널이 살아 있는지 확인해 주세요.", name)
	}
	return fmt.Errorf("%s 워커를 시작하지 못했습니다: %w", name, err)
}

// workerName is the checkout each worker gets, and it has to differ per agent or
// two workers land in one place and edit each other's files — which is the exact
// thing separate checkouts exist to prevent.
func (s *orcaSession) workerName(agent string) string {
	base := strings.TrimPrefix(s.worktreeName, "agenthub-")
	return "agenthub-" + base + "-" + orcaSafeName(agent)
}

func (s *orcaSession) record(ctx context.Context, dispatch store.OrcaDispatch) {
	if err := s.orchestrator.store.SaveOrcaDispatch(ctx, dispatch); err != nil {
		s.orchestrator.logger.Error("an orca dispatch could not be recorded", "run", s.run.ID, "error", err)
	}
}

// orcaAgentNames reads the Goal's list, dropping anything that could not be an
// agent id. The names go on a command line as arguments, and one that is a
// sentence would fail with a message about flags rather than about the agent.
// OrcaAgentNames is exported so the API can clean the same list the runner will
// read, rather than each keeping its own idea of what an agent id may contain.
func OrcaAgentNames(list string) []string {
	names := []string{}
	for _, raw := range strings.Split(list, ",") {
		name := orcaSafeName(raw)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// orcaSafeName keeps what an agent id can be made of.
func orcaSafeName(raw string) string {
	kept := strings.Builder{}
	for _, char := range strings.TrimSpace(strings.ToLower(raw)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
			kept.WriteRune(char)
		}
	}
	name := kept.String()
	if len(name) > 40 {
		name = name[:40]
	}
	return name
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

// trimmedSuffix adds the container's last words when it left any.
func trimmedSuffix(text string) string {
	if detail := trimmed(text, 200); strings.TrimSpace(detail) != "" {
		return ": " + detail
	}
	return ""
}

// orcaWorkerState is one worker as the fabric's listing describes it.
type orcaWorkerState struct {
	dispatchID string
	status     string
	detail     string
}

// workerStates reads every worker the fabric holds, keyed by the checkout name
// this platform asked for.
//
// One call rather than one per worker, and a listing rather than the answer to
// the command that started them: worker-start's reply carries no dispatch id
// this code could find, which is why every worker read "dispatched" until the
// run's time ran out.
func (s *orcaSession) workerStates(ctx context.Context) map[string]orcaWorkerState {
	var listing orcaWorkerListing
	if err := s.call(ctx, &listing, "orchestration", "worker-list", "--json"); err != nil {
		// Not knowing is not a state. The wait tries again on its next turn.
		return nil
	}
	states := map[string]orcaWorkerState{}
	for _, worker := range listing.Workers {
		if s.taskID != "" && worker.TaskID != "" && worker.TaskID != s.taskID {
			continue
		}
		status := strings.TrimSpace(worker.WorkerState)
		if status == "" {
			status = strings.TrimSpace(worker.DispatchStatus)
		}
		name := worker.Resource.WorktreeID
		if at := strings.LastIndex(name, "/"); at >= 0 {
			name = name[at+1:]
		}
		if name == "" {
			continue
		}
		states[name] = orcaWorkerState{dispatchID: worker.DispatchID, status: status}
	}
	return states
}
