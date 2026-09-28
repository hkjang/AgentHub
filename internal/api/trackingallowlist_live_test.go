package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/tracking"
	"github.com/jackc/pgx/v5"
)

// Requires an isolated PostgreSQL database. An entry of the tracking allow list
// is joined into the Content-Security-Policy header of every page as written,
// and there are two ways one gets stored: the settings form and the one-click
// "allow" beside a reported violation. Both are exercised here through
// Server.Handler() with a real administrator session and CSRF token, because an
// entry refused in one and accepted in the other would leave the header open.
func TestAnAllowedOriginCannotReachThePolicyHeaderUnchecked(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("AGENTHUB_TEST_DSN is required for the live tracking allow-list check")
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
	admin, err := db.UpsertOIDCUser(ctx, "tracking-allowlist-test:admin", "tracking-allowlist-admin", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != roleAdmin {
		t.Fatal("test account must be an administrator")
	}
	session, csrf, _, err := db.CreateSession(ctx, admin.ID, "127.0.0.1", "tracking-allowlist-test")
	if err != nil {
		t.Fatal(err)
	}
	// Whatever this deployment already had in the row goes back afterwards,
	// including the absence of a row.
	previous, err := db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	t.Cleanup(func() {
		if value, exists := previous[tracking.SettingKey]; exists {
			if err := db.PutSetting(ctx, tracking.SettingKey, value, nil, admin.ID); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := conn.Exec(ctx, `DELETE FROM system_settings WHERE key=$1`, tracking.SettingKey); err != nil {
			t.Error(err)
		}
	})

	server := New(db, cipher, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), appLog.NewRing(8), nil,
		fstest.MapFS{"index.html": {Data: []byte(testShell)}})
	handler := server.Handler()
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder
	}
	// What is actually in the row, read back through the store rather than
	// through the response the request wrote.
	storedHosts := func() string {
		t.Helper()
		settings := tracking.Defaults()
		if err := db.Setting(ctx, tracking.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		return settings.AllowedHosts
	}
	// The policy header the console would serve from whatever is in the row now.
	policyHeader := func() string {
		t.Helper()
		server.invalidateTrackingSettings()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
		return recorder.Header().Get("Content-Security-Policy")
	}
	document := func(allowedHosts string) map[string]any {
		return map[string]any{
			"enabled": true, "provider": tracking.ProviderCustom, "placement": "head",
			"customSnippet": `<script src="https://t.corp.example/t.js"></script>`,
			"allowedHosts":  allowedHosts,
		}
	}
	message := func(recorder *httptest.ResponseRecorder) string {
		t.Helper()
		var body errorBody
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("the refusal is not an error body: %s", recorder.Body.String())
		}
		return body.Error.Message
	}

	const settled = "https://a.corp.example"
	if err := db.PutSetting(ctx, tracking.SettingKey, document(settled), nil, admin.ID); err != nil {
		t.Fatal(err)
	}
	directives := strings.Count(policyHeader(), ";")

	for name, refused := range map[string]string{
		"a semicolon opens a directive of its own": "https://evil.corp.example/;script-src-elem",
		"longer than any origin is":                "https://" + strings.Repeat("a", tracking.MaxAllowedHostRunes) + ".corp.example",
	} {
		t.Run(name, func(t *testing.T) {
			// The settings form: the whole document, with the entry appended.
			response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": document(settled + "\n" + refused)})
			if response.Code != http.StatusBadRequest {
				t.Errorf("the settings form answered %d: %s", response.Code, response.Body.String())
			}
			if text := message(response); !strings.HasPrefix(text, "허용 출처 ") {
				t.Errorf("the settings form did not say which entry is wrong: %q", text)
			}
			if hosts := storedHosts(); hosts != settled {
				t.Errorf("the settings form wrote %q", hosts)
			}

			// And the one click beside a reported violation, which validates the
			// same document after adding the one line.
			response = request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": refused})
			if response.Code != http.StatusBadRequest {
				t.Errorf("the one-click allow answered %d: %s", response.Code, response.Body.String())
			}
			if text := message(response); !strings.HasPrefix(text, "허용 출처 ") {
				t.Errorf("the one-click allow did not say which entry is wrong: %q", text)
			}
			if hosts := storedHosts(); hosts != settled {
				t.Errorf("the one-click allow wrote %q", hosts)
			}

			// Nothing of it reached the header every page is served under.
			policy := policyHeader()
			if strings.Contains(policy, "evil.corp.example") || strings.Contains(policy, strings.Repeat("a", tracking.MaxAllowedHostRunes)) {
				t.Errorf("the refused entry is in the page policy: %s", policy)
			}
			if got := strings.Count(policy, ";"); got != directives {
				t.Errorf("the page policy has %d directives, not %d: %s", got, directives, policy)
			}
		})
	}

	// The control: both routes still store an origin nobody objects to, so the
	// refusals above are the validation talking and not the session, the CSRF
	// token or the route.
	t.Run("an ordinary origin still goes in both ways", func(t *testing.T) {
		if response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": document(settled + "\nhttps://*.corp.example")}); response.Code != http.StatusOK {
			t.Fatalf("the settings form answered %d: %s", response.Code, response.Body.String())
		}
		if response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": "https://pixel.corp.example:8443/"}); response.Code != http.StatusOK {
			t.Fatalf("the one-click allow answered %d: %s", response.Code, response.Body.String())
		}
		want := settled + "\nhttps://*.corp.example\nhttps://pixel.corp.example:8443"
		if hosts := storedHosts(); hosts != want {
			t.Fatalf("stored %q, want %q", hosts, want)
		}
		if policy := policyHeader(); !strings.Contains(policy, "https://*.corp.example") || !strings.Contains(policy, "https://pixel.corp.example:8443") {
			t.Errorf("an allowed origin is not in the page policy: %s", policy)
		}
	})
}
