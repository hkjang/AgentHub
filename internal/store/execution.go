package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Task statuses. Only the ones the execution plane actually transitions through
// are named here; the database CHECK constraint holds the full set.
const (
	TaskQueued     = "queued"
	TaskRunning    = "running"
	TaskRetrying   = "retrying"
	TaskCompleted  = "completed"
	TaskFailed     = "failed"
	TaskCancelled  = "cancelled"
	TaskDeadLetter = "dead_letter"
	// TaskBlocked is work that is ready but not allowed to run yet — today only
	// the promotion gate holds tasks here. It is deliberately not a failure: the
	// task resumes on its own once the block is lifted.
	TaskBlocked = "blocked"
	// TaskHandoff is work the agent could not finish in a prose loop and handed to
	// a person in the runtime. Also not a failure: the transcript stands, the
	// workspace is where the agent left it, and a person decides how it ends.
	TaskHandoff = "handoff"
)

// taskFinished is the set a task never leaves on its own. It exists as an
// expression because two different questions turn on it: what to put in front of
// somebody first, and what still needs a person.
const taskFinished = `('completed','failed','dead_letter','cancelled')`

// taskUnfinishedFirst puts work that has not ended above work that has.
//
// The list is capped and ordered newest first, which is right for a log and wrong
// for a work queue: a task waiting for an approval or handed to a person is
// exactly the task that gets older, so on a busy deployment it slides past the end
// of the page and stops existing as far as the screen is concerned. The longer it
// waits, the less visible it is — the opposite of what waiting should do.
const taskUnfinishedFirst = `CASE WHEN t.status IN ` + taskFinished + ` THEN 1 ELSE 0 END`

// taskPriorityRank orders the queue. Postgres has no ordering for these strings,
// so the claim query sorts on this expression.
const taskPriorityRank = `CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END`

// ACPToolPolicy is what a goal says about tools by name, for ACP runs where the
// platform answers each permission request itself.
//
// Two lists rather than one mode, because they answer different questions and an
// operator usually has both. Never is a rule: it holds even under yolo, since a
// blocklist an approval mode can overrule is not a blocklist. Without asking is
// the remedy for an agent that reports every tool as `other` — the kind-based
// mode cannot tell `npm test` from `rm -rf`, and a name can.
type ACPToolPolicy struct {
	// Deny refuses the request outright, whatever the mode says.
	Deny []string `json:"deny,omitempty"`
	// Allow runs the tool without asking anybody, before the mode is consulted.
	Allow []string `json:"allow,omitempty"`
}

// Empty reports a policy that says nothing, so the caller can keep the old path
// exactly as it was.
func (p ACPToolPolicy) Empty() bool { return len(p.Deny) == 0 && len(p.Allow) == 0 }

type AgentGoal struct {
	AgentID            string   `json:"agentId"`
	Description        string   `json:"description"`
	SuccessCriteria    []string `json:"successCriteria"`
	FailureCriteria    []string `json:"failureCriteria"`
	Constraints        string   `json:"constraints"`
	MaxSteps           int      `json:"maxSteps"`
	MaxToolCalls       int      `json:"maxToolCalls"`
	MaxDurationSeconds int      `json:"maxDurationSeconds"`
	MaxRetries         int      `json:"maxRetries"`
	StartOnDemand      bool     `json:"startOnDemand"`
	StopAfterTask      bool     `json:"stopAfterTask"`
	CompletionStrategy string   `json:"completionStrategy"`
	ConcurrencyPolicy  string   `json:"concurrencyPolicy"`
	MaxConcurrentRuns  int      `json:"maxConcurrentRuns"`
	// PlannerMode decides who plans: 'native' leaves it to the runtime adapter,
	// 'platform' has AgentHub produce a plan first, 'hybrid' does both.
	PlannerMode string `json:"plannerMode"`
	// ApprovalRequired parks the task before any state-changing action the agent
	// declares, until a reviewer decides.
	ApprovalRequired bool `json:"approvalRequired"`
	// MaxDelegationDepth bounds agent-to-agent hand-off; 0 forbids it.
	MaxDelegationDepth int `json:"maxDelegationDepth"`
	// WarmupSeconds starts the Runtime this long before a scheduled trigger
	// fires, so the task does not pay for a cold Pod. Zero disables it.
	WarmupSeconds int `json:"warmupSeconds"`
	// KeepWarmSeconds holds the Runtime for this long after a task ends instead
	// of stopping it at once, so a burst pays the start cost once.
	KeepWarmSeconds int `json:"keepWarmSeconds"`
	// ResumeFromCheckpoint lets a retry continue from the steps earlier attempts
	// completed instead of starting the task over. On by default: repeating
	// completed work costs tokens twice and performs its side effects twice.
	ResumeFromCheckpoint bool `json:"resumeFromCheckpoint"`
	// TokenBudget bounds what this agent may spend over the reporting window.
	// Zero leaves it bounded only by its owner's budget.
	TokenBudget int64 `json:"tokenBudget"`
	// Runner decides where the work happens: "prose" reasons at the model
	// gateway, "flow" runs a flow the runtime itself holds. Only a runtime whose
	// descriptor reports FlowExecution can use the second.
	Runner string `json:"runner"`
	// FlowID is the runtime's own id for the flow to run.
	FlowID string `json:"flowId"`
	// FlowOutputComponent picks which output to read when a flow has several.
	// Empty takes the flow's own answer.
	FlowOutputComponent string `json:"flowOutputComponent"`
	// ExternalAppID is the application a `dify` Goal sends its work to.
	ExternalAppID string `json:"externalAppId"`
	// ExternalInputKey names the variable the task's text goes into for an app
	// that takes named inputs rather than a prompt. Empty uses the product's
	// default for that kind.
	ExternalInputKey string `json:"externalInputKey"`
	// ToolPolicy names tools rather than kinds, for the runs where the kind does
	// not distinguish anything.
	ToolPolicy ACPToolPolicy `json:"toolPolicy"`
	// ApprovalMode is how much a run may do without asking.
	// when it runs a task unattended. It is stored rather than defaulted at the
	// call site because the difference between "plan" and "yolo" is the difference
	// between a report and a changed repository.
	ApprovalMode string `json:"approvalMode"`
	// The name it had when it served only the headless runner, still written so a
	// client reading that field keeps working. Ignored on the way in — the API
	// reads the old name from the request itself — and due to be dropped once the
	// documented deprecation has passed.
	LegacyApprovalMode string `json:"cliApprovalMode"`
}

// The kinds of step a run's timeline can hold. They are constants rather than
// literals at each call site because the database checks them: a step written
// with a type the constraint does not allow is refused, the failure is logged
// rather than raised, and the run ends up claiming work whose evidence is
// missing. A test pins this list to the migration that enforces it.
const (
	StepPlan       = "plan"
	StepReasoning  = "reasoning"
	StepTool       = "tool"
	StepArtifact   = "artifact"
	StepCompletion = "completion"
	StepDelegation = "delegation"
	// StepFlow is one execution of a runtime's own saved flow.
	StepFlow = "flow"
	// StepCLI is one headless run of a runtime's own agent.
	StepCLI = "cli"
	// StepExternal is one call to an application the platform does not run.
	StepExternal = "external"
	// StepACP is one turn of a protocol conversation with a runtime's own agent.
	StepACP = "acp"
	// StepInvestigate is one investigation: a question, the evidence gathered for
	// it, and the conclusion drawn from that evidence.
	StepInvestigate = "investigate"
)

// RunStepTypes is every type the platform writes.
var RunStepTypes = []string{StepPlan, StepReasoning, StepTool, StepArtifact, StepCompletion, StepDelegation, StepFlow, StepCLI, StepExternal, StepACP, StepInvestigate}

// The two places a task's work can happen.
const (
	// RunnerProse reasons step by step against the agent's model endpoint.
	RunnerProse = "prose"
	// RunnerFlow hands the task to a flow the runtime holds and keeps its answer.
	RunnerFlow = "flow"
	// RunnerCLI runs the runtime's own agent headlessly, in its own workspace.
	RunnerCLI = "cli"
	// RunnerDify calls an application the site already runs. Nothing is started
	// for it: the work happens somewhere else and the platform keeps the record.
	RunnerDify = "dify"
	// RunnerInvestigate hands the task to an incident investigator, which answers
	// with a conclusion and the evidence it gathered for it.
	RunnerInvestigate = "investigate"
	// RunnerACP speaks the Agent Client Protocol to whatever agent the runtime
	// holds. One adapter, many agents: the platform is the client and the thing
	// in the Pod is the agent, whichever vendor wrote it.
	RunnerACP = "acp"
)

// DefaultAgentGoal is what an agent without an explicit goal runs with, so a
// manual task on a plain interactive agent still executes sensibly.
func DefaultAgentGoal(agentID string) AgentGoal {
	return AgentGoal{
		AgentID: agentID, SuccessCriteria: []string{}, FailureCriteria: []string{},
		MaxSteps: 10, MaxToolCalls: 50, MaxDurationSeconds: 1800, MaxRetries: 2,
		StartOnDemand: true, StopAfterTask: false,
		CompletionStrategy: "agent", ConcurrencyPolicy: "queue", MaxConcurrentRuns: 1,
		PlannerMode: "native", ApprovalRequired: false, MaxDelegationDepth: 0,
		WarmupSeconds: 0, KeepWarmSeconds: 0, ResumeFromCheckpoint: true,
		Runner: RunnerProse, ApprovalMode: "default",
	}
}

type AgentTrigger struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agentId"`
	OwnerID     string     `json:"ownerId"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Enabled     bool       `json:"enabled"`
	Schedule    string     `json:"schedule"`
	Timezone    string     `json:"timezone"`
	TaskTitle   string     `json:"taskTitle"`
	TaskInput   string     `json:"taskInput"`
	Priority    string     `json:"priority"`
	LastFiredAt *time.Time `json:"lastFiredAt,omitempty"`
	NextFireAt  *time.Time `json:"nextFireAt,omitempty"`
	// HasSecret reports whether a webhook secret is configured. The secret itself
	// is never returned.
	HasSecret bool `json:"hasSecret"`
	// EventType and EventFilter apply to event triggers: the platform event to
	// react to, and an optional equality filter over its payload so one agent can
	// watch a single subject rather than every event of that type.
	EventType   string          `json:"eventType"`
	EventFilter json.RawMessage `json:"eventFilter,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type AgentTask struct {
	ID           string     `json:"id"`
	AgentID      string     `json:"agentId"`
	AgentName    string     `json:"agentName,omitempty"`
	OwnerID      string     `json:"ownerId"`
	Title        string     `json:"title"`
	Input        string     `json:"input"`
	Priority     string     `json:"priority"`
	Status       string     `json:"status"`
	Source       string     `json:"source"`
	TriggerID    *string    `json:"triggerId,omitempty"`
	Attempts     int        `json:"attempts"`
	ScheduledAt  time.Time  `json:"scheduledAt"`
	DeadlineAt   *time.Time `json:"deadlineAt,omitempty"`
	CurrentRunID *string    `json:"currentRunId,omitempty"`
	ParentTaskID *string    `json:"parentTaskId,omitempty"`
	Delegation   int        `json:"delegationDepth"`
	ApprovalID   *string    `json:"approvalId,omitempty"`
	LastError    string     `json:"lastError"`
	// WaitingReason is why the task is queued rather than running — a quota it is
	// waiting behind. Kept apart from LastError because waiting is not failing,
	// and because a defer must not erase the message from an attempt that did.
	WaitingReason string    `json:"waitingReason,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type AgentRun struct {
	ID              string  `json:"id"`
	TaskID          string  `json:"taskId"`
	AgentID         string  `json:"agentId"`
	OwnerID         string  `json:"ownerId"`
	Attempt         int     `json:"attempt"`
	Status          string  `json:"status"`
	AgentVersion    int     `json:"agentVersion"`
	RuntimeID       *string `json:"runtimeId,omitempty"`
	ModelEndpointID *string `json:"modelEndpointId,omitempty"`
	ModelName       string  `json:"modelName"`
	TraceID         string  `json:"traceId"`
	WorkerID        string  `json:"workerId"`
	// ResumedSteps is how many completed steps this run inherited from the task's
	// earlier attempts. Zero means it started from the beginning.
	ResumedSteps int `json:"resumedSteps"`
	StepCount    int `json:"stepCount"`
	ToolCalls    int `json:"toolCalls"`
	TotalTokens  int `json:"totalTokens"`
	// AgentName is filled in by listings, which join it so a page of runs reads
	// as "which agent" rather than as a page of identifiers.
	AgentName string `json:"agentName,omitempty"`
	// Metering says who counted those tokens: the platform at its own model
	// gateway, the agent reporting its own spend, only the agent's context
	// occupancy, or nothing at all. Empty on runs that finished before this was
	// recorded — they are not relabelled, because guessing at history is how a
	// report stops being evidence.
	Metering      string          `json:"metering,omitempty"`
	DurationMs    int64           `json:"durationMs"`
	Result        string          `json:"result"`
	FailureReason string          `json:"failureReason"`
	Completion    json.RawMessage `json:"completion"`
	StartedAt     time.Time       `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt,omitempty"`
}

type AgentRunStep struct {
	ID               string    `json:"id"`
	RunID            string    `json:"runId"`
	Sequence         int       `json:"sequence"`
	Type             string    `json:"type"`
	Title            string    `json:"title"`
	Input            string    `json:"input"`
	Output           string    `json:"output"`
	Status           string    `json:"status"`
	Error            string    `json:"error"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	DurationMs       int64     `json:"durationMs"`
	CreatedAt        time.Time `json:"createdAt"`
}

type AgentRunEvent struct {
	ID         int64           `json:"id"`
	RunID      string          `json:"runId"`
	TaskID     string          `json:"taskId"`
	Type       string          `json:"type"`
	Message    string          `json:"message"`
	Details    json.RawMessage `json:"details"`
	OccurredAt time.Time       `json:"occurredAt"`
}

type AgentArtifact struct {
	ID          string    `json:"id"`
	RunID       string    `json:"runId"`
	TaskID      string    `json:"taskId"`
	AgentID     string    `json:"agentId"`
	OwnerID     string    `json:"ownerId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	Content     string    `json:"content,omitempty"`
	StorageRef  string    `json:"storageRef,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Who counted a run's tokens. The names are values in the database's own CHECK,
// so a run written with something else is refused rather than quietly stored.
const (
	// MeteringGateway: the platform made the model calls, so every token is counted.
	MeteringGateway = "gateway"
	// MeteringAgent: the agent reported its own spend and it was taken as given.
	MeteringAgent = "agent"
	// MeteringContextOnly: the agent said how full its context was and nothing
	// about spend. Context occupancy is not what was bought.
	MeteringContextOnly = "context_only"
	// MeteringUnmetered: the agent reported nothing. Tokens on such a run are the
	// platform's own — planning, judging — and not the agent's work.
	MeteringUnmetered = "unmetered"
)

// --- Goals ---

func (s *Store) AgentGoalByID(ctx context.Context, agentID string) (AgentGoal, error) {
	item := AgentGoal{AgentID: agentID}
	var success, failure, toolPolicy []byte
	err := s.pool.QueryRow(ctx, `SELECT description,success_criteria,failure_criteria,constraints,max_steps,max_tool_calls,max_duration_seconds,max_retries,start_on_demand,stop_after_task,completion_strategy,concurrency_policy,max_concurrent_runs,planner_mode,approval_required,max_delegation_depth,warmup_seconds,keep_warm_seconds,resume_from_checkpoint,token_budget,runner,flow_id,flow_output_component,approval_mode,COALESCE(external_app_id,''),external_input_key,tool_policy FROM agent_goals WHERE agent_id=$1`, agentID).
		Scan(&item.Description, &success, &failure, &item.Constraints, &item.MaxSteps, &item.MaxToolCalls, &item.MaxDurationSeconds, &item.MaxRetries, &item.StartOnDemand, &item.StopAfterTask, &item.CompletionStrategy, &item.ConcurrencyPolicy, &item.MaxConcurrentRuns, &item.PlannerMode, &item.ApprovalRequired, &item.MaxDelegationDepth, &item.WarmupSeconds, &item.KeepWarmSeconds, &item.ResumeFromCheckpoint, &item.TokenBudget, &item.Runner, &item.FlowID, &item.FlowOutputComponent, &item.ApprovalMode, &item.ExternalAppID, &item.ExternalInputKey, &toolPolicy)
	if errors.Is(err, pgx.ErrNoRows) {
		return WithLegacyNames(DefaultAgentGoal(agentID)), nil
	}
	if err != nil {
		return AgentGoal{}, err
	}
	_ = json.Unmarshal(success, &item.SuccessCriteria)
	_ = json.Unmarshal(failure, &item.FailureCriteria)
	_ = json.Unmarshal(toolPolicy, &item.ToolPolicy)
	if item.SuccessCriteria == nil {
		item.SuccessCriteria = []string{}
	}
	if item.FailureCriteria == nil {
		item.FailureCriteria = []string{}
	}
	return WithLegacyNames(item), nil
}

func (s *Store) PutAgentGoal(ctx context.Context, item AgentGoal) (AgentGoal, error) {
	success, _ := json.Marshal(item.SuccessCriteria)
	failure, _ := json.Marshal(item.FailureCriteria)
	toolPolicy, _ := json.Marshal(cleanToolPolicy(item.ToolPolicy))
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_goals(agent_id,description,success_criteria,failure_criteria,constraints,max_steps,max_tool_calls,max_duration_seconds,max_retries,start_on_demand,stop_after_task,completion_strategy,concurrency_policy,max_concurrent_runs,planner_mode,approval_required,max_delegation_depth,warmup_seconds,keep_warm_seconds,resume_from_checkpoint,token_budget,runner,flow_id,flow_output_component,approval_mode,external_app_id,external_input_key,tool_policy)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
		ON CONFLICT(agent_id) DO UPDATE SET description=excluded.description,success_criteria=excluded.success_criteria,failure_criteria=excluded.failure_criteria,constraints=excluded.constraints,max_steps=excluded.max_steps,max_tool_calls=excluded.max_tool_calls,max_duration_seconds=excluded.max_duration_seconds,max_retries=excluded.max_retries,start_on_demand=excluded.start_on_demand,stop_after_task=excluded.stop_after_task,completion_strategy=excluded.completion_strategy,concurrency_policy=excluded.concurrency_policy,max_concurrent_runs=excluded.max_concurrent_runs,planner_mode=excluded.planner_mode,approval_required=excluded.approval_required,max_delegation_depth=excluded.max_delegation_depth,warmup_seconds=excluded.warmup_seconds,keep_warm_seconds=excluded.keep_warm_seconds,resume_from_checkpoint=excluded.resume_from_checkpoint,token_budget=excluded.token_budget,runner=excluded.runner,flow_id=excluded.flow_id,flow_output_component=excluded.flow_output_component,approval_mode=excluded.approval_mode,external_app_id=excluded.external_app_id,external_input_key=excluded.external_input_key,tool_policy=excluded.tool_policy,updated_at=now()`,
		item.AgentID, item.Description, success, failure, item.Constraints, item.MaxSteps, item.MaxToolCalls, item.MaxDurationSeconds, item.MaxRetries, item.StartOnDemand, item.StopAfterTask, item.CompletionStrategy, item.ConcurrencyPolicy, item.MaxConcurrentRuns, item.PlannerMode, item.ApprovalRequired, item.MaxDelegationDepth, item.WarmupSeconds, item.KeepWarmSeconds, item.ResumeFromCheckpoint, item.TokenBudget, runnerOrDefault(item.Runner), item.FlowID, item.FlowOutputComponent, approvalModeOrDefault(item.ApprovalMode), nullText(item.ExternalAppID), item.ExternalInputKey, toolPolicy)
	if err != nil {
		return AgentGoal{}, err
	}
	return s.AgentGoalByID(ctx, item.AgentID)
}

// runnerOrDefault keeps a goal written by an older client — or by a test that
// only sets the fields it cares about — on the prose loop rather than failing the
// CHECK constraint with an empty string.
// withLegacyNames mirrors renamed fields under the names they used to have, so
// a client written against the old API keeps reading a value rather than an
// empty string.
func WithLegacyNames(item AgentGoal) AgentGoal {
	item.LegacyApprovalMode = item.ApprovalMode
	return item
}

func runnerOrDefault(value string) string {
	switch value {
	case RunnerFlow, RunnerCLI, RunnerDify, RunnerACP, RunnerInvestigate:
		return value
	}
	return RunnerProse
}

// approvalModeOrDefault keeps a goal written without one on the runtime's own
// default, which asks before it changes anything. Defaulting the other way would
// make an unattended agent edit a repository because a field was omitted.
func approvalModeOrDefault(value string) string {
	switch value {
	case "plan", "default", "auto-edit", "auto", "yolo":
		return value
	}
	return "default"
}

func (s *Store) SetAgentExecutionMode(ctx context.Context, agentID, ownerID string, admin bool, mode string) error {
	query := `UPDATE agent_definitions SET execution_mode=$2,updated_at=now() WHERE id=$1`
	args := []any{agentID, mode}
	if !admin {
		query += ` AND owner_id=$3`
		args = append(args, ownerID)
	}
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AgentExecutionMode(ctx context.Context, agentID string) (string, error) {
	var mode string
	err := s.pool.QueryRow(ctx, `SELECT execution_mode FROM agent_definitions WHERE id=$1`, agentID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return mode, err
}

// --- Tasks ---

type CreateTaskInput struct {
	AgentID      string
	OwnerID      string
	Title        string
	Input        string
	Priority     string
	Source       string
	TriggerID    *string
	CreatedBy    string
	ScheduledAt  *time.Time
	DeadlineAt   *time.Time
	ParentTaskID *string
	Delegation   int
}

func (s *Store) CreateAgentTask(ctx context.Context, input CreateTaskInput) (AgentTask, error) {
	if input.Priority == "" {
		input.Priority = "normal"
	}
	if input.Source == "" {
		input.Source = "manual"
	}
	scheduled := time.Now().UTC()
	if input.ScheduledAt != nil {
		scheduled = *input.ScheduledAt
	}
	var item AgentTask
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_tasks(id,agent_id,owner_id,title,input,priority,source,trigger_id,created_by,scheduled_at,deadline_at,parent_task_id,delegation_depth)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+taskOwnColumns,
		uuid.NewString(), input.AgentID, input.OwnerID, input.Title, input.Input, input.Priority, input.Source, input.TriggerID, nullText(input.CreatedBy), scheduled, input.DeadlineAt, input.ParentTaskID, input.Delegation).
		Scan(item.scanTargets()...)
	return item, err
}

// taskOwnColumns are the task's own fields, unprefixed, as the insert returns
// them; taskCoreColumns is the same list qualified for the queries that join the
// agent. scanTask additionally expects the agent name.
const taskOwnColumns = `id,agent_id,owner_id,title,input,priority,status,source,trigger_id,attempts,scheduled_at,deadline_at,current_run_id,parent_task_id,delegation_depth,approval_id,last_error,waiting_reason,created_at,updated_at`

var taskCoreColumns = prefixColumns("t", taskOwnColumns)

var taskColumns = taskCoreColumns + `, a.name`

// scanTargets is where taskOwnColumns lands, in that order. The insert reads the
// same columns back and cannot use scanTask, so it goes through here — the third
// column list in this package to have been spelled out twice, and the second to
// have broken something when one copy was updated and the other was not.
func (t *AgentTask) scanTargets() []any {
	return []any{&t.ID, &t.AgentID, &t.OwnerID, &t.Title, &t.Input, &t.Priority, &t.Status, &t.Source,
		&t.TriggerID, &t.Attempts, &t.ScheduledAt, &t.DeadlineAt, &t.CurrentRunID, &t.ParentTaskID,
		&t.Delegation, &t.ApprovalID, &t.LastError, &t.WaitingReason, &t.CreatedAt, &t.UpdatedAt}
}

func scanTask(row pgx.Row) (AgentTask, error) {
	var item AgentTask
	err := row.Scan(append(item.scanTargets(), &item.AgentName)...)
	return item, err
}

// AgentTaskCounts is how many tasks are in each status, over all of them.
//
// The console used to count the rows it had been given, which is a different
// question: the list is capped, so "승인 대기 2" meant two were waiting among the
// hundred most recent tasks — and read as two waiting altogether. On a deployment
// that finishes a hundred tasks a day, work waiting for a person showed as zero
// while it sat there.
func (s *Store) AgentTaskCounts(ctx context.Context, ownerID, agentID string) (map[string]int, error) {
	query := `SELECT status, count(*) FROM agent_tasks WHERE owner_id=$1`
	args := []any{ownerID}
	if agentID != "" {
		args = append(args, agentID)
		query += fmt.Sprintf(` AND agent_id=$%d`, len(args))
	}
	rows, err := s.pool.Query(ctx, query+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// AgentTasks lists a user's tasks, optionally filtered by agent and status.
func (s *Store) AgentTasks(ctx context.Context, ownerID, agentID, status string, limit int) ([]AgentTask, error) {
	limit = clampLimit(limit, 100, 200)
	query := `SELECT ` + taskColumns + ` FROM agent_tasks t JOIN agent_definitions a ON a.id=t.agent_id WHERE t.owner_id=$1`
	args := []any{ownerID}
	if agentID != "" {
		args = append(args, agentID)
		query += fmt.Sprintf(` AND t.agent_id=$%d`, len(args))
	}
	if status != "" {
		args = append(args, status)
		query += fmt.Sprintf(` AND t.status=$%d`, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY `+taskUnfinishedFirst+`, t.created_at DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentTask{}
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) AgentTaskByID(ctx context.Context, id, ownerID string, admin bool) (AgentTask, error) {
	query := `SELECT ` + taskColumns + ` FROM agent_tasks t JOIN agent_definitions a ON a.id=t.agent_id WHERE t.id=$1`
	args := []any{id}
	if !admin {
		query += ` AND t.owner_id=$2`
		args = append(args, ownerID)
	}
	item, err := scanTask(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	return item, err
}

// ClaimAgentTask leases the next due task to one worker.
//
// SKIP LOCKED is what makes several workers safe on one queue: a row another
// worker is already claiming is passed over instead of blocking. The lease has
// an expiry so a worker that dies does not strand its task forever.
func (s *Store) ClaimAgentTask(ctx context.Context, workerID string, lease time.Duration) (AgentTask, error) {
	query := `
		WITH claimed AS (
			SELECT id FROM agent_tasks
			WHERE status IN ('queued','retrying')
			  AND scheduled_at <= now()
			  AND (claimed_until IS NULL OR claimed_until < now())
			ORDER BY ` + taskPriorityRank + `, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE agent_tasks t
		SET status='running', claimed_by=$1, claimed_until=now() + $2::interval, attempts=t.attempts+1,
		    waiting_reason='', updated_at=now()
		FROM claimed
		WHERE t.id = claimed.id
		RETURNING ` + taskCoreColumns + `, (SELECT name FROM agent_definitions WHERE id=t.agent_id)`
	item, err := scanTask(s.pool.QueryRow(ctx, query, workerID, lease.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	return item, err
}

// ExtendTaskLease keeps a long-running task claimed while the worker is alive, and
// reports whether the worker still holds it.
//
// The row count was already the answer to "may this worker go on": a cancellation
// clears claimed_by, so the update stops matching. It was discarded, and the run
// carried on past a cancellation nobody had any way to enforce.
func (s *Store) ExtendTaskLease(ctx context.Context, taskID, workerID string, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET claimed_until=now() + $3::interval, updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND status='running'`, taskID, workerID, lease.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// FinishAgentTask records the terminal state and clears the lease.
//
// A cancelled task is left alone. The run that was stopped still comes back here
// with an outcome, and writing it would undo the cancellation — which is what used
// to happen: somebody cancelled a task and the row said completed a minute later,
// because the work it was cancelling had finished and reported itself.
func (s *Store) FinishAgentTask(ctx context.Context, taskID, status, lastError string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status=$2,last_error=$3,claimed_by='',claimed_until=NULL,updated_at=now()
		WHERE id=$1 AND status<>'cancelled'`, taskID, status, lastError)
	return err
}

// RetryAgentTask puts a failed task back on the queue after a backoff.
// BlockAgentTask holds a task until the thing standing in its way is resolved.
//
// The attempt the claim charged for is given back. Waiting for a person to
// promote a version is not a failed attempt, and spending the retry budget on it
// would leave the task out of attempts by the time it was allowed to run — which
// is what this comment claimed to prevent while the code did nothing about it.
// Claiming a task increments its attempt count before anything decides whether it
// may run, so a hold that leaves the count alone charges for the wait. The defer
// path next door hands the attempt back for exactly this reason; a hold is the
// same situation with a person rather than a free slot at the end of it.
func (s *Store) BlockAgentTask(ctx context.Context, taskID, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_tasks
		SET status='blocked', last_error=$2, attempts=GREATEST(attempts - 1, 0),
		    claimed_by='', claimed_until=NULL, updated_at=now()
		WHERE id=$1 AND status<>'cancelled'`, taskID, reason)
	return err
}

// HandOffTask parks a task for a person to finish in the runtime.
//
// The note is stored where every other "why is this waiting" message lives, so
// the task list can explain the state without a second lookup. The attempt count
// is untouched: the agent did its attempt, and it ended in a handover rather than
// a failure.
func (s *Store) HandOffTask(ctx context.Context, taskID, note string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_tasks
		SET status='handoff', last_error=$2, claimed_by='', claimed_until=NULL, updated_at=now()
		WHERE id=$1 AND status<>'cancelled'`, taskID, note)
	return err
}

// ResolveHandoffTask records how a person finished — or gave up on — a task they
// took over.
//
// Only a handed-off task can be resolved this way. Letting anyone mark any task
// completed would make the status meaningless; letting nobody close this one
// would leave every handover open forever.
func (s *Store) ResolveHandoffTask(ctx context.Context, taskID, ownerID, status, note string, admin bool) (AgentTask, error) {
	if status != TaskCompleted && status != TaskCancelled {
		return AgentTask{}, errors.New("인계된 작업은 완료 또는 취소로만 마무리할 수 있습니다")
	}
	// Aliased as t because the shared column list is written for the claim query,
	// which needs the alias.
	query := `UPDATE agent_tasks t SET status=$3, last_error=$4, updated_at=now()
		WHERE t.id=$1 AND t.status='handoff' AND ($2 = '' OR t.owner_id=$2)
		RETURNING ` + taskCoreColumns + `, (SELECT name FROM agent_definitions WHERE id=t.agent_id)`
	owner := ownerID
	if admin {
		owner = ""
	}
	item, err := scanTask(s.pool.QueryRow(ctx, query, taskID, owner, status, note))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	return item, err
}

// ReleaseBlockedTasks puts an agent's held tasks back on the queue and reports
// how many moved, so the person who lifted the block is told what it started.
func (s *Store) ReleaseBlockedTasks(ctx context.Context, agentID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_tasks
		SET status='queued', last_error='', waiting_reason='', scheduled_at=now(), updated_at=now()
		WHERE agent_id=$1 AND status='blocked'`, agentID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) RetryAgentTask(ctx context.Context, taskID string, delay time.Duration, lastError string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status='retrying',scheduled_at=now() + $2::interval,last_error=$3,claimed_by='',claimed_until=NULL,updated_at=now()
		WHERE id=$1 AND status<>'cancelled'`, taskID, delay.String(), lastError)
	return err
}

// RequeueAgentTask returns a task to the queue for a manual retry, clearing the
// attempt counter so the operator gets a full retry budget again.
//
// A blocked task is included, and that was a gap worth naming: the block is
// lifted by fixing whatever caused it — promoting a version, changing a policy
// rule — and only the promotion had a path back. A task held by a policy could be
// released solely by promoting the agent, which has nothing to do with the rule
// that stopped it, so the honest answer was to let the owner press the same
// button they press for anything else once they have fixed the cause.
//
// fresh retires the steps completed so far, so the attempt starts from the
// beginning: the right choice when the earlier reasoning was based on something
// that has since been corrected. Otherwise the attempt resumes from them.
func (s *Store) RequeueAgentTask(ctx context.Context, taskID, ownerID string, fresh bool) (AgentTask, error) {
	checkpoint := `checkpoint_after=checkpoint_after`
	if fresh {
		checkpoint = `checkpoint_after=now()`
	}
	query := `UPDATE agent_tasks SET status='queued',attempts=0,scheduled_at=now(),last_error='',waiting_reason='',claimed_by='',claimed_until=NULL,` + checkpoint + `,updated_at=now()
		WHERE id=$1 AND owner_id=$2 AND status IN ('failed','dead_letter','cancelled','blocked') RETURNING id`
	var id string
	if err := s.pool.QueryRow(ctx, query, taskID, ownerID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The update matched nothing, which is two different situations: the
			// task is somebody else's or gone, or it is theirs and simply not in a
			// state that can be retried. Answering "찾을 수 없습니다" to the second is
			// how somebody comes to believe their task disappeared.
			var status string
			if lookupErr := s.pool.QueryRow(ctx, `SELECT status FROM agent_tasks WHERE id=$1 AND owner_id=$2`, taskID, ownerID).Scan(&status); lookupErr == nil {
				return AgentTask{}, Conflict{Message: retryRefusal(status)}
			}
			return AgentTask{}, ErrNotFound
		}
		return AgentTask{}, err
	}
	return s.AgentTaskByID(ctx, taskID, ownerID, false)
}

// retryRefusal says why this task cannot be put back on the queue, in terms of
// what it is doing now rather than in terms of the query that matched nothing.
func retryRefusal(status string) string {
	switch status {
	case "completed":
		return "이미 완료된 작업은 다시 실행할 수 없습니다. 같은 일을 다시 시키려면 새 작업을 만드세요."
	case "queued", "retrying":
		return "이 작업은 이미 대기 중입니다. 워커가 가져가면 실행됩니다."
	case "running", "planning", "ready", "waiting_tool":
		return "실행 중인 작업은 다시 실행할 수 없습니다. 먼저 취소하거나 끝나기를 기다려 주세요."
	case "waiting_approval":
		return "승인을 기다리는 작업입니다. 검토 화면에서 승인하거나 반려해 주세요."
	case "blocked":
		// Unreachable now that a blocked task may be requeued, and kept because
		// the state can be reached two ways and a future third might not be
		// releasable by hand.
		return "차단된 작업입니다. 막고 있는 것을 해제한 뒤 다시 실행할 수 있습니다."
	case "handoff":
		return "사람이 이어받은 작업입니다. 런타임에서 마무리하고 완료 처리해 주세요."
	}
	return "지금 상태(" + status + ")에서는 다시 실행할 수 없습니다."
}

// CancelAgentTask stops a task that has not reached a terminal state, and the
// unfinished work it delegated, and reports how many of those there were.
//
// An agent that hands part of its goal to another agent creates a task of its own
// — tracked separately, which is the right way to run it and the wrong way to stop
// it. Cancelling the parent left every delegated child running: still spending the
// same person's budget, still changing whatever it was going to change, on behalf
// of a goal that had been called off. The delegation is recorded, the descendants
// are one query away, and nothing asked.
//
// Only unfinished descendants are touched. A child that already completed is
// history, and a cancellation is not a reason to rewrite it.
func (s *Store) CancelAgentTask(ctx context.Context, taskID, ownerID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status='cancelled',claimed_by='',claimed_until=NULL,updated_at=now()
		WHERE id=$1 AND owner_id=$2 AND status NOT IN ('completed','cancelled','dead_letter')`, taskID, ownerID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrNotFound
	}
	descendants, err := s.pool.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, 0 AS depth FROM agent_tasks WHERE id=$1
			UNION ALL
			SELECT t.id, tree.depth+1 FROM agent_tasks t JOIN tree ON t.parent_task_id = tree.id
			WHERE tree.depth < 20
		)
		UPDATE agent_tasks SET status='cancelled',claimed_by='',claimed_until=NULL,updated_at=now()
		WHERE id IN (SELECT id FROM tree WHERE depth > 0)
		  AND owner_id=$2
		  AND status NOT IN ('completed','cancelled','dead_letter','failed')`, taskID, ownerID)
	if err != nil {
		return 0, err
	}
	return int(descendants.RowsAffected()), nil
}

// RunningRunsForAgent counts in-flight runs, which is what the concurrency
// policy is enforced against.
func (s *Store) RunningRunsForAgent(ctx context.Context, agentID, exceptRunID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE agent_id=$1 AND status='running' AND id <> $2`, agentID, exceptRunID).Scan(&count)
	return count, err
}

// --- Runs ---

func (s *Store) CreateAgentRun(ctx context.Context, run AgentRun) (AgentRun, error) {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	// The rate this run is charged at, as it stands now. Reading it here rather
	// than at billing time is the whole point: a price corrected next month must
	// not rewrite this month's bill, and an endpoint deleted next year must not
	// take this run's cost to zero.
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_runs(id,task_id,agent_id,owner_id,attempt,agent_version,runtime_id,model_endpoint_id,model_name,trace_id,worker_id,resumed_steps,
			input_price_per_mtok,output_price_per_mtok,price_currency)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
		       m.input_price_per_mtok, m.output_price_per_mtok, m.currency
		FROM (SELECT 1) AS one
		LEFT JOIN model_endpoints m ON m.id = $8
		RETURNING `+runColumns,
		run.ID, run.TaskID, run.AgentID, run.OwnerID, run.Attempt, run.AgentVersion, run.RuntimeID, run.ModelEndpointID, run.ModelName, run.TraceID, run.WorkerID, run.ResumedSteps).
		Scan(run.scanTargets()...)
	if err != nil {
		return AgentRun{}, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE agent_tasks SET current_run_id=$2,updated_at=now() WHERE id=$1`, run.TaskID, run.ID)
	return run, nil
}

func (s *Store) FinishAgentRun(ctx context.Context, run AgentRun) error {
	completion := run.Completion
	if len(completion) == 0 {
		completion = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status=$2,step_count=$3,tool_calls=$4,total_tokens=$5,duration_ms=$6,result=$7,failure_reason=$8,completion=$9,runtime_id=COALESCE($10,runtime_id),metering=$11,finished_at=now() WHERE id=$1`,
		run.ID, run.Status, run.StepCount, run.ToolCalls, run.TotalTokens, run.DurationMs, run.Result, run.FailureReason, completion, run.RuntimeID, run.Metering)
	return err
}

const runColumns = `id,task_id,agent_id,owner_id,attempt,status,agent_version,runtime_id,model_endpoint_id,model_name,trace_id,worker_id,resumed_steps,step_count,tool_calls,total_tokens,metering,duration_ms,result,failure_reason,completion,started_at,finished_at`

// scanTargets is where runColumns lands, in that order. The insert reads the
// same columns back and cannot use scanRun, so it goes through here: a column
// added to runColumns and forgotten in that second scan fails every run on the
// queue with "number of field descriptions must equal number of destinations",
// which names neither the column nor the query.
func (r *AgentRun) scanTargets() []any {
	return []any{&r.ID, &r.TaskID, &r.AgentID, &r.OwnerID, &r.Attempt, &r.Status, &r.AgentVersion, &r.RuntimeID,
		&r.ModelEndpointID, &r.ModelName, &r.TraceID, &r.WorkerID, &r.ResumedSteps, &r.StepCount, &r.ToolCalls,
		&r.TotalTokens, &r.Metering, &r.DurationMs, &r.Result, &r.FailureReason, &r.Completion, &r.StartedAt, &r.FinishedAt}
}

func scanRun(row pgx.Row) (AgentRun, error) {
	var item AgentRun
	err := row.Scan(item.scanTargets()...)
	return item, err
}

func (s *Store) AgentRunByID(ctx context.Context, id, ownerID string, admin bool) (AgentRun, error) {
	query := `SELECT ` + runColumns + ` FROM agent_runs WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	item, err := scanRun(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRun{}, ErrNotFound
	}
	return item, err
}

// RunFilter narrows a run listing. Every field is optional; the zero value lists
// the owner's most recent runs, which is what the screen opens on.
type RunFilter struct {
	AgentID  string
	TaskID   string
	Status   string
	Metering string
	// Text matches the trace id, the failure reason or the result, which are the
	// three things somebody has in hand when they arrive looking for one run: an
	// id copied out of a log, or a sentence they remember seeing.
	Text string
	// Since bounds how far back to look. Zero means no bound.
	Since time.Time
	Limit int
	// AllOwners lists across the deployment. Only an administrator may ask.
	AllOwners bool
}

// runListColumns qualifies the run's own columns and adds the agent's name, so a
// listing reads as "which agent" rather than as a page of identifiers.
var runListColumns = prefixColumns("r", runColumns) + ", a.name"

// AgentRuns lists runs, most recent first.
func (s *Store) AgentRuns(ctx context.Context, ownerID string, filter RunFilter) ([]AgentRun, error) {
	filter.Limit = clampLimit(filter.Limit, 50, 200)
	query := `SELECT ` + runListColumns + ` FROM agent_runs r JOIN agent_definitions a ON a.id = r.agent_id WHERE true`
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(clause, len(args))
	}
	if !filter.AllOwners {
		add(` AND r.owner_id=$%d`, ownerID)
	}
	if filter.AgentID != "" {
		add(` AND r.agent_id=$%d`, filter.AgentID)
	}
	if filter.TaskID != "" {
		add(` AND r.task_id=$%d`, filter.TaskID)
	}
	if filter.Status != "" {
		add(` AND r.status=$%d`, filter.Status)
	}
	if filter.Metering != "" {
		// "unmetered" in the console means "nothing of this run's cost is in any
		// total", which is both of the values that mean nobody counted.
		if filter.Metering == "unmetered" {
			query += ` AND r.metering IN ('unmetered','context_only')`
		} else {
			add(` AND r.metering=$%d`, filter.Metering)
		}
	}
	if text := strings.TrimSpace(filter.Text); text != "" {
		args = append(args, "%"+strings.ToLower(text)+"%")
		query += fmt.Sprintf(` AND (lower(r.trace_id) LIKE $%d OR lower(r.failure_reason) LIKE $%d OR lower(r.result) LIKE $%d OR lower(a.name) LIKE $%d)`,
			len(args), len(args), len(args), len(args))
	}
	if !filter.Since.IsZero() {
		add(` AND r.started_at >= $%d`, filter.Since)
	}
	add(` ORDER BY r.started_at DESC LIMIT $%d`, filter.Limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentRun{}
	for rows.Next() {
		var item AgentRun
		if err := rows.Scan(append(item.scanTargets(), &item.AgentName)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Checkpoint is the work a task has already completed and paid for: the outputs
// of the reasoning and delegation steps its earlier attempts finished.
//
// It is scoped to one agent version. A definition that changed since — a new
// system prompt, a different goal — invalidates the reasoning done under the old
// one, and resuming into it would produce an attempt that no instruction ever
// asked for.
type Checkpoint struct {
	// Outputs are the completed step outputs, oldest first.
	Outputs []string `json:"-"`
	// Steps is how many completed steps the task has.
	Steps int `json:"steps"`
	// LastRunID is the run that produced the most recent of them.
	LastRunID string `json:"lastRunId,omitempty"`
}

// TaskCheckpoint reads what a retry of this task may resume from. An empty
// checkpoint is the normal answer for a first attempt and is not an error.
func (s *Store) TaskCheckpoint(ctx context.Context, taskID string, agentVersion int) (Checkpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id, s.output
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		JOIN agent_tasks t ON t.id = r.task_id
		WHERE r.task_id = $1
		  AND r.agent_version = $2
		  AND s.status = 'succeeded'
		  AND s.type IN ('reasoning', 'delegation')
		  AND (t.checkpoint_after IS NULL OR s.created_at > t.checkpoint_after)
		ORDER BY r.started_at, s.sequence`, taskID, agentVersion)
	if err != nil {
		return Checkpoint{}, err
	}
	defer rows.Close()
	var checkpoint Checkpoint
	for rows.Next() {
		var runID, output string
		if err := rows.Scan(&runID, &output); err != nil {
			return Checkpoint{}, err
		}
		checkpoint.Outputs = append(checkpoint.Outputs, output)
		checkpoint.LastRunID = runID
	}
	checkpoint.Steps = len(checkpoint.Outputs)
	return checkpoint, rows.Err()
}

// --- Steps, events, artifacts ---

func (s *Store) AppendRunStep(ctx context.Context, step AgentRunStep) (AgentRunStep, error) {
	if step.ID == "" {
		step.ID = uuid.NewString()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_run_steps(id,run_id,sequence,type,title,input,output,status,error,prompt_tokens,completion_tokens,duration_ms)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING created_at`,
		step.ID, step.RunID, step.Sequence, step.Type, step.Title, step.Input, step.Output, step.Status, step.Error, step.PromptTokens, step.CompletionTokens, step.DurationMs).Scan(&step.CreatedAt)
	return step, err
}

func (s *Store) RunSteps(ctx context.Context, runID string) ([]AgentRunStep, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,run_id,sequence,type,title,input,output,status,error,prompt_tokens,completion_tokens,duration_ms,created_at FROM agent_run_steps WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentRunStep{}
	for rows.Next() {
		var item AgentRunStep
		if err := rows.Scan(&item.ID, &item.RunID, &item.Sequence, &item.Type, &item.Title, &item.Input, &item.Output, &item.Status, &item.Error, &item.PromptTokens, &item.CompletionTokens, &item.DurationMs, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AppendRunEvent records one point on the run timeline. Timeline writes must
// never fail a run, so callers ignore the error and the worker logs it instead.
func (s *Store) AppendRunEvent(ctx context.Context, runID, taskID, eventType, message string, details any) error {
	payload := json.RawMessage(`{}`)
	if details != nil {
		if encoded, err := json.Marshal(details); err == nil {
			payload = encoded
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_run_events(run_id,task_id,type,message,details) VALUES($1,$2,$3,$4,$5)`, runID, taskID, eventType, message, payload)
	return err
}

func (s *Store) RunEvents(ctx context.Context, runID string) ([]AgentRunEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,run_id,task_id,type,message,details,occurred_at FROM agent_run_events WHERE run_id=$1 ORDER BY occurred_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentRunEvent{}
	for rows.Next() {
		var item AgentRunEvent
		if err := rows.Scan(&item.ID, &item.RunID, &item.TaskID, &item.Type, &item.Message, &item.Details, &item.OccurredAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// maxInlineArtifactBytes bounds what is stored directly in PostgreSQL. Anything
// larger belongs in an object store; the column keeps the reference.
const maxInlineArtifactBytes = 256 * 1024

func (s *Store) CreateArtifact(ctx context.Context, item AgentArtifact) (AgentArtifact, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	item.SizeBytes = int64(len(item.Content))
	if item.SizeBytes > maxInlineArtifactBytes {
		return AgentArtifact{}, fmt.Errorf("artifact %q is %d bytes, which exceeds the %d byte inline limit", item.Name, item.SizeBytes, maxInlineArtifactBytes)
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_artifacts(id,run_id,task_id,agent_id,owner_id,name,type,content_type,size_bytes,content,storage_ref)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING created_at`,
		item.ID, item.RunID, item.TaskID, item.AgentID, item.OwnerID, item.Name, item.Type, item.ContentType, item.SizeBytes, item.Content, item.StorageRef).Scan(&item.CreatedAt)
	return item, err
}

// Artifacts lists metadata only; content is fetched per artifact so a listing
// never drags every report through memory.
func (s *Store) Artifacts(ctx context.Context, ownerID, runID string, limit int) ([]AgentArtifact, error) {
	limit = clampLimit(limit, 100, 200)
	query := `SELECT id,run_id,task_id,agent_id,owner_id,name,type,content_type,size_bytes,storage_ref,created_at FROM agent_artifacts WHERE owner_id=$1`
	args := []any{ownerID}
	if runID != "" {
		args = append(args, runID)
		query += fmt.Sprintf(` AND run_id=$%d`, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentArtifact{}
	for rows.Next() {
		var item AgentArtifact
		if err := rows.Scan(&item.ID, &item.RunID, &item.TaskID, &item.AgentID, &item.OwnerID, &item.Name, &item.Type, &item.ContentType, &item.SizeBytes, &item.StorageRef, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ArtifactByID(ctx context.Context, id, ownerID string) (AgentArtifact, error) {
	var item AgentArtifact
	err := s.pool.QueryRow(ctx, `SELECT id,run_id,task_id,agent_id,owner_id,name,type,content_type,size_bytes,content,storage_ref,created_at FROM agent_artifacts WHERE id=$1 AND owner_id=$2`, id, ownerID).
		Scan(&item.ID, &item.RunID, &item.TaskID, &item.AgentID, &item.OwnerID, &item.Name, &item.Type, &item.ContentType, &item.SizeBytes, &item.Content, &item.StorageRef, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentArtifact{}, ErrNotFound
	}
	return item, err
}

// --- Triggers ---

const triggerColumns = `id,agent_id,owner_id,name,type,enabled,schedule,timezone,task_title,task_input,priority,last_fired_at,next_fire_at,webhook_secret <> '',event_type,event_filter,created_at,updated_at`

func scanTrigger(row pgx.Row) (AgentTrigger, error) {
	var item AgentTrigger
	err := row.Scan(&item.ID, &item.AgentID, &item.OwnerID, &item.Name, &item.Type, &item.Enabled, &item.Schedule, &item.Timezone, &item.TaskTitle, &item.TaskInput, &item.Priority, &item.LastFiredAt, &item.NextFireAt, &item.HasSecret, &item.EventType, &item.EventFilter, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

// scanTriggers reads a whole result set of triggers.
func scanTriggers(rows pgx.Rows) ([]AgentTrigger, error) {
	items := []AgentTrigger{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// PutAgentTrigger creates or updates a trigger. An empty secret leaves any
// existing one in place so editing a schedule does not silently unprotect a
// webhook.
func (s *Store) PutAgentTrigger(ctx context.Context, item AgentTrigger, secret string) (AgentTrigger, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Timezone == "" {
		item.Timezone = "Asia/Seoul"
	}
	if item.Priority == "" {
		item.Priority = "normal"
	}
	if len(item.EventFilter) == 0 {
		item.EventFilter = json.RawMessage(`{}`)
	}
	var encrypted any
	if strings.TrimSpace(secret) != "" {
		value, err := s.cipher.Encrypt([]byte(secret), "trigger-secret:"+item.ID)
		if err != nil {
			return AgentTrigger{}, err
		}
		encrypted = value
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO agent_triggers(id,agent_id,owner_id,name,type,enabled,schedule,timezone,webhook_secret,task_title,task_input,priority,next_fire_at,event_type,event_filter)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9,''),$10,$11,$12,$13,$14,$15)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,enabled=excluded.enabled,schedule=excluded.schedule,timezone=excluded.timezone,
			webhook_secret=COALESCE($9,agent_triggers.webhook_secret),task_title=excluded.task_title,task_input=excluded.task_input,priority=excluded.priority,
			next_fire_at=excluded.next_fire_at,event_type=excluded.event_type,event_filter=excluded.event_filter,updated_at=now()
		RETURNING `+triggerColumns,
		item.ID, item.AgentID, item.OwnerID, item.Name, item.Type, item.Enabled, item.Schedule, item.Timezone, encrypted, item.TaskTitle, item.TaskInput, item.Priority, item.NextFireAt, item.EventType, item.EventFilter)
	return scanTrigger(row)
}

func (s *Store) AgentTriggers(ctx context.Context, ownerID, agentID string) ([]AgentTrigger, error) {
	query := `SELECT ` + triggerColumns + ` FROM agent_triggers WHERE owner_id=$1`
	args := []any{ownerID}
	if agentID != "" {
		args = append(args, agentID)
		query += fmt.Sprintf(` AND agent_id=$%d`, len(args))
	}
	query += ` ORDER BY name`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

func (s *Store) AgentTriggerByID(ctx context.Context, id string) (AgentTrigger, error) {
	item, err := scanTrigger(s.pool.QueryRow(ctx, `SELECT `+triggerColumns+` FROM agent_triggers WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTrigger{}, ErrNotFound
	}
	return item, err
}

// TriggerSecret returns the plaintext webhook secret for signature verification.
func (s *Store) TriggerSecret(ctx context.Context, id string) (string, error) {
	var encrypted string
	if err := s.pool.QueryRow(ctx, `SELECT webhook_secret FROM agent_triggers WHERE id=$1`, id).Scan(&encrypted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if encrypted == "" {
		return "", nil
	}
	plain, err := s.cipher.Decrypt(encrypted, "trigger-secret:"+id)
	return string(plain), err
}

func (s *Store) DeleteAgentTrigger(ctx context.Context, id, ownerID string) error {
	return s.deleteScoped(ctx, "agent_triggers", id, ownerID, false, "trigger")
}

// DueCronTriggers claims schedules that are ready to fire. next_fire_at is moved
// forward by the caller, so a trigger cannot be claimed twice by two workers.
func (s *Store) DueCronTriggers(ctx context.Context, limit int) ([]AgentTrigger, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+triggerColumns+` FROM agent_triggers
		WHERE enabled AND type='cron' AND next_fire_at IS NOT NULL AND next_fire_at <= now()
		ORDER BY next_fire_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentTrigger{}
	for rows.Next() {
		item, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// MarkTriggerFired advances the schedule. Returning the row count lets the
// caller tell whether it won the race against another worker.
// ClaimWebhookDelivery records a webhook delivery and reports whether it is the
// first time this exact request has arrived.
//
// A signature proves the sender knew the secret. It does not prove this is the
// first time the request has been sent — the signature is a function of the body,
// so a captured request stays valid forever, and every replay of it queued another
// task. Anybody who saw one request could fire that agent again whenever they
// liked, indefinitely, and the audit trail would show a perfectly valid webhook.
//
// A body carrying anything unique — a delivery id, a timestamp, an event id, which
// most senders include — signs differently every time and is unaffected. An
// identical body arriving twice is either a replay or a sender retrying after a
// timeout, and refusing the second one is what both cases want.
func (s *Store) ClaimWebhookDelivery(ctx context.Context, triggerID, signature string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO webhook_deliveries(trigger_id,signature) VALUES($1,$2)
		ON CONFLICT (trigger_id,signature) DO NOTHING`, triggerID, signature)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordWebhookTask links a claimed delivery to the task it created, so an
// operator asking "did that webhook do anything" has an answer.
func (s *Store) RecordWebhookTask(ctx context.Context, triggerID, signature, taskID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhook_deliveries SET task_id=$3 WHERE trigger_id=$1 AND signature=$2`, triggerID, signature, taskID)
	return err
}

// SweepWebhookDeliveries drops delivery records past the replay window.
//
// Like an expired session, this is not history somebody might want: past the
// window the record cannot refuse anything, and keeping it only grows a table
// every webhook writes to. It is swept whether or not retention is configured.
func (s *Store) SweepWebhookDeliveries(ctx context.Context, window time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE created_at < now() - $1::interval`, window.String())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) MarkTriggerFired(ctx context.Context, id string, next *time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_triggers SET last_fired_at=now(),next_fire_at=$2,updated_at=now() WHERE id=$1 AND (next_fire_at IS NULL OR next_fire_at <= now())`, id, next)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetTriggerNextFire schedules the first fire after a trigger is saved.
func (s *Store) SetTriggerNextFire(ctx context.Context, id string, next *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_triggers SET next_fire_at=$2,updated_at=now() WHERE id=$1`, id, next)
	return err
}

// ExecutionSchemaReady reports whether the execution plane's tables exist. The
// API process owns migrations, so a worker started alongside a fresh install can
// come up first; it waits on this instead of logging failures until the schema
// catches up.
func (s *Store) ExecutionSchemaReady(ctx context.Context) (bool, error) {
	var ready bool
	err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.agent_tasks') IS NOT NULL AND to_regclass('public.agent_triggers') IS NOT NULL`).Scan(&ready)
	return ready, err
}

// TaskQueueDepth reports what the queue looks like right now: tasks ready to be
// claimed, and tasks a worker is already running.
//
// It is the signal the workers scale on, and the one an operator reads to see
// whether the plane is keeping up. Scheduled-for-later tasks are deliberately
// not counted as depth: they are not waiting, they are not due.
func (s *Store) TaskQueueDepth(ctx context.Context) (ready int, running int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status IN ('queued','retrying') AND scheduled_at <= now() AND (claimed_until IS NULL OR claimed_until < now())),
		count(*) FILTER (WHERE status IN ('planning','ready','running','waiting_tool'))
		FROM agent_tasks`).Scan(&ready, &running)
	return ready, running, err
}

// QueueSnapshot is the queue as the console shows it.
type QueueSnapshot struct {
	Ready   int            `json:"ready"`
	Running int            `json:"running"`
	Workers int            `json:"workers"`
	Status  map[string]int `json:"status"`
}

// Queue reports the depth plus a breakdown, scoped to one owner unless the
// caller is an admin looking at the whole plane.
func (s *Store) Queue(ctx context.Context, ownerID string) (QueueSnapshot, error) {
	snapshot := QueueSnapshot{Status: map[string]int{}}
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM agent_tasks
		WHERE ($1 = '' OR owner_id = $1) GROUP BY status`, ownerID)
	if err != nil {
		return QueueSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return QueueSnapshot{}, err
		}
		snapshot.Status[status] = count
	}
	if err := rows.Err(); err != nil {
		return QueueSnapshot{}, err
	}
	// Depth and worker count describe the plane, not one owner's slice of it:
	// they are what explains how fast anyone's tasks are moving.
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status IN ('queued','retrying') AND scheduled_at <= now() AND (claimed_until IS NULL OR claimed_until < now())),
		count(*) FILTER (WHERE status IN ('planning','ready','running','waiting_tool')),
		count(DISTINCT claimed_by) FILTER (WHERE claimed_by <> '' AND claimed_until > now())
		FROM agent_tasks`).Scan(&snapshot.Ready, &snapshot.Running, &snapshot.Workers); err != nil {
		return QueueSnapshot{}, err
	}
	return snapshot, nil
}

// cleanToolPolicy trims and drops empties, so a textarea that ended in a newline
// does not store a pattern that matches every title.
func cleanToolPolicy(policy ACPToolPolicy) ACPToolPolicy {
	return ACPToolPolicy{Deny: cleanPatterns(policy.Deny), Allow: cleanPatterns(policy.Allow)}
}

func cleanPatterns(values []string) []string {
	cleaned := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[strings.ToLower(trimmed)] {
			continue
		}
		seen[strings.ToLower(trimmed)] = true
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}
