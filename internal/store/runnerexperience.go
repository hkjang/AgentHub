package store

import (
	"context"
	"time"
)

// What this deployment has actually run, by way of running it.
//
// Nine execution backends are offered and every one looks equally available,
// which is the same shape the runtime types had: true only where every one has
// been used. On the deployment this was written against, eight of the nine had
// never produced a single step.
//
// A run does not record which backend produced it — it records the steps that
// backend wrote, and their type is the backend's fingerprint. So this counts
// steps rather than asking runs what they were, which also means a backend
// cannot claim experience it did not write down.

// RunnerExperience is what one way of running has done here.
type RunnerExperience struct {
	Runner string `json:"runner"`
	// Runs counts the runs that carry a step this backend writes.
	Runs int `json:"runs"`
	// Completed counts those that finished successfully. The difference between
	// the two is what makes this worth reading: a backend used ten times and
	// never completing is not the same as one used ten times and always working.
	Completed int        `json:"completed"`
	LastAt    *time.Time `json:"lastAt,omitempty"`
	// LastFailure is what the most recent unsuccessful step said, so a backend
	// that keeps failing says why rather than only how often.
	LastFailure string `json:"lastFailure,omitempty"`
}

// RunnerExperiences reports what each backend has done here, keyed by runner.
//
// Backends with no history are absent rather than present with zeros: an absent
// entry means never used, which is different from used-and-nothing-happened.
func (s *Store) RunnerExperiences(ctx context.Context) (map[string]RunnerExperience, error) {
	// The step types this platform knows how to attribute, and the backend each
	// belongs to. Built from the same function the rest of the platform uses, so
	// a backend added without a step type is absent here too rather than
	// silently attributed to something else.
	byStep := map[string]string{}
	for _, runner := range Runners {
		if step := RunnerStepType(runner); step != "" {
			byStep[step] = runner
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT s.type,
		       count(DISTINCT s.run_id) AS runs,
		       count(DISTINCT s.run_id) FILTER (WHERE r.status = 'completed') AS completed,
		       max(r.created_at) AS last_at
		  FROM agent_run_steps s
		  JOIN agent_runs r ON r.id = s.run_id
		 GROUP BY s.type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	experiences := map[string]RunnerExperience{}
	for rows.Next() {
		var stepType string
		var item RunnerExperience
		var lastAt *time.Time
		if err := rows.Scan(&stepType, &item.Runs, &item.Completed, &lastAt); err != nil {
			return nil, err
		}
		runner, known := byStep[stepType]
		if !known {
			// A step this platform writes for something other than a backend —
			// completion, artifact, plan. Attributing it to a runner would credit
			// one backend with another's work.
			continue
		}
		item.Runner, item.LastAt = runner, lastAt
		experiences[runner] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// What the most recent failure said, per step type. Kept separate because the
	// counts come from every step and this comes from the last bad one.
	failures, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (s.type) s.type, s.error
		  FROM agent_run_steps s
		 WHERE s.status <> 'succeeded' AND s.error <> ''
		 ORDER BY s.type, s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer failures.Close()
	for failures.Next() {
		var stepType, message string
		if err := failures.Scan(&stepType, &message); err != nil {
			return nil, err
		}
		runner, known := byStep[stepType]
		if !known {
			continue
		}
		item := experiences[runner]
		item.Runner, item.LastFailure = runner, message
		experiences[runner] = item
	}
	return experiences, failures.Err()
}
