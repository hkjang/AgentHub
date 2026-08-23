package store

import (
	"context"
	"time"
)

// What this deployment has actually seen a runtime type do.
//
// The console offers fifteen runtime types and every one of them looks equally
// available, which is only true on a deployment where every one of them has been
// set up. Somewhere with no Kubernetes credentials, or without the image for a
// particular type loaded, they are not equal at all — and the person choosing
// finds out by creating an agent, pressing start, and reading a failure.
//
// The platform already knows the answer and has never put it together: which
// types have run here, which were tried and failed, and with what. This is that,
// and nothing more — it is a record of what happened, not a prediction. A type
// nobody has tried is reported as untried rather than guessed at.

// RuntimeTypeExperience is what one runtime type has done on this deployment.
type RuntimeTypeExperience struct {
	RuntimeType string `json:"runtimeType"`
	// Attempts counts every runtime of this type that was ever created here.
	Attempts int `json:"attempts"`
	// Started counts those that reached a state only a runtime that ran can
	// reach. It is the difference between "this type works here" and "somebody
	// tried it once".
	Started int `json:"started"`
	// LastStatus and LastFailure are the most recent runtime's own words.
	LastStatus  string     `json:"lastStatus,omitempty"`
	LastFailure string     `json:"lastFailure,omitempty"`
	LastAt      *time.Time `json:"lastAt,omitempty"`
	// ApprovedImages counts the registered images an administrator has approved
	// for this type. Zero is not a problem on its own — the platform has a
	// default for every type it ships — but it matters on an offline site, where
	// a type with no approved image is a type whose image nobody has loaded.
	ApprovedImages int `json:"approvedImages"`
}

// RuntimeTypeExperiences reports what each type has done here, keyed by type.
//
// Types with no history are absent rather than present with zeros: the caller
// knows the full list of types and an absent entry means untried, which is a
// different thing from tried-and-nothing-happened.
func (s *Store) RuntimeTypeExperiences(ctx context.Context) (map[string]RuntimeTypeExperience, error) {
	experiences := map[string]RuntimeTypeExperience{}

	// A runtime that started is one that reached a state only a running Pod
	// reaches. 'created' and 'pending' are the platform's own bookkeeping before
	// anything happened, and the failure states are the ones an operator would
	// want to know about — both are excluded from the count that means "this
	// works here".
	rows, err := s.pool.Query(ctx, `
		SELECT a.runtime_type,
		       count(*) AS attempts,
		       count(*) FILTER (WHERE r.started_at IS NOT NULL
		                          AND r.status NOT IN ('created','pending','failed','crashed','spawn_failed','unhealthy')) AS started
		  FROM agent_runtimes r
		  JOIN agent_definitions a ON a.id = r.agent_id
		 GROUP BY a.runtime_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item RuntimeTypeExperience
		if err := rows.Scan(&item.RuntimeType, &item.Attempts, &item.Started); err != nil {
			return nil, err
		}
		experiences[item.RuntimeType] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The most recent runtime of each type, and what it said. A count without
	// this is a number nobody can act on: three attempts and no starts is only
	// useful next to the reason the last one gave.
	latest, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (a.runtime_type) a.runtime_type, r.status, r.failure_reason, r.updated_at
		  FROM agent_runtimes r
		  JOIN agent_definitions a ON a.id = r.agent_id
		 ORDER BY a.runtime_type, r.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer latest.Close()
	for latest.Next() {
		var runtimeType, status, failure string
		var at time.Time
		if err := latest.Scan(&runtimeType, &status, &failure, &at); err != nil {
			return nil, err
		}
		item := experiences[runtimeType]
		item.RuntimeType, item.LastStatus, item.LastFailure = runtimeType, status, failure
		when := at
		item.LastAt = &when
		experiences[runtimeType] = item
	}
	if err := latest.Err(); err != nil {
		return nil, err
	}

	images, err := s.pool.Query(ctx, `SELECT runtime_type, count(*) FROM runtime_images
		WHERE approved AND NOT deprecated GROUP BY runtime_type`)
	if err != nil {
		return nil, err
	}
	defer images.Close()
	for images.Next() {
		var runtimeType string
		var count int
		if err := images.Scan(&runtimeType, &count); err != nil {
			return nil, err
		}
		item := experiences[runtimeType]
		item.RuntimeType, item.ApprovedImages = runtimeType, count
		experiences[runtimeType] = item
	}
	return experiences, images.Err()
}
