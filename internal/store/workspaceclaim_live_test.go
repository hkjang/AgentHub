package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Who gets the last of the storage.
//
// The quota was read and the workspace written with nothing holding the gap, so
// two requests arriving together each saw room for the last hundred gigabytes
// and each took it. A unit test cannot make two transactions race, so this is
// the only way to check it: real connections, released together, against a real
// database.
//
// Point it at one with AGENTHUB_TEST_DSN.
func TestOnlyOneWorkspaceTakesTheLastOfTheQuota(t *testing.T) {
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
	// Connections established before the race rather than during it: the writers
	// must overlap, and a pool that is still connecting serialises them into a
	// test that passes whatever the code does.
	config.MinConns, config.MaxConns = writers, writers
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{pool: pool}

	var owner string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' LIMIT 1`).Scan(&owner); err != nil {
		t.Skip("this deployment has no administrator to attribute workspaces to")
	}

	// A limit this owner is already close to. Whatever they hold now plus one
	// more workspace is the ceiling, so exactly one of the writers may win.
	var held int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(size_gb), 0) FROM workspaces WHERE owner_id=$1`, owner).Scan(&held); err != nil {
		t.Fatal(err)
	}
	const size = 5
	limit := held + size
	if _, err := pool.Exec(ctx, `INSERT INTO user_quotas(owner_id, quota) VALUES($1, $2::jsonb)
		ON CONFLICT (owner_id) DO UPDATE SET quota = excluded.quota`,
		owner, `{"maxStorageGB": `+itoa(limit)+`}`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_quotas WHERE owner_id=$1`, owner)
		_, _ = pool.Exec(ctx, `DELETE FROM workspaces WHERE owner_id=$1 AND name LIKE 'quota-race-%'`, owner)
	}()

	var wait sync.WaitGroup
	start := make(chan struct{})
	won := make([]bool, writers)
	refused := make([]bool, writers)
	wait.Add(writers)
	for i := range writers {
		go func() {
			defer wait.Done()
			<-start
			_, err := store.CreateWorkspaceWithinQuota(ctx, owner, Workspace{
				Name: "quota-race-" + itoa(i), SizeGB: size, Type: "empty",
			})
			switch {
			case err == nil:
				won[i] = true
			case isExceeded(err):
				refused[i] = true
			default:
				t.Errorf("writer %d failed for an unrelated reason: %v (%T)", i, err, err)
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
		t.Errorf("%d of %d writers took the last %dGB; the quota allowed one", created, writers, size)
	}

	// And the database agrees: the total never went past the limit.
	var after int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(size_gb), 0) FROM workspaces WHERE owner_id=$1`, owner).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after > limit {
		t.Errorf("this owner now holds %dGB against a limit of %dGB", after, limit)
	}
}

func isExceeded(err error) bool { return errors.Is(err, quota.ErrExceeded) }

func itoa(value int) string { return strconv.Itoa(value) }
