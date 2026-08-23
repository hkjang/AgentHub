package store

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Who gets to say a dependency changed.
//
// Every worker runs the sweep, so several of them notice the same machine going
// down inside the same second. If each read the old value and then decided, each
// would announce it, and an operator would get one notification per worker for
// one event. The comparison is inside the write for that reason, and this is the
// only way to check it: two connections doing it at once against a real
// database.
//
// Point it at one with AGENTHUB_TEST_DSN.
const writers = 8

func TestOnlyOneWriterSeesAHealthChange(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the race against")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Connections established before the race, not during it. Without this the
	// writers queue for a connection, the first one commits while the rest are
	// still connecting, and they read a value that has already changed — which
	// makes a read-then-decide implementation pass a test it should fail.
	config.MinConns, config.MaxConns = writers, writers
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{pool: pool}

	var id string
	if err := pool.QueryRow(ctx, `SELECT id FROM agent_servers ORDER BY created_at LIMIT 1`).Scan(&id); err != nil {
		t.Skip("this deployment has no agent server registered to exercise")
	}
	// Every writer holding a live connection before the barrier opens.
	var warm sync.WaitGroup
	warm.Add(writers)
	for range writers {
		go func() {
			defer warm.Done()
			var one int
			_ = pool.QueryRow(ctx, `SELECT pg_sleep(0.05), 1`).Scan(&one, &one)
		}()
	}
	warm.Wait()

	// A known starting point, so what follows is a change from something.
	if _, _, err := store.RecordAgentServerHealth(ctx, id, "healthy", "출발점"); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	seen := make([]bool, writers)
	// Released together. Without the barrier the goroutines finish one at a time —
	// starting a goroutine costs more than the query does — and a read-then-decide
	// implementation passes this test while still being wrong under real load.
	start := make(chan struct{})
	wait.Add(writers)
	for i := range writers {
		go func() {
			defer wait.Done()
			<-start
			_, changed, err := store.RecordAgentServerHealth(ctx, id, "unreachable", "동시에 확인함")
			if err != nil {
				t.Error(err)
				return
			}
			seen[i] = changed
		}()
	}
	close(start)
	wait.Wait()

	announced := 0
	for _, changed := range seen {
		if changed {
			announced++
		}
	}
	if announced != 1 {
		t.Errorf("%d of %d writers were told the health changed; an operator would get that many notifications for one event", announced, writers)
	}

	// And the state afterwards is the one they all wrote, not a torn mixture.
	var health string
	if err := pool.QueryRow(ctx, `SELECT health FROM agent_servers WHERE id=$1`, id).Scan(&health); err != nil {
		t.Fatal(err)
	}
	if health != "unreachable" {
		t.Errorf("after eight writers the row says %q", health)
	}
}
