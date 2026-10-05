package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// The details of the newest trail row about this settings key, read back
	// through the store the audit screen reads. Each use follows the request that
	// wrote the row, so the newest one is that request's.
	newestTrailDetails := func(t *testing.T) map[string]any {
		t.Helper()
		page, err := db.AuditTrail(ctx, store.AuditFilter{Action: "settings.update", ResourceType: "setting", ResourceID: tracking.SettingKey, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatal("the write left no audit row")
		}
		details, _ := page.Items[0]["details"].(map[string]any)
		if details == nil {
			t.Fatalf("the newest trail row carries no details: %v", page.Items[0])
		}
		return details
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

	// The list as a whole, on both routes. Every entry here is an ordinary origin
	// that each per-entry check accepts, so what refuses these is the limit on the
	// list — and it has to hold on the one-click route too, or an administrator
	// clicking "allow" beside one report after another walks the header past what
	// a proxy will carry one origin at a time.
	t.Run("the list as a whole stops at its limit on both routes", func(t *testing.T) {
		const size = tracking.MaxAllowedHostsTotalRunes / tracking.MaxAllowedHostEntries
		// Distinct origins of exactly the width the limits divide into, so the
		// list sits on its total the moment it holds MaxAllowedHostEntries of them.
		entry := func(n int) string {
			const prefix, suffix = "https://", ".corp.example"
			return prefix + fmt.Sprintf("%03d", n) + strings.Repeat("a", size-len(prefix)-len(suffix)-3) + suffix
		}
		var seeded []string
		for n := range tracking.MaxAllowedHostEntries - 1 {
			seeded = append(seeded, entry(n))
		}
		stored := strings.Join(seeded, "\n")
		if err := db.PutSetting(ctx, tracking.SettingKey, document(stored), nil, admin.ID); err != nil {
			t.Fatal(err)
		}

		// The settings form, with two more entries than the list holds.
		response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": document(stored + "\n" + entry(100) + "\n" + entry(101))})
		if response.Code != http.StatusBadRequest {
			t.Errorf("the settings form answered %d: %s", response.Code, response.Body.String())
		}
		if text := message(response); !strings.HasPrefix(text, "허용 출처 목록") {
			t.Errorf("the settings form did not say the list is what is wrong: %q", text)
		}
		if hosts := storedHosts(); hosts != stored {
			t.Errorf("the settings form wrote a list of %d entries over the stored %d", len(strings.Split(hosts, "\n")), len(seeded))
		}

		// One click fits, the one after it does not, and clicking again does not
		// get past it.
		if response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": entry(200)}); response.Code != http.StatusOK {
			t.Fatalf("the click that fills the list answered %d: %s", response.Code, response.Body.String())
		}
		full := stored + "\n" + entry(200)
		if hosts := storedHosts(); hosts != full {
			t.Fatalf("the click that fills the list stored something else: %d entries", len(strings.Split(hosts, "\n")))
		}
		for attempt := range 3 {
			response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": entry(201 + attempt)})
			if response.Code != http.StatusBadRequest {
				t.Errorf("click %d past a full list answered %d: %s", attempt+1, response.Code, response.Body.String())
			}
			if text := message(response); !strings.HasPrefix(text, "허용 출처 목록") {
				t.Errorf("click %d past a full list blames the origin, not the list: %q", attempt+1, text)
			}
			if hosts := storedHosts(); hosts != full {
				t.Errorf("click %d past a full list wrote %d entries", attempt+1, len(strings.Split(hosts, "\n")))
			}
		}

		// And the header the console actually serves from the full list is one a
		// proxy will carry: the tripled list, the base policy and the snippet's own
		// origin together, rather than however much a settings body can hold.
		if policy := policyHeader(); len(policy) > 16*1024 {
			t.Errorf("the full list serves a %d byte policy header", len(policy))
		}
	})

	// The allow list reaches the policy header. What a provider branch renders —
	// the three site and measurement ids, and the whole of the two collector
	// addresses — reaches the body of every tracked page instead, through
	// serveIndex and injectSnippet, and nothing bounded its size: validation asked
	// only whether the field the chosen provider needs was filled in. The limits
	// on them are exercised here through the real route with a real session,
	// because a unit call to validateSetting is not proof that a write is refused,
	// and the page is read back because the page is where the harm was.
	t.Run("an oversized provider id or address never reaches a page", func(t *testing.T) {
		providerDocument := func(fields map[string]any) map[string]any {
			value := map[string]any{"enabled": true, "placement": "head"}
			for key, item := range fields {
				value[key] = item
			}
			return value
		}
		// The whole stored row, read back through the store rather than through the
		// response the request wrote.
		storedSettings := func() tracking.Settings {
			t.Helper()
			settings := tracking.Defaults()
			if err := db.Setting(ctx, tracking.SettingKey, &settings); err != nil {
				t.Fatal(err)
			}
			return settings
		}
		// The page the console would serve from whatever is in the row now,
		// snippet included.
		pageBody := func() string {
			t.Helper()
			server.invalidateTrackingSettings()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
			return recorder.Body.String()
		}

		const settledID = "7"
		settledDocument := providerDocument(map[string]any{"provider": tracking.ProviderMatomo,
			"matomoUrl": "https://matomo.corp.example", "matomoSiteId": settledID})
		if err := db.PutSetting(ctx, tracking.SettingKey, settledDocument, nil, admin.ID); err != nil {
			t.Fatal(err)
		}
		// The control, so the refusals below are the limits talking and not the
		// route: a provider configuration really does render its id and its address
		// into the page.
		if body := pageBody(); !strings.Contains(body, `_paq.push(['setSiteId','`+settledID+`'])`) || !strings.Contains(body, "https://matomo.corp.example") {
			t.Fatalf("the stored provider configuration is not rendered into the page: %s", body)
		}

		oversizedID := strings.Repeat("7", tracking.MaxProviderIDRunes+1)
		oversizedPath := strings.Repeat("p", tracking.MaxProviderURLRunes)
		for name, pair := range map[string]struct {
			value map[string]any
			seen  string
		}{
			"a measurement id": {providerDocument(map[string]any{"provider": tracking.ProviderGA4,
				"measurementId": "G-" + strings.Repeat("1", tracking.MaxProviderIDRunes)}), strings.Repeat("1", tracking.MaxProviderIDRunes)},
			"a site id": {providerDocument(map[string]any{"provider": tracking.ProviderMatomo,
				"matomoUrl": "https://matomo.corp.example", "matomoSiteId": oversizedID}), oversizedID},
			"a collector address with a path on it": {providerDocument(map[string]any{"provider": tracking.ProviderMatomo,
				"matomoUrl": "https://matomo.corp.example/" + oversizedPath, "matomoSiteId": settledID}), oversizedPath},
			// Switched off and unselected, which is the write the limits have to
			// refuse here rather than later: stored now, a second write that only
			// flips the provider would serve it.
			"a site id stored with tracking switched off": {providerDocument(map[string]any{"enabled": false,
				"provider": tracking.ProviderNone, "momentoSiteId": oversizedID}), oversizedID},
		} {
			response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": pair.value})
			if response.Code != http.StatusBadRequest {
				t.Errorf("%s: the settings form answered %d: %s", name, response.Code, response.Body.String())
			}
			if text := message(response); !strings.Contains(text, "넘을 수 없습니다") {
				t.Errorf("%s: the refusal does not say what the limit is: %q", name, text)
			}
			if settings := storedSettings(); settings.MatomoSiteID != settledID || settings.MeasurementID != "" || settings.MomentoSiteID != "" || settings.MatomoURL != "https://matomo.corp.example" {
				t.Errorf("%s: the settings form wrote over the stored configuration: %+v", name, settings)
			}
			if body := pageBody(); strings.Contains(body, pair.seen) {
				t.Errorf("%s: the refused value is in the page body (%d bytes)", name, len(body))
			}
		}

		// And the control again from the other side: a value on the limit is stored
		// and served, so what the limits describe as allowed is allowed.
		onTheLimit := providerDocument(map[string]any{"provider": tracking.ProviderMatomo,
			"matomoUrl":    "https://matomo.corp.example/" + strings.Repeat("p", tracking.MaxProviderURLRunes-len("https://matomo.corp.example/")),
			"matomoSiteId": strings.Repeat("7", tracking.MaxProviderIDRunes)})
		if response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": onTheLimit}); response.Code != http.StatusOK {
			t.Fatalf("a provider configuration on the limits was refused: %d %s", response.Code, response.Body.String())
		}
		if body := pageBody(); !strings.Contains(body, strings.Repeat("7", tracking.MaxProviderIDRunes)) {
			t.Errorf("a configuration on the limits is not served: %d bytes", len(body))
		}
	})

	// The one-click route answers for one origin — the one a report named — and
	// its trail row has to say what the list now holds. Neither was true. The
	// handler checked for an "http" prefix and appended the string as it arrived,
	// while splitHosts, which every reader of the list goes through, separates on
	// a comma, a space, a tab and a newline alike: one click could store two
	// entries, and the row named the string that arrived rather than the entry
	// that was stored, so the trail could not be read against the list. A click
	// that changes nothing was recorded exactly like one that added an origin.
	//
	// The settings form is where a list belongs and is exercised alongside, with
	// the very value the one click refuses, because the two routes read the same
	// stored string and only one of them is being narrowed here.
	t.Run("one click adds one origin and the trail names what was stored", func(t *testing.T) {
		seed := func() {
			t.Helper()
			if err := db.PutSetting(ctx, tracking.SettingKey, document(settled), nil, admin.ID); err != nil {
				t.Fatal(err)
			}
		}
		seed()
		for name, pair := range map[string]string{
			"separated by a comma":   "https://a.corp.example,https://evil.corp.example",
			"separated by a space":   "https://a.corp.example https://evil.corp.example",
			"separated by a newline": "https://a.corp.example\nhttps://evil.corp.example",
			"separated by a tab":     "https://a.corp.example\thttps://evil.corp.example",
		} {
			response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": pair})
			if response.Code != http.StatusBadRequest {
				t.Errorf("%s: the one-click allow answered %d: %s", name, response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code != "invalid_origin" {
				t.Errorf("%s: the refusal came back as %q, not invalid_origin", name, code)
			}
			if hosts := storedHosts(); hosts != settled {
				t.Errorf("%s: one click stored %q", name, hosts)
			}
		}
		// What the one click refuses, the settings form still takes: a list in one
		// box is that route's whole purpose.
		if response := request(http.MethodPut, "/api/v1/admin/settings/"+tracking.SettingKey, map[string]any{"value": document(settled + ",https://b.corp.example")}); response.Code != http.StatusOK {
			t.Fatalf("the settings form no longer takes a list: %d %s", response.Code, response.Body.String())
		}
		if hosts := storedHosts(); hosts != settled+",https://b.corp.example" {
			t.Fatalf("the settings form stored %q", hosts)
		}

		// A trailing slash and the space around a pasted address are not part of
		// the entry, so the row has to name the entry without them — an auditor who
		// cannot match the row against the list learns nothing from it.
		seed()
		const added = "https://pixel.corp.example"
		if response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": "  " + added + "/  "}); response.Code != http.StatusOK {
			t.Fatalf("one ordinary origin was refused: %d %s", response.Code, response.Body.String())
		}
		if hosts := storedHosts(); hosts != settled+"\n"+added {
			t.Fatalf("stored %q", hosts)
		}
		details := newestTrailDetails(t)
		if details["origin"] != added {
			t.Errorf("the trail says %#v, the list holds %q", details["origin"], added)
		}
		if details["added"] != true {
			t.Errorf("the trail does not record that the list grew: %v", details)
		}

		// And the click that changes nothing. The console still gets its list back,
		// so the screen keeps working, but the row says the list did not grow.
		if response := request(http.MethodPost, "/api/v1/admin/tracking/violations/allow", map[string]any{"origin": added + "/"}); response.Code != http.StatusOK {
			t.Fatalf("allowing an origin already in the list answered %d: %s", response.Code, response.Body.String())
		}
		if hosts := storedHosts(); hosts != settled+"\n"+added {
			t.Fatalf("a repeated click rewrote the list: %q", hosts)
		}
		details = newestTrailDetails(t)
		if details["origin"] != added {
			t.Errorf("the repeated click's row says %#v", details["origin"])
		}
		if details["added"] != false {
			t.Errorf("a click that changed nothing is recorded like one that did: %v", details)
		}
	})
}

// errorCode reads the machine-readable half of an error body, which is what the
// console branches on and what a refusal has to keep stable.
func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not an error body: %s", recorder.Body.String())
	}
	return body.Error.Code
}
