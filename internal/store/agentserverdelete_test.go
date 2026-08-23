package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// How a registered server may be removed.
//
// Both references were declared backwards when this backend was added, in the
// same direction: the delete succeeded and something else changed quietly.
//
// A Goal that names one machine must hold the delete, because the reason for
// naming a machine usually cannot be written in a field — and an unpinned Goal
// takes any server in any network. A finished run must not hold it, because
// history is a record of what happened rather than a pointer at something that
// still exists.
func TestARegisteredServerIsHeldByGoalsAndNotByHistory(t *testing.T) {
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	schema := ""
	for _, entry := range migrations {
		body, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		schema += string(body)
	}
	if !strings.Contains(schema, "agent_goals ADD CONSTRAINT agent_goals_agent_server_id_fkey") {
		t.Error("a Goal's agent server is not a reference the database enforces; deleting the machine would silently unpin the Goal, and an unpinned Goal takes any machine in any zone")
	}
	// The wrong version is not searched for: migrations are history and the
	// original text stays in it forever. What the database ends up with is
	// checked against a real one below.
	if !strings.Contains(schema, "agent_runs DROP CONSTRAINT IF EXISTS agent_runs_agent_server_id_fkey") {
		t.Error("a finished run still references the server it ran on, so retiring a machine erases where past work happened")
	}
}

// TestTheDatabaseReallyHoldsAPinnedServer reads the constraints a live database
// ended up with, because the migration files say what was asked for and only the
// database says what is true.
//
// Point it at one with AGENTHUB_TEST_DSN.
func TestTheDatabaseReallyHoldsAPinnedServer(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to read the constraints from")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var action string
	err = pool.QueryRow(ctx, `SELECT confdeltype::text FROM pg_constraint
		WHERE conname = 'agent_goals_agent_server_id_fkey'`).Scan(&action)
	if err != nil {
		t.Fatalf("a Goal's agent server is not a reference this database enforces: %v", err)
	}
	// 'a' is no action: the delete is refused while a Goal points at the server.
	// 'n' would be set null, which is the version that quietly unpinned the Goal.
	if action != "a" && action != "r" {
		t.Errorf("deleting a server a Goal names does %q instead of refusing", action)
	}

	var references int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'agent_runs'::regclass AND conname LIKE '%agent_server%'`).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 0 {
		t.Error("a finished run still references the server it ran on, so retiring a machine erases where past work happened")
	}
}
