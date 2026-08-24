package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Every table this deployment appends to has to be reachable by a sweep.
//
// The retention screen says how long operational history is kept. Five tables
// were swept; several others were appended to for as long as the deployment
// lived and no policy could ever touch them — workflow runs, webhook deliveries,
// approvals, launch tickets. Nothing said so, because a table nobody remembers
// is exactly the table nobody remembers.
//
// So the classification is checked against a real database rather than kept in
// somebody's head: a table added later and left out of this map fails here, and
// the failure lands on whoever adds it.
//
// Point it at one with AGENTHUB_TEST_DSN.

// sweptDirectly are the tables the cleanup deletes from by name.
var sweptDirectly = map[string]bool{
	"agent_tasks": true, "agent_runs": true, "platform_events": true,
	"audit_events": true, "notifications": true, "workflow_runs": true,
	"webhook_deliveries": true, "approvals": true, "runtime_launch_tickets": true,
}

// configuration is what a deployment is, rather than what it did. None of it
// grows with use, and sweeping it would delete somebody's setup.
var configuration = map[string]bool{
	"agent_definitions": true, "agent_goals": true, "agent_templates": true,
	"agent_triggers": true, "agent_versions": true, "agent_workflows": true,
	"agent_servers": true, "api_keys": true, "departments": true,
	"evaluation_test_sets": true, "external_apps": true, "mcp_bundles": true,
	"mcp_credentials": true, "mcp_servers": true, "mcp_tool_policies": true,
	// A forge credential is setup, not history: it is written once and used for
	// as long as it works. It goes when its owner does.
	"scm_connections": true,
	"model_endpoints": true, "network_profiles": true, "personal_secrets": true,
	"runtime_images": true, "runtime_profiles": true, "security_profiles": true,
	"schema_migrations": true, "system_settings": true, "user_keyrings": true,
	"user_quotas": true, "users": true, "workspaces": true,
	// Sessions expire on their own clock, and a live one must not be swept by a
	// retention policy meant for history.
	"sessions": true, "runtime_sessions": true,
	// Runtimes and what belongs to them go when the runtime does.
	"agent_runtimes": true, "runtime_config_reports": true,
	// Workers are trimmed by the caretaker when they stop, not by retention.
	"execution_workers": true,
	// A snapshot row names storage that exists outside this database. Deleting
	// the row without the volume would orphan the volume, so it is removed
	// deliberately rather than by a clock.
	"workspace_snapshots": true,
	// Evaluations belong to the test set somebody keeps.
	"agent_evaluations": true,
}

func TestEveryTableIsSweptOrDeliberatelyKept(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to read the schema from")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Reachable means: swept by name, or deleted by a cascade from something that
	// is, at any depth.
	rows, err := pool.Query(ctx, `
		WITH RECURSIVE cascades AS (
			SELECT c.relname AS child, cf.relname AS parent
			FROM pg_constraint con
			JOIN pg_class c ON c.oid = con.conrelid
			JOIN pg_class cf ON cf.oid = con.confrelid
			WHERE con.contype = 'f' AND con.confdeltype = 'c'
		),
		reachable AS (
			SELECT child, parent FROM cascades
			UNION
			SELECT c.child, r.parent FROM cascades c JOIN reachable r ON r.child = c.parent
		)
		SELECT c.relname, COALESCE(array_agg(DISTINCT r.parent) FILTER (WHERE r.parent IS NOT NULL), '{}')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		LEFT JOIN reachable r ON r.child = c.relname
		WHERE c.relkind = 'r'
		GROUP BY c.relname ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var table string
		var parents []string
		if err := rows.Scan(&table, &parents); err != nil {
			t.Fatal(err)
		}
		seen++
		if sweptDirectly[table] || configuration[table] {
			continue
		}
		reachable := false
		for _, parent := range parents {
			if sweptDirectly[parent] {
				reachable = true
				break
			}
		}
		if !reachable {
			t.Errorf("%s 는 어떤 보관 정책으로도 지워지지 않고, 지워지는 표에서 연쇄되지도 않습니다 — 배포가 사는 동안 계속 쌓입니다. 정리 대상에 넣거나 설정으로 분류해 주세요", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen < 20 {
		t.Fatalf("read %d table(s); this guard is not looking at the schema", seen)
	}
}
