package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/jackc/pgx/v5"
)

// Requires an isolated PostgreSQL database and Node 22+. The Node subprocess
// calls the same backup/update/finally module as the Playwright Ready branch,
// through Server.Handler(), including real session authentication and CSRF.
func TestLangflowSettings(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTHUB_TEST_DSN is required for the live settings check")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the live settings check")
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("AGENTHUB_ENCRYPTION_KEY"))
	if err != nil {
		t.Fatal("invalid test encryption key")
	}
	cipher, err := cryptox.New(key)
	if err != nil {
		t.Fatal("a valid test encryption key is required")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := db.UpsertOIDCUser(ctx, "langflow-settings-test:admin", "langflow-settings-admin", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != roleAdmin {
		t.Fatal("test account must be an administrator")
	}
	session, csrf, _, err := db.CreateSession(ctx, admin.ID, "127.0.0.1", "langflow-settings-test")
	if err != nil {
		t.Fatal(err)
	}
	// Preserve any previous row, including absence, for repeatable live runs.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	previous, err := db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clear := func() {
		t.Helper()
		if _, err := conn.Exec(ctx, `DELETE FROM system_settings WHERE key='sessionGateway'`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if value, exists := previous["sessionGateway"]; exists {
			if err := db.PutSetting(ctx, "sessionGateway", value, nil, admin.ID); err != nil {
				t.Error(err)
			}
		} else {
			clear()
		}
	})
	server := New(db, cipher, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), appLog.NewRing(8), nil, nil)
	handler := server.Handler()
	var writes atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/admin/settings/sessionGateway" {
			writes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	module, err := filepath.Abs("../../web/scripts/session-gateway-check.mjs")
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]any{
		"enabled": true, "scheme": "https", "baseDomain": "original.example", "sessionHours": float64(4),
		"future": map[string]any{"list": []any{"preserve", float64(7)}, "nested": map[string]any{"flag": true}},
	}
	for _, scenario := range []string{"success", "check throws", "missing", "unauthorized", "invalid value"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "missing" {
				clear()
			} else {
				var value any = original
				if scenario == "invalid value" {
					value = []any{}
				}
				if err := db.PutSetting(ctx, "sessionGateway", value, nil, admin.ID); err != nil {
					t.Fatal(err)
				}
			}
			writes.Store(0)
			input, err := json.Marshal(map[string]any{
				"url": endpoint.URL, "module": module, "session": session, "csrf": csrf,
				"scenario": scenario, "original": original,
			})
			if err != nil {
				t.Fatal(err)
			}
			commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(commandCtx, node, "--input-type=module", "--eval", langflowSettingsCheck)
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shared settings check failed: %v\n%s", err, output)
			}
			wantWrites := int32(0)
			if scenario == "success" || scenario == "check throws" {
				wantWrites = 3
			}
			if writes.Load() != wantWrites {
				t.Fatalf("got %d PUT requests, want %d", writes.Load(), wantWrites)
			}
			if wantWrites > 0 {
				var restored map[string]any
				if err := db.Setting(ctx, "sessionGateway", &restored); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(restored, original) {
					t.Error("the database did not retain the complete original setting")
				}
			}
		})
	}
}

const langflowSettingsCheck = `
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
const config = JSON.parse(readFileSync(0, 'utf8'))
const { withSessionGateway } = await import(pathToFileURL(config.module))
const call = async (method, path, body) => {
  const headers = { 'Content-Type': 'application/json' }
  if (config.scenario !== 'unauthorized') {
    headers.Cookie = 'agenthub_session=' + config.session
    headers['X-CSRF-Token'] = config.csrf
  }
  const response = await fetch(config.url + path, { method, headers,
    body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await response.text()
  let parsed = null
  let parseError = false
  try { parsed = text ? JSON.parse(text) : null } catch { parseError = true }
  return { status: response.status, body: parsed, parseError }
}
let checks = 0
const run = withSessionGateway(call, async ({ gateway, setGateway }) => {
  checks++
  assert.deepEqual(gateway, config.original)
  for (const value of [
    { ...gateway, enabled: false, baseDomain: '' },
    { ...gateway, enabled: true, scheme: 'https', baseDomain: 'rt.e2e.internal', sessionHours: 8 },
  ]) {
    await setGateway(value)
    const current = await call('GET', '/api/v1/admin/settings')
    assert.equal(current.status, 200)
    assert.deepEqual(current.body.sessionGateway, value)
  }
  if (config.scenario === 'check throws') throw new Error('launch check failed')
})
if (config.scenario === 'unauthorized') {
  await assert.rejects(run, /backup.*HTTP 401/)
  assert.equal(checks, 0)
} else if (config.scenario === 'invalid value') {
  await assert.rejects(run, /invalid setting object/)
  assert.equal(checks, 0)
} else if (config.scenario === 'missing') {
  const result = await run
  assert.equal(result.skipped, true)
  assert.match(result.reason, /sessionGateway/)
  assert.equal(checks, 0)
  assert.equal(Object.hasOwn((await call('GET', '/api/v1/admin/settings')).body, 'sessionGateway'), false)
} else {
  if (config.scenario === 'check throws') await assert.rejects(run, /launch check failed/)
  else assert.deepEqual(await run, { skipped: false })
  assert.equal(checks, 1)
  const after = await call('GET', '/api/v1/admin/settings')
  assert.equal(after.status, 200)
  assert.deepEqual(after.body.sessionGateway, config.original)
}
`
