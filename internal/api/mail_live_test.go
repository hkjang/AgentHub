package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/mail"
	"github.com/hkjang/AgentHub/internal/store"
)

// The mail settings, through the production handlers and a real database:
// the password goes in as the row's secret and never comes back out, the test
// button sends one real message through a stand-in relay, and the delivery
// list shows the attempt with its subject and recipient and no body.
//
// Point it at a database with AGENTHUB_TEST_DSN and AGENTHUB_ENCRYPTION_KEY.
func TestTheMailSettingsRoundTripWithoutThePassword(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("no database to check the wiring against")
	}
	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("AGENTHUB_ENCRYPTION_KEY"))
	if err != nil || len(rawKey) == 0 {
		t.Skip("no encryption key to seal the password with")
	}
	cipher, err := cryptox.New(rawKey)
	if err != nil {
		t.Skip("no encryption key to seal the password with")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("the schema could not be brought up to date: %v", err)
	}
	admin, err := db.UpsertOIDCUser(ctx, "agenthub-mail-test:admin", "mail-test-admin", "mail-admin@corp.example", "메일 관리자", true)
	if err != nil {
		t.Skipf("this deployment will not let the check create the administrator it needs: %v", err)
	}
	var previous json.RawMessage
	_ = db.Setting(ctx, mail.SettingKey, &previous)
	previousSecret, _ := db.SettingSecret(ctx, mail.SettingKey)
	t.Cleanup(func() {
		if previous != nil {
			_ = db.PutSetting(ctx, mail.SettingKey, previous, &previousSecret, admin.ID)
		}
	})
	relay := startStandInRelay(t)
	server := New(db, cipher, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), appLog.NewRing(8), nil, nil).
		WithMailer(mail.NewService(db, nil))

	as := func(method, path string, body any, params map[string]string) *httptest.ResponseRecorder {
		var payload string
		if body != nil {
			raw, _ := json.Marshal(body)
			payload = string(raw)
		}
		request := httptest.NewRequest(method, path, strings.NewReader(payload))
		request.Header.Set("content-type", "application/json")
		routeContext := chi.NewRouteContext()
		for key, value := range params {
			routeContext.URLParams.Add(key, value)
		}
		request = request.WithContext(context.WithValue(context.WithValue(request.Context(), userContextKey, admin), chi.RouteCtxKey, routeContext))
		recorder := httptest.NewRecorder()
		switch {
		case path == "/api/v1/admin/mail/test":
			server.adminSendTestMail(recorder, request)
		case strings.HasPrefix(path, "/api/v1/admin/mail/deliveries"):
			server.adminMailDeliveries(recorder, request)
		case method == http.MethodPut:
			server.putAdminSetting(recorder, request)
		default:
			server.adminSettings(recorder, request)
		}
		return recorder
	}

	host, port, _ := net.SplitHostPort(relay.address)
	value := map[string]any{"enabled": true, "smtp_host": host, "smtp_port": atoi(port), "security": "none",
		"username": "notifier", "from_address": "agenthub@corp.example", "from_name": "AgentHub", "timeout_seconds": 3}
	if saved := as(http.MethodPut, "/api/v1/admin/settings/mail", map[string]any{"value": value, "secret": "SENTINEL-MAIL-PASSWORD-7e21"}, map[string]string{"key": "mail"}); saved.Code != http.StatusOK {
		t.Fatalf("saving the mail settings returned %d: %s", saved.Code, saved.Body)
	}

	// The settings come back with the password's existence and nothing else.
	read := as(http.MethodGet, "/api/v1/admin/settings", nil, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("reading settings returned %d", read.Code)
	}
	if strings.Contains(read.Body.String(), "SENTINEL-MAIL-PASSWORD") {
		t.Fatalf("the SMTP password came back out of the settings API: %s", read.Body)
	}
	var settings map[string]map[string]any
	if err := json.Unmarshal(read.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if configured, _ := settings["mail"]["passwordConfigured"].(bool); !configured {
		t.Fatalf("the settings should say a password is set: %v", settings["mail"])
	}
	if _, present := settings["mail"]["password"]; present {
		t.Fatalf("a password field is present in the settings document: %v", settings["mail"])
	}

	// One real message through the relay, from the saved settings, with the
	// saved password used to authenticate.
	sent := as(http.MethodPost, "/api/v1/admin/mail/test", map[string]any{"recipient": "someone@corp.example"}, nil)
	if sent.Code != http.StatusOK {
		t.Fatalf("the test send returned %d: %s", sent.Code, sent.Body)
	}
	messages := relay.received()
	if len(messages) != 1 || !strings.Contains(messages[0], "To: someone@corp.example") {
		t.Fatalf("the relay should have received one message, got %d: %v", len(messages), messages)
	}
	if !relay.authenticated() {
		t.Fatalf("the saved password was not used to authenticate: %v", relay.transcript())
	}

	// And the attempt is on the record: what, to whom, whether it went — and
	// not the body.
	listed := as(http.MethodGet, "/api/v1/admin/mail/deliveries?status=sent", nil, nil)
	var page store.MailPage
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("delivery list: %v (%s)", err, listed.Body)
	}
	found := false
	for _, item := range page.Items {
		if item.Recipient == "someone@corp.example" && item.Event == mail.EventTest && item.Status == store.MailSent {
			found = true
		}
	}
	if !found {
		t.Fatalf("the test send is not in the sent list: %+v", page.Items)
	}
	if strings.Contains(listed.Body.String(), "자동으로 발송되었습니다") {
		t.Fatalf("the delivery list carries the mail body: %s", listed.Body)
	}
}

func atoi(value string) int {
	n := 0
	for _, digit := range value {
		n = n*10 + int(digit-'0')
	}
	return n
}

// standInRelay speaks just enough SMTP to accept one message and remember
// whether the client authenticated.
type standInRelay struct {
	address  string
	mu       sync.Mutex
	commands []string
	bodies   []string
}

func startStandInRelay(t *testing.T) *standInRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := &standInRelay{address: listener.Addr().String()}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go relay.handle(connection)
		}
	}()
	return relay
}

func (r *standInRelay) handle(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	write("220 relay.internal ESMTP stand-in")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		r.mu.Lock()
		r.commands = append(r.commands, command)
		r.mu.Unlock()
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-relay.internal")
			write("250-AUTH PLAIN LOGIN")
			write("250 SIZE 35882577")
		case strings.HasPrefix(upper, "AUTH"):
			write("235 2.7.0 Authentication successful")
		case upper == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				body.WriteString(dataLine)
			}
			r.mu.Lock()
			r.bodies = append(r.bodies, body.String())
			r.mu.Unlock()
			write("250 2.0.0 Ok: queued")
		case upper == "QUIT":
			write("221 2.0.0 Bye")
			return
		default:
			write("250 2.0.0 Ok")
		}
	}
}

func (r *standInRelay) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func (r *standInRelay) transcript() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func (r *standInRelay) authenticated() bool {
	for _, command := range r.transcript() {
		if strings.HasPrefix(strings.ToUpper(command), "AUTH ") {
			return true
		}
	}
	return false
}
