package store

import (
	"context"
	"time"
)

// What a trigger has actually produced.
//
// A trigger records when it last fired and when it will fire next, and nothing
// about what came of it. So a schedule that fires every hour into a task that
// fails every hour reads exactly like one that works, and the only way to find
// out is to open the task list and recognise the titles.
//
// This is the other half of the same record: how many tasks this trigger has
// created lately, how many of them failed, and how the last one ended. It is the
// difference between automation somebody set up and automation somebody can
// trust.

// TriggerHealth is one trigger's recent record.
type TriggerHealth struct {
	TriggerID string `json:"triggerId"`
	// Tasks and Failed count the window. A trigger with no tasks in it is not
	// failing — it may simply not have been due — which is why LastFiredAt stays
	// the thing that says whether it runs at all.
	Tasks  int `json:"tasks"`
	Failed int `json:"failed"`
	// LastStatus is how the most recent task from this trigger ended, and
	// LastError what it said. Empty when nothing has run in the window.
	LastStatus string     `json:"lastStatus,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
	LastAt     *time.Time `json:"lastAt,omitempty"`
}

// TriggerHealthWindow is how far back the record is read. Long enough that a
// daily schedule has fired a few times, short enough that a fault fixed last
// month is not still being reported.
const TriggerHealthWindow = 7 * 24 * time.Hour

// TriggerHealthFor reads what each of an owner's triggers has produced.
//
// One query for all of them: a console listing twenty triggers must not make
// twenty round trips, and the number beside each one is worthless if it is too
// expensive to show.
func (s *Store) TriggerHealthFor(ctx context.Context, ownerID, agentID string) (map[string]TriggerHealth, error) {
	since := time.Now().UTC().Add(-TriggerHealthWindow)
	rows, err := s.pool.Query(ctx, `
		SELECT t.trigger_id,
		       count(*),
		       count(*) FILTER (WHERE t.status IN ('failed', 'dead_letter')),
		       (array_agg(t.status ORDER BY t.created_at DESC))[1],
		       (array_agg(COALESCE(t.last_error, '') ORDER BY t.created_at DESC))[1],
		       max(t.created_at)
		FROM agent_tasks t
		WHERE t.trigger_id IS NOT NULL
		  AND t.created_at >= $1
		  AND ($2 = '' OR t.owner_id = $2)
		  AND ($3 = '' OR t.agent_id = $3)
		GROUP BY t.trigger_id`, since, ownerID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	health := map[string]TriggerHealth{}
	for rows.Next() {
		var item TriggerHealth
		if err := rows.Scan(&item.TriggerID, &item.Tasks, &item.Failed,
			&item.LastStatus, &item.LastError, &item.LastAt); err != nil {
			return nil, err
		}
		health[item.TriggerID] = item
	}
	return health, rows.Err()
}

// OverdueTriggers counts enabled schedules whose next firing is well in the past.
//
// The type is spelled the way the schema spells it. The first version of this
// asked for 'schedule', which no row has ever been, so it reported nothing
// forever and looked exactly like a healthy deployment — found by running it
// rather than by reading it, which is why the guard beside this compares the
// spelling against the constraint.
//
// One overdue trigger is a cron expression nobody can satisfy. Every trigger
// overdue at once is the scheduler not running — which looks, from every screen,
// exactly like a quiet week: the console answers, the agents are there, and
// nothing happens. It is the same silence as having no workers, and that already
// has a place on the readiness list.
func (s *Store) OverdueTriggers(ctx context.Context, grace time.Duration) (overdue int, total int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE next_fire_at IS NOT NULL AND next_fire_at < $1), count(*)
		FROM agent_triggers WHERE enabled AND type = 'cron'`,
		time.Now().UTC().Add(-grace)).Scan(&overdue, &total)
	return overdue, total, err
}

// The reasons a webhook is turned away, in the words its owner needs. They are
// deliberately about what to change: a signature mismatch is a secret somebody
// has to compare, a replay is a sender retrying, a disabled trigger is a switch.
const (
	RejectedSignature = "서명이 맞지 않습니다"
	RejectedNoSecret  = "서명을 확인할 설정이 없습니다"
	RejectedReplay    = "이미 처리한 요청입니다"
	RejectedDisabled  = "꺼져 있는 트리거입니다"
)

// RecordTriggerRejection keeps what a trigger turned away.
//
// On the trigger's own row, not in a table: this endpoint is reachable by
// anybody who knows the address, and history that strangers can append to is
// history that fills a disk. A counter and the latest reason answer the question
// an owner actually has — is something calling this, and why is it not working —
// without giving the caller a way to write anything but a number.
func (s *Store) RecordTriggerRejection(ctx context.Context, triggerID, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_triggers
		SET rejected_count = rejected_count + 1, last_rejection = $2, last_rejected_at = now()
		WHERE id = $1`, triggerID, reason)
	return err
}

// RecordTriggerFired stamps a trigger that has just started work.
//
// Only the scheduler used to do this, through the statement that also advances
// the next firing — so a webhook trigger that had accepted a thousand deliveries
// still read "never fired", and so did every event trigger. The schedule is not
// touched here: these two have nothing to advance, and the fact worth recording
// is simply that this trigger did something.
func (s *Store) RecordTriggerFired(ctx context.Context, triggerID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_triggers SET last_fired_at = now() WHERE id = $1`, triggerID)
	return err
}
