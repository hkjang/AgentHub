package store

import (
	"context"
	"time"
)

// The administrator's view of the whole deployment.
//
// Everything an operator needed was already in the database, but only as a
// per-user slice of it: a usage report for the person looking, a queue depth for
// their own tasks, a log console with no numbers attached. Answering "is this
// platform healthy, who is spending what, and what is stuck" meant opening five
// screens and adding up rows by eye — which nobody does at 2am, so nobody knew
// until something failed.
//
// These queries are read-only aggregates over the tables the execution plane
// already writes. They are deliberately not a new meter to keep in sync: every
// figure here is derived from the same rows the detail screens show.

// PlatformUsers counts who is on the deployment.
type PlatformUsers struct {
	Total int `json:"total"`
	// Active is how many signed in during the window, which is the only
	// membership figure that says anything about use.
	Active    int `json:"active"`
	Admins    int `json:"admins"`
	Managers  int `json:"managers"`
	Disabled  int `json:"disabled"`
	NeverUsed int `json:"neverUsed"`
}

// PlatformAgents counts definitions and the runtimes behind them.
type PlatformAgents struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	Failed   int `json:"failed"`
	Warm     int `json:"warm"`
	Autonomy int `json:"autonomy"`
	// Gated agents require a promoted definition; Unpromoted are the ones whose
	// live definition is not the promoted one, which is where work is piling up
	// rather than running.
	Gated      int `json:"gated"`
	Unpromoted int `json:"unpromoted"`
}

// ExecutionHealth is what the execution plane did in the window.
type ExecutionHealth struct {
	Tasks map[string]int `json:"tasks"`
	Runs  int            `json:"runs"`
	// Completed and Failed are the finished outcomes the success rate is built
	// from; a task still running is neither.
	Completed   int     `json:"completed"`
	Failed      int     `json:"failed"`
	DeadLetter  int     `json:"deadLetter"`
	Blocked     int     `json:"blocked"`
	Retried     int     `json:"retried"`
	SuccessRate float64 `json:"successRate"`
	// Durations are of finished runs, in milliseconds. The median says what a run
	// usually costs; p95 says what the slow ones cost, which is the number that
	// decides whether a schedule still fits in its window.
	MedianDurationMs int64 `json:"medianDurationMs"`
	P95DurationMs    int64 `json:"p95DurationMs"`
}

// EventBacklog is the delivery outbox, which is invisible until it is a problem.
type EventBacklog struct {
	Pending    int `json:"pending"`
	Retrying   int `json:"retrying"`
	DeadLetter int `json:"deadLetter"`
	Delivered  int `json:"delivered"`
	// OldestPendingSeconds is how long the oldest undelivered event has waited.
	OldestPendingSeconds int64 `json:"oldestPendingSeconds"`
}

// SpendRow is one line of a spend breakdown — by user, by agent or by model.
type SpendRow struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Runs         int     `json:"runs"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Cost         float64 `json:"cost"`
	// Priced is false when no endpoint involved has a price, so a zero is not
	// mistaken for free.
	Priced bool `json:"priced"`
}

// PlatformSpend is the bill, and where it came from.
type PlatformSpend struct {
	Currency       string       `json:"currency"`
	InputTokens    int64        `json:"inputTokens"`
	OutputTokens   int64        `json:"outputTokens"`
	Cost           float64      `json:"cost"`
	UnpricedTokens int64        `json:"unpricedTokens"`
	Users          []SpendRow   `json:"users"`
	Agents         []SpendRow   `json:"agents"`
	Models         []SpendRow   `json:"models"`
	Daily          []UsagePoint `json:"daily"`
}

// PlatformOverview is the whole picture for one window.
type PlatformOverview struct {
	From      time.Time       `json:"from"`
	To        time.Time       `json:"to"`
	Users     PlatformUsers   `json:"users"`
	Agents    PlatformAgents  `json:"agents"`
	Execution ExecutionHealth `json:"execution"`
	Queue     QueueSnapshot   `json:"queue"`
	Events    EventBacklog    `json:"events"`
	Spend     PlatformSpend   `json:"spend"`
	// OldestQueuedSeconds is how long the oldest runnable task has been waiting.
	// A queue depth alone does not distinguish a busy minute from a stopped
	// worker; this does.
	OldestQueuedSeconds int64 `json:"oldestQueuedSeconds"`
}

// PlatformOverview aggregates the deployment over one window.
func (s *Store) PlatformOverview(ctx context.Context, from, to time.Time) (PlatformOverview, error) {
	overview := PlatformOverview{From: from, To: to}

	if err := s.pool.QueryRow(ctx, `SELECT
		count(*),
		count(*) FILTER (WHERE last_login_at >= $1),
		count(*) FILTER (WHERE role = 'admin'),
		count(*) FILTER (WHERE role = 'manager'),
		count(*) FILTER (WHERE status = 'disabled'),
		count(*) FILTER (WHERE last_login_at IS NULL)
		FROM users`, from).Scan(&overview.Users.Total, &overview.Users.Active,
		&overview.Users.Admins, &overview.Users.Managers, &overview.Users.Disabled, &overview.Users.NeverUsed); err != nil {
		return PlatformOverview{}, err
	}

	if err := s.pool.QueryRow(ctx, `SELECT
		count(*),
		count(*) FILTER (WHERE r.status IN ('running','ready')),
		count(*) FILTER (WHERE r.id IS NULL OR r.status = 'stopped'),
		count(*) FILTER (WHERE r.status IN ('failed','crashed')),
		count(*) FILTER (WHERE r.warm_until IS NOT NULL AND r.warm_until > now()),
		count(*) FILTER (WHERE d.execution_mode <> 'manual'),
		count(*) FILTER (WHERE d.require_promotion),
		count(*) FILTER (WHERE d.require_promotion AND d.promoted_version IS DISTINCT FROM d.version)
		FROM agent_definitions d
		LEFT JOIN agent_runtimes r ON r.agent_id = d.id`).Scan(
		&overview.Agents.Total, &overview.Agents.Running, &overview.Agents.Stopped, &overview.Agents.Failed,
		&overview.Agents.Warm, &overview.Agents.Autonomy, &overview.Agents.Gated, &overview.Agents.Unpromoted); err != nil {
		return PlatformOverview{}, err
	}

	execution, err := s.executionHealth(ctx, from, to)
	if err != nil {
		return PlatformOverview{}, err
	}
	overview.Execution = execution

	queue, err := s.Queue(ctx, "")
	if err != nil {
		return PlatformOverview{}, err
	}
	overview.Queue = queue

	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(scheduled_at))::bigint, 0)
		FROM agent_tasks WHERE status IN ('queued','retrying') AND scheduled_at <= now()`).
		Scan(&overview.OldestQueuedSeconds); err != nil {
		return PlatformOverview{}, err
	}

	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL AND attempts = 0),
		count(*) FILTER (WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL AND attempts > 0),
		count(*) FILTER (WHERE dead_lettered_at IS NOT NULL),
		count(*) FILTER (WHERE dispatched_at >= $1),
		COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL))::bigint, 0)
		FROM platform_events`, from).Scan(&overview.Events.Pending, &overview.Events.Retrying,
		&overview.Events.DeadLetter, &overview.Events.Delivered, &overview.Events.OldestPendingSeconds); err != nil {
		return PlatformOverview{}, err
	}

	spend, err := s.PlatformSpend(ctx, from, to, 8)
	if err != nil {
		return PlatformOverview{}, err
	}
	overview.Spend = spend
	return overview, nil
}

// executionHealth counts what ran and how it ended.
func (s *Store) executionHealth(ctx context.Context, from, to time.Time) (ExecutionHealth, error) {
	health := ExecutionHealth{Tasks: map[string]int{}}
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM agent_tasks
		WHERE created_at >= $1 AND created_at < $2 GROUP BY status`, from, to)
	if err != nil {
		return ExecutionHealth{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return ExecutionHealth{}, err
		}
		health.Tasks[status] = count
	}
	if err := rows.Err(); err != nil {
		return ExecutionHealth{}, err
	}
	health.Completed = health.Tasks[TaskCompleted]
	health.Failed = health.Tasks[TaskFailed]
	health.DeadLetter = health.Tasks[TaskDeadLetter]
	health.Blocked = health.Tasks[TaskBlocked]
	finished := health.Completed + health.Failed + health.DeadLetter
	if finished > 0 {
		health.SuccessRate = float64(health.Completed) / float64(finished) * 100
	}

	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE attempts > 1)
		FROM agent_tasks WHERE created_at >= $1 AND created_at < $2`, from, to).Scan(&health.Retried); err != nil {
		return ExecutionHealth{}, err
	}

	// Percentiles come from finished runs only: a run still going has no duration
	// yet, and counting it as zero would flatter every figure here.
	if err := s.pool.QueryRow(ctx, `SELECT count(*),
		COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE finished_at IS NOT NULL), 0)::bigint,
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE finished_at IS NOT NULL), 0)::bigint
		FROM agent_runs WHERE created_at >= $1 AND created_at < $2`, from, to).
		Scan(&health.Runs, &health.MedianDurationMs, &health.P95DurationMs); err != nil {
		return ExecutionHealth{}, err
	}
	return health, nil
}

// PlatformSpend breaks the bill down by user, by agent and by model.
//
// limit bounds each breakdown: an operator reads the top of these lists and
// exports the rest, and an unbounded list on a deployment with a thousand agents
// is a page nobody can use.
func (s *Store) PlatformSpend(ctx context.Context, from, to time.Time, limit int) (PlatformSpend, error) {
	if limit <= 0 || limit > 200 {
		limit = 8
	}
	spend := PlatformSpend{Currency: "KRW", Users: []SpendRow{}, Agents: []SpendRow{}, Models: []SpendRow{}, Daily: []UsagePoint{}}

	byUser := `SELECT u.id, COALESCE(NULLIF(u.display_name, ''), u.username)`
	byAgent := `SELECT a.id, a.name`
	byModel := `SELECT COALESCE(NULLIF(r.model_name, ''), COALESCE(m.default_model, '(모델 미기록)')),
	                   COALESCE(NULLIF(r.model_name, ''), COALESCE(m.default_model, '(모델 미기록)'))`
	tail := `, count(DISTINCT r.id), COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		       COALESCE(sum(` + usageCostSQL + `), 0),
		       bool_or(COALESCE(m.input_price_per_mtok, 0) > 0 OR COALESCE(m.output_price_per_mtok, 0) > 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id `

	for _, breakdown := range []struct {
		head   string
		join   string
		group  string
		target *[]SpendRow
	}{
		{head: byUser, join: `JOIN users u ON u.id = r.owner_id `, group: `GROUP BY u.id, u.display_name, u.username`, target: &spend.Users},
		{head: byAgent, join: `JOIN agent_definitions a ON a.id = r.agent_id `, group: `GROUP BY a.id, a.name`, target: &spend.Agents},
		{head: byModel, join: ``, group: `GROUP BY 1`, target: &spend.Models},
	} {
		query := breakdown.head + tail + breakdown.join +
			`WHERE s.created_at >= $1 AND s.created_at < $2 ` + breakdown.group +
			` ORDER BY 6 DESC, 4 DESC LIMIT $3`
		rows, err := s.pool.Query(ctx, query, from, to, limit)
		if err != nil {
			return PlatformSpend{}, err
		}
		for rows.Next() {
			var row SpendRow
			var priced *bool
			if err := rows.Scan(&row.ID, &row.Name, &row.Runs, &row.InputTokens, &row.OutputTokens, &row.Cost, &priced); err != nil {
				rows.Close()
				return PlatformSpend{}, err
			}
			row.Priced = priced != nil && *priced
			*breakdown.target = append(*breakdown.target, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return PlatformSpend{}, err
		}
	}

	// Totals are computed over every row rather than summed from the truncated
	// breakdowns, which would understate the bill by exactly the tail.
	if err := s.pool.QueryRow(ctx, `SELECT
		COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		COALESCE(sum(`+usageCostSQL+`), 0),
		COALESCE(sum(CASE WHEN COALESCE(m.input_price_per_mtok, 0) = 0 AND COALESCE(m.output_price_per_mtok, 0) = 0
			THEN s.prompt_tokens + s.completion_tokens ELSE 0 END), 0),
		COALESCE(max(m.currency), 'KRW')
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE s.created_at >= $1 AND s.created_at < $2`, from, to).
		Scan(&spend.InputTokens, &spend.OutputTokens, &spend.Cost, &spend.UnpricedTokens, &spend.Currency); err != nil {
		return PlatformSpend{}, err
	}
	if spend.Currency == "" {
		spend.Currency = "KRW"
	}

	daily, err := s.pool.Query(ctx, `SELECT date_trunc('day', s.created_at) AS day,
		       COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		       COALESCE(sum(`+usageCostSQL+`), 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE s.created_at >= $1 AND s.created_at < $2
		GROUP BY day ORDER BY day`, from, to)
	if err != nil {
		return PlatformSpend{}, err
	}
	defer daily.Close()
	for daily.Next() {
		var point UsagePoint
		if err := daily.Scan(&point.Day, &point.InputTokens, &point.OutputTokens, &point.Cost); err != nil {
			return PlatformSpend{}, err
		}
		spend.Daily = append(spend.Daily, point)
	}
	return spend, daily.Err()
}

// UserActivity is what one account has actually been doing, which is the part of
// user management the platform could never answer: the list showed a role and a
// last login, so "who is this account for" and "is it still used" were guesses.
type UserActivity struct {
	UserID string `json:"userId"`
	Agents int    `json:"agents"`
	// Tasks and Running describe work; Tokens and Cost describe spend. Both are
	// over the reported window except Running, which is now.
	Tasks        int     `json:"tasks"`
	Failed       int     `json:"failed"`
	Running      int     `json:"running"`
	Runs         int     `json:"runs"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Cost         float64 `json:"cost"`
}

// UserActivitySummary keys activity by user id, for a list that already has the
// accounts and needs the numbers beside them.
func (s *Store) UserActivitySummary(ctx context.Context, from, to time.Time) (map[string]UserActivity, error) {
	activity := map[string]UserActivity{}
	at := func(id string) UserActivity {
		item, ok := activity[id]
		if !ok {
			item = UserActivity{UserID: id}
		}
		return item
	}

	agents, err := s.pool.Query(ctx, `SELECT owner_id, count(*) FROM agent_definitions GROUP BY owner_id`)
	if err != nil {
		return nil, err
	}
	defer agents.Close()
	for agents.Next() {
		var id string
		var count int
		if err := agents.Scan(&id, &count); err != nil {
			return nil, err
		}
		item := at(id)
		item.Agents = count
		activity[id] = item
	}
	if err := agents.Err(); err != nil {
		return nil, err
	}

	tasks, err := s.pool.Query(ctx, `SELECT owner_id,
		count(*) FILTER (WHERE created_at >= $1 AND created_at < $2),
		count(*) FILTER (WHERE created_at >= $1 AND created_at < $2 AND status IN ('failed','dead_letter')),
		count(*) FILTER (WHERE status IN ('planning','ready','running','waiting_tool'))
		FROM agent_tasks GROUP BY owner_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer tasks.Close()
	for tasks.Next() {
		var id string
		var total, failed, running int
		if err := tasks.Scan(&id, &total, &failed, &running); err != nil {
			return nil, err
		}
		item := at(id)
		item.Tasks, item.Failed, item.Running = total, failed, running
		activity[id] = item
	}
	if err := tasks.Err(); err != nil {
		return nil, err
	}

	spend, err := s.pool.Query(ctx, `SELECT r.owner_id, count(DISTINCT r.id),
		COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		COALESCE(sum(`+usageCostSQL+`), 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE s.created_at >= $1 AND s.created_at < $2
		GROUP BY r.owner_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer spend.Close()
	for spend.Next() {
		var id string
		var runs int
		var input, output int64
		var cost float64
		if err := spend.Scan(&id, &runs, &input, &output, &cost); err != nil {
			return nil, err
		}
		item := at(id)
		item.Runs, item.InputTokens, item.OutputTokens, item.Cost = runs, input, output, cost
		activity[id] = item
	}
	return activity, spend.Err()
}
