package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A runtime that never came up must not hold its owner's quota.
//
// The record is created before its profile is known, and created already counted
// as running. A warm-up that was then refused, or whose start failed, left the
// row at running with no Pod behind it — and releasing the pool's hold put it
// beyond the cooling sweep, which only looks at runtimes that still have one. So
// the capacity was held by something that would never run and could never be
// stopped.
//
// Point it at a database with AGENTHUB_TEST_DSN.
func TestARuntimeThatNeverStartedIsGivenBack(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to exercise this against")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{pool: pool}

	var owner, agentID string
	if err := pool.QueryRow(ctx, `SELECT owner_id, id FROM agent_definitions LIMIT 1`).Scan(&owner, &agentID); err != nil {
		t.Skip("this deployment has no agent to attach a runtime to")
	}
	seed := func(status string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO agent_runtimes(id,agent_id,owner_id,status,desired_state,crd_name)
			VALUES(gen_random_uuid()::text,$1,$2,$3,'running','abandon-probe') RETURNING id`,
			agentID, owner, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM agent_runtimes WHERE crd_name='abandon-probe'`) }()

	// Never came up: given back, and no longer counted.
	pending := seed("pending")
	if stopped, err := store.AbandonUnstartedRuntime(ctx, pending); err != nil || !stopped {
		t.Fatalf("a runtime that never started was not given back (stopped=%v, err=%v)", stopped, err)
	}
	var state, status string
	if err := pool.QueryRow(ctx, `SELECT desired_state, status FROM agent_runtimes WHERE id=$1`, pending).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" {
		t.Errorf("it still holds capacity: desired_state is %q", state)
	}

	// Actually running: left alone. Somebody is working in it, whatever the pool
	// thinks — stopping it here would take a runtime away from its user.
	live := seed("running")
	if stopped, err := store.AbandonUnstartedRuntime(ctx, live); err != nil {
		t.Fatal(err)
	} else if stopped {
		t.Error("a running runtime was stopped by the give-back path")
	}
	if err := pool.QueryRow(ctx, `SELECT desired_state FROM agent_runtimes WHERE id=$1`, live).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Errorf("a running runtime was left at %q", state)
	}
}
