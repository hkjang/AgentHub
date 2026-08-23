package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Who gets the last runtime.
//
// The limit was read and the runtime written with nothing holding the gap, so
// two starts arriving together each saw room for the last one. Same shape as the
// storage race, same way of checking it: real connections, released together,
// against a real database.
//
// Point it at one with AGENTHUB_TEST_DSN.
func TestOnlyOneRuntimeTakesTheLastOfTheQuota(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the race against")
	}
	ctx := context.Background()
	const writers = 6
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Room for each writer's transaction and for the reads it makes before
	// opening one. Sized to the writers exactly, the pre-reads queue for
	// connections and stagger the writers — which hides the race and lets a
	// version with no lock pass. That happened.
	config.MinConns, config.MaxConns = writers*3, writers*3
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{pool: pool}

	var owner, agentID string
	if err := pool.QueryRow(ctx, `SELECT a.owner_id, a.id FROM agent_definitions a
		JOIN users u ON u.id = a.owner_id WHERE u.role='admin' LIMIT 1`).Scan(&owner, &agentID); err != nil {
		t.Skip("this deployment has no agent to start runtimes for")
	}

	var running int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runtimes WHERE owner_id=$1 AND desired_state='running'`, owner).Scan(&running); err != nil {
		t.Fatal(err)
	}
	limit := running + 1
	if _, err := pool.Exec(ctx, `INSERT INTO user_quotas(owner_id, quota) VALUES($1, $2::jsonb)
		ON CONFLICT (owner_id) DO UPDATE SET quota = excluded.quota`,
		owner, `{"maxRuntimes": `+itoa(limit)+`}`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_quotas WHERE owner_id=$1`, owner)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_runtimes WHERE owner_id=$1 AND crd_name LIKE 'race-%'`, owner)
	}()

	agent := Agent{ID: agentID, OwnerID: owner}
	var wait sync.WaitGroup
	start := make(chan struct{})
	won := make([]bool, writers)
	wait.Add(writers)
	for i := range writers {
		go func() {
			defer wait.Done()
			<-start
			err := store.ClaimRuntimeCapacity(ctx, owner, "", "", func(tx pgx.Tx) error {
				_, inner := tx.Exec(ctx, `INSERT INTO agent_runtimes(id,agent_id,owner_id,status,desired_state,crd_name)
					VALUES(gen_random_uuid()::text,$1,$2,'pending','running',$3)`, agent.ID, agent.OwnerID, "race-"+itoa(i))
				return inner
			})
			switch {
			case err == nil:
				won[i] = true
			case errors.Is(err, quota.ErrExceeded):
			default:
				t.Errorf("writer %d failed for an unrelated reason: %v", i, err)
			}
		}()
	}
	close(start)
	wait.Wait()

	created := 0
	for _, ok := range won {
		if ok {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d of %d starts took the last runtime; the quota allowed one", created, writers)
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runtimes WHERE owner_id=$1 AND desired_state='running'`, owner).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after > limit {
		t.Errorf("this owner now holds %d running runtimes against a limit of %d", after, limit)
	}
}
