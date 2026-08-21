package store

import (
	"context"
	"time"
)

// Token spend reporting.
//
// The execution plane runs agents when nobody is watching, which is exactly when
// a runaway loop costs money quietly. Every step already records its prompt and
// completion tokens, so the cost is a join away rather than a new meter to keep
// in sync with reality.

// UsageRow is one agent's spend over the reported window.
type UsageRow struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	ModelName string `json:"modelName"`
	Currency  string `json:"currency"`
	Runs      int    `json:"runs"`
	Steps     int    `json:"steps"`
	// InputTokens and OutputTokens are counted separately because they are
	// priced separately.
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Cost         float64 `json:"cost"`
	// Priced is false when the model endpoint has no price set, so the console
	// can say "not priced" instead of showing a confident zero.
	Priced bool `json:"priced"`
}

// UsagePoint is one day of spend across everything in scope.
type UsagePoint struct {
	Day          time.Time `json:"day"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	Cost         float64   `json:"cost"`
}

// UsageReport is what the console renders.
type UsageReport struct {
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Currency     string    `json:"currency"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	Cost         float64   `json:"cost"`
	// Unpriced counts the tokens spent on endpoints with no price configured;
	// reporting them separately keeps the total honest.
	UnpricedTokens int64 `json:"unpricedTokens"`
	// Runs and UnmeteredRuns say how much of the window this report actually
	// covers. A total is not evidence unless it says what it could not see: an
	// agent that spends in its own process and reports nothing leaves a run whose
	// real cost is absent from every number above.
	Runs          int          `json:"runs"`
	UnmeteredRuns int          `json:"unmeteredRuns"`
	Agents        []UsageRow   `json:"agents"`
	Daily         []UsagePoint `json:"daily"`
}

// usageCostSQL prices one row. Prices are per million tokens.
const usageCostSQL = `(s.prompt_tokens * COALESCE(m.input_price_per_mtok, 0) + s.completion_tokens * COALESCE(m.output_price_per_mtok, 0)) / 1000000.0`

// Usage totals token spend for one owner over a window.
//
// Admins may look across every owner by passing an empty ownerID, which is how
// the platform bill is reconciled; a user only ever sees their own agents.
func (s *Store) Usage(ctx context.Context, ownerID, agentID string, from, to time.Time) (UsageReport, error) {
	report := UsageReport{From: from, To: to, Currency: "KRW", Agents: []UsageRow{}, Daily: []UsagePoint{}}

	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.name, COALESCE(NULLIF(r.model_name, ''), COALESCE(m.default_model, '')),
		       COALESCE(m.currency, 'KRW'),
		       count(DISTINCT r.id), count(s.id),
		       COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		       COALESCE(sum(`+usageCostSQL+`), 0),
		       bool_or(COALESCE(m.input_price_per_mtok, 0) > 0 OR COALESCE(m.output_price_per_mtok, 0) > 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		JOIN agent_definitions a ON a.id = r.agent_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE s.created_at >= $1 AND s.created_at < $2
		  AND ($3 = '' OR r.owner_id = $3)
		  AND ($4 = '' OR r.agent_id = $4)
		GROUP BY a.id, a.name, COALESCE(NULLIF(r.model_name, ''), COALESCE(m.default_model, '')), COALESCE(m.currency, 'KRW')
		ORDER BY 9 DESC, 7 DESC`, from, to, ownerID, agentID)
	if err != nil {
		return UsageReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row UsageRow
		var priced *bool
		if err := rows.Scan(&row.AgentID, &row.AgentName, &row.ModelName, &row.Currency,
			&row.Runs, &row.Steps, &row.InputTokens, &row.OutputTokens, &row.Cost, &priced); err != nil {
			return UsageReport{}, err
		}
		row.Priced = priced != nil && *priced
		report.InputTokens += row.InputTokens
		report.OutputTokens += row.OutputTokens
		report.Cost += row.Cost
		if !row.Priced {
			report.UnpricedTokens += row.InputTokens + row.OutputTokens
		}
		if row.Currency != "" {
			report.Currency = row.Currency
		}
		report.Agents = append(report.Agents, row)
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, err
	}

	// Counted from the runs themselves rather than from their steps: a run that
	// reported nothing has nothing to join to, which is exactly the run this
	// number exists to make visible.
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE r.metering IN ('unmetered', 'context_only'))
		FROM agent_runs r
		WHERE r.started_at >= $1 AND r.started_at < $2
		  AND ($3 = '' OR r.owner_id = $3)
		  AND ($4 = '' OR r.agent_id = $4)`, from, to, ownerID, agentID).
		Scan(&report.Runs, &report.UnmeteredRuns); err != nil {
		return UsageReport{}, err
	}

	daily, err := s.pool.Query(ctx, `
		SELECT date_trunc('day', s.created_at) AS day,
		       COALESCE(sum(s.prompt_tokens), 0), COALESCE(sum(s.completion_tokens), 0),
		       COALESCE(sum(`+usageCostSQL+`), 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE s.created_at >= $1 AND s.created_at < $2
		  AND ($3 = '' OR r.owner_id = $3)
		  AND ($4 = '' OR r.agent_id = $4)
		GROUP BY day ORDER BY day`, from, to, ownerID, agentID)
	if err != nil {
		return UsageReport{}, err
	}
	defer daily.Close()
	for daily.Next() {
		var point UsagePoint
		if err := daily.Scan(&point.Day, &point.InputTokens, &point.OutputTokens, &point.Cost); err != nil {
			return UsageReport{}, err
		}
		report.Daily = append(report.Daily, point)
	}
	return report, daily.Err()
}
