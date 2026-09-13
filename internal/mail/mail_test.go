package mail

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// fakeRelay is a minimal SMTP server. It records the conversation so a test can
// assert what the platform actually said, including whether it tried to
// authenticate.
type fakeRelay struct {
	address    string
	offerAuth  bool
	rejectFrom bool
	mu         sync.Mutex
	commands   []string
	bodies     []string
	listener   net.Listener
}

func startRelay(t *testing.T, offerAuth bool) *fakeRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	relay := &fakeRelay{address: listener.Addr().String(), offerAuth: offerAuth, listener: listener}
	go relay.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return relay
}

func (f *fakeRelay) host() string { host, _, _ := net.SplitHostPort(f.address); return host }
func (f *fakeRelay) port() int {
	_, port, _ := net.SplitHostPort(f.address)
	value := 0
	_, _ = fmt.Sscanf(port, "%d", &value)
	return value
}

func (f *fakeRelay) record(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, line)
}

func (f *fakeRelay) transcript() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *fakeRelay) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *fakeRelay) serve() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(connection)
	}
}

func (f *fakeRelay) handle(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	write("220 relay.internal ESMTP test")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		f.record(command)
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-relay.internal")
			if f.offerAuth {
				write("250-AUTH PLAIN LOGIN")
			}
			write("250 SIZE 35882577")
		case strings.HasPrefix(upper, "HELO"):
			write("250 relay.internal")
		case strings.HasPrefix(upper, "AUTH"):
			write("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM"):
			if f.rejectFrom {
				write("550 5.7.1 Sender rejected")
				continue
			}
			write("250 2.1.0 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			write("250 2.1.5 Ok")
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
			f.mu.Lock()
			f.bodies = append(f.bodies, body.String())
			f.mu.Unlock()
			write("250 2.0.0 Ok: queued")
		case upper == "QUIT":
			write("221 2.0.0 Bye")
			return
		default:
			write("250 2.0.0 Ok")
		}
	}
}

func relayConfig(relay *fakeRelay) Config {
	return Config{Enabled: true, Host: relay.host(), Port: relay.port(), FromAddress: "agenthub@corp.example",
		FromName: "AgentHub 알림", Security: SecurityAuto, Timeout: 3 * time.Second, Events: map[string]bool{}}
}

// An internal relay that asks for nothing must work with no credentials — that
// is the default a company relay on port 25 needs.
func TestDeliverWithoutAuthentication(t *testing.T) {
	relay := startRelay(t, false)
	err := Deliver(context.Background(), relayConfig(relay), Message{To: "reviewer@corp.example", Subject: "승인 요청", Body: "본문"})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	for _, command := range relay.transcript() {
		if strings.HasPrefix(strings.ToUpper(command), "AUTH") {
			t.Fatalf("authenticated against a relay that offered no AUTH: %v", relay.transcript())
		}
	}
	bodies := relay.received()
	if len(bodies) != 1 {
		t.Fatalf("expected one message, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], "Subject: =?utf-8?q?") || !strings.Contains(bodies[0], "To: reviewer@corp.example") {
		t.Fatalf("headers not encoded as expected:\n%s", bodies[0])
	}
}

func TestDeliverAuthenticatesWhenConfigured(t *testing.T) {
	relay := startRelay(t, true)
	config := relayConfig(relay)
	config.Username, config.Password = "notifier", "s3cret"
	if err := Deliver(context.Background(), config, Message{To: "a@corp.example", Subject: "x", Body: "y"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	authenticated := false
	for _, command := range relay.transcript() {
		if strings.HasPrefix(strings.ToUpper(command), "AUTH PLAIN") {
			authenticated = true
		}
	}
	if !authenticated {
		t.Fatalf("no AUTH in transcript: %v", relay.transcript())
	}
}

func TestDeliverReportsRejection(t *testing.T) {
	relay := startRelay(t, false)
	relay.rejectFrom = true
	err := Deliver(context.Background(), relayConfig(relay), Message{To: "a@corp.example", Subject: "x", Body: "y"})
	if err == nil || !strings.Contains(err.Error(), "MAIL FROM") {
		t.Fatalf("expected the relay's refusal to be reported, got %v", err)
	}
}

// --- the service, over a fake store ---

type fakeStore struct {
	mu       sync.Mutex
	settings map[string]any
	secret   string
	emails   map[string]string
	rows     []store.MailDelivery
}

func newFakeStore(settings Settings, password string) *fakeStore {
	return &fakeStore{settings: map[string]any{SettingKey: settings, "general": map[string]any{"publicUrl": "https://hub.corp.example"}}, secret: password,
		emails: map[string]string{"owner": "owner@corp.example", "reviewer": "reviewer@corp.example", "admin": "admin@corp.example"}}
}

func (f *fakeStore) Setting(_ context.Context, key string, dst any) error {
	value, ok := f.settings[key]
	if !ok {
		return store.ErrNotFound
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, dst)
}
func (f *fakeStore) SettingSecret(context.Context, string) (string, error) { return f.secret, nil }
func (f *fakeStore) UserEmails(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if email, ok := f.emails[id]; ok {
			out[id] = email
		}
	}
	return out, nil
}
func (f *fakeStore) QueueMail(_ context.Context, items []store.MailDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, items...)
	return nil
}
func (f *fakeStore) ClaimMail(_ context.Context, limit int) ([]store.MailDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claimed := []store.MailDelivery{}
	for i := range f.rows {
		if f.rows[i].Status != StatusQueued || len(claimed) >= limit {
			continue
		}
		f.rows[i].Status, f.rows[i].Attempts = StatusSending, f.rows[i].Attempts+1
		claimed = append(claimed, f.rows[i])
	}
	return claimed, nil
}
func (f *fakeStore) SettleMail(_ context.Context, ids []string, status, message string, retryAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		for _, id := range ids {
			if f.rows[i].ID == id {
				f.rows[i].Status, f.rows[i].ErrorMessage, f.rows[i].NextAttempt = status, message, retryAt
			}
		}
	}
	return nil
}
func (f *fakeStore) ExpireMail(context.Context, time.Duration, string) (int, error) { return 0, nil }
func (f *fakeStore) MailDeliveries(context.Context, string, int) (store.MailPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return store.MailPage{Items: append([]store.MailDelivery(nil), f.rows...)}, nil
}

func (f *fakeStore) deliveries() []store.MailDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.MailDelivery(nil), f.rows...)
}

func enabledSettings(relay *fakeRelay) Settings {
	settings := Defaults()
	settings.Enabled = true
	settings.SMTPHost, settings.SMTPPort = relay.host(), relay.port()
	settings.FromAddress = "agenthub@corp.example"
	settings.TimeoutSeconds = 2
	return settings
}

func TestOffSendsNothingAndQueuesNothing(t *testing.T) {
	db := newFakeStore(Defaults(), "")
	service := NewService(db, nil)
	service.SetSender(func(context.Context, Config, Message) error { t.Fatal("sent while off"); return nil })
	service.Notify(context.Background(), Notice{Event: EventTaskFailed, Subject: "x"}, "", []string{"owner"})
	if service.Sweep(context.Background()) != 0 || len(db.deliveries()) != 0 {
		t.Fatalf("off should queue nothing, got %+v", db.deliveries())
	}
	// A fresh deployment has no mail row at all, and that is the same as off.
	delete(db.settings, SettingKey)
	service.Notify(context.Background(), Notice{Event: EventTaskFailed, Subject: "x"}, "", []string{"owner"})
	if len(db.deliveries()) != 0 {
		t.Fatalf("a missing setting should mean off, got %+v", db.deliveries())
	}
}

// Enabled with no host: nothing goes out, and the record says which setting is
// missing. Not retried — the host will be missing in a minute too.
func TestAMissingHostIsWrittenOnTheRecord(t *testing.T) {
	settings := Defaults()
	settings.Enabled = true
	settings.FromAddress = "agenthub@corp.example"
	db := newFakeStore(settings, "")
	service := NewService(db, nil)
	service.Notify(context.Background(), Notice{Event: EventTaskFailed, Subject: "작업 실패: 야간 배치"}, "", []string{"owner"})
	if n := service.Sweep(context.Background()); n != 1 {
		t.Fatalf("expected one delivery settled, got %d", n)
	}
	row := db.deliveries()[0]
	if row.Status != StatusFailed || !strings.Contains(row.ErrorMessage, "mail.smtp_host") {
		t.Fatalf("expected a failed record naming mail.smtp_host, got %+v", row)
	}
}

// The request that raises an event returns whether the relay is there or not:
// Notify never opens a connection, and the failed attempts land on the record.
func TestADeadRelayNeverReachesTheRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // nothing answers here now
	settings := Defaults()
	settings.Enabled = true
	settings.FromAddress = "agenthub@corp.example"
	fmt.Sscanf(strings.Split(address, ":")[1], "%d", &settings.SMTPPort)
	settings.SMTPHost, settings.TimeoutSeconds = "127.0.0.1", 1
	db := newFakeStore(settings, "")
	service := NewService(db, nil)

	started := time.Now()
	service.Notify(context.Background(), Notice{Event: EventApprovalRequested, Subject: "승인 요청: 배포"}, "", []string{"reviewer"})
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("Notify waited on the relay: %v", time.Since(started))
	}
	if rows := db.deliveries(); len(rows) != 1 || rows[0].Status != StatusQueued {
		t.Fatalf("expected one queued delivery, got %+v", rows)
	}
	service.Sweep(context.Background())
	row := db.deliveries()[0]
	if row.Status != StatusQueued || row.Attempts != 1 || row.ErrorMessage == "" || row.NextAttempt == nil {
		t.Fatalf("first failure should be recorded and re-queued for one retry, got %+v", row)
	}
	service.Sweep(context.Background())
	row = db.deliveries()[0]
	if row.Status != StatusFailed || row.Attempts != MaxAttempts || !strings.Contains(row.ErrorMessage, "SMTP 연결 실패") {
		t.Fatalf("second failure should be final with the reason, got %+v", row)
	}
}

func TestNobodyIsMailedAboutTheirOwnAction(t *testing.T) {
	relay := startRelay(t, false)
	db := newFakeStore(enabledSettings(relay), "")
	service := NewService(db, nil)
	// The reviewer decided; the reviewer is also the requester.
	service.Notify(context.Background(), Notice{Event: EventApprovalDecided, Subject: "승인됨"}, "reviewer", []string{"reviewer"})
	if len(db.deliveries()) != 0 {
		t.Fatalf("the actor was mailed about their own action: %+v", db.deliveries())
	}
	// Somebody else asked: the reviewer hears, the asker does not.
	service.Notify(context.Background(), Notice{Event: EventApprovalRequested, Subject: "승인 요청"}, "owner", []string{"reviewer", "owner"})
	rows := db.deliveries()
	if len(rows) != 1 || rows[0].Recipient != "reviewer@corp.example" {
		t.Fatalf("expected the reviewer only, got %+v", rows)
	}
}

func TestAnEventSwitchSilencesOnlyItsKind(t *testing.T) {
	relay := startRelay(t, false)
	settings := enabledSettings(relay)
	off := false
	settings.NotifyTaskFailed = &off
	db := newFakeStore(settings, "")
	service := NewService(db, nil)
	service.Notify(context.Background(), Notice{Event: EventTaskFailed, Subject: "작업 실패"}, "", []string{"owner"})
	service.Notify(context.Background(), Notice{Event: EventApprovalRequested, Subject: "승인 요청"}, "", []string{"reviewer"})
	rows := db.deliveries()
	if len(rows) != 1 || rows[0].Event != EventApprovalRequested {
		t.Fatalf("expected only the approval to be queued, got %+v", rows)
	}
}

// One sweep, one person, several notices: one mail, and every record settled.
func TestNoticesToOnePersonGoOutAsOneMail(t *testing.T) {
	relay := startRelay(t, false)
	db := newFakeStore(enabledSettings(relay), "")
	service := NewService(db, nil)
	ctx := context.Background()
	service.Notify(ctx, Notice{Event: EventTaskFailed, Subject: "작업 실패: 야간 배치", Path: "/tasks"}, "", []string{"owner"})
	service.Notify(ctx, Notice{Event: EventTaskHandoff, Subject: "런타임에서 이어받아야 하는 작업: 리팩터", Path: "/tasks"}, "", []string{"owner"})
	service.Notify(ctx, Notice{Event: EventApprovalRequested, Subject: "승인 요청: 배포", Path: "/reviews"}, "", []string{"owner", "reviewer"})
	if n := service.Sweep(ctx); n != 4 {
		t.Fatalf("expected four deliveries settled, got %d", n)
	}
	bodies := relay.received()
	if len(bodies) != 2 {
		t.Fatalf("expected one mail per person (2), relay got %d", len(bodies))
	}
	var bundle string
	for _, body := range bodies {
		if strings.Contains(body, "To: owner@corp.example") {
			bundle = body
		}
	}
	for _, want := range []string{"• 작업 실패: 야간 배치", "• 런타임에서 이어받아야 하는 작업: 리팩터", "• 승인 요청: 배포", "https://hub.corp.example/reviews"} {
		if !strings.Contains(bundle, want) {
			t.Errorf("the bundle lacks %q:\n%s", want, bundle)
		}
	}
	for _, row := range db.deliveries() {
		if row.Status != StatusSent || row.Attempts != 1 {
			t.Errorf("expected every record sent on the first attempt, got %+v", row)
		}
	}
}

func TestSuccessAndFailureAreBothRecorded(t *testing.T) {
	relay := startRelay(t, false)
	db := newFakeStore(enabledSettings(relay), "")
	service := NewService(db, nil)
	ctx := context.Background()
	if _, err := service.SendNow(ctx, "admin@corp.example", "admin"); err != nil {
		t.Fatalf("test send: %v", err)
	}
	relay.rejectFrom = true
	if _, err := service.SendNow(ctx, "admin@corp.example", "admin"); err == nil {
		t.Fatal("a rejected sender should be reported")
	}
	rows := db.deliveries()
	if len(rows) != 2 || rows[0].Status != StatusSent || rows[1].Status != StatusFailed || rows[1].ErrorMessage == "" {
		t.Fatalf("expected one sent and one failed record with a reason, got %+v", rows)
	}
	for _, row := range rows {
		if row.Event != EventTest || row.Recipient != "admin@corp.example" || row.Subject == "" {
			t.Errorf("a record should say what, to whom and which event: %+v", row)
		}
	}
}

func TestSendNowRefusesWhileOff(t *testing.T) {
	db := newFakeStore(Defaults(), "")
	if _, err := NewService(db, nil).SendNow(context.Background(), "a@corp.example", ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

// The password is the row's secret, never a field of the blob the settings
// API returns. Checked by shape: a field tagged password on Settings would be
// read back by GET /admin/settings.
func TestTheSettingsCarryNoPassword(t *testing.T) {
	kind := reflect.TypeOf(Settings{})
	for i := 0; i < kind.NumField(); i++ {
		tag := strings.ToLower(kind.Field(i).Tag.Get("json"))
		if strings.Contains(tag, "password") || strings.Contains(tag, "secret") {
			t.Fatalf("Settings.%s is tagged %q and would come back from the settings API", kind.Field(i).Name, tag)
		}
	}
	raw, _ := json.Marshal(Defaults())
	if strings.Contains(strings.ToLower(string(raw)), "password") {
		t.Fatalf("the settings document mentions a password: %s", raw)
	}
	// And the transport reads it from the secret, not the blob.
	config := enabledSettings(startRelay(t, true)).Config("hunter2", "")
	if config.Password != "hunter2" {
		t.Fatalf("the secret did not reach the transport: %+v", config)
	}
}

func TestSettingsValidationAndDefaults(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults must be valid while off: %v", err)
	}
	settings := Defaults()
	settings.Enabled = true
	if err := settings.Validate(); err == nil || !strings.Contains(err.Error(), "릴레이 주소") {
		t.Fatalf("enabled without a host should be refused, got %v", err)
	}
	settings.SMTPHost = "relay.corp.example"
	if err := settings.Validate(); err == nil || !strings.Contains(err.Error(), "보내는 주소") {
		t.Fatalf("enabled without a sender should be refused, got %v", err)
	}
	settings.FromAddress = "agenthub@corp.example"
	settings.Security = "smtps"
	if err := settings.Validate(); err == nil {
		t.Fatal("an unknown security mode should be refused")
	}
	settings.Security = ""
	settings.SMTPPort = 465
	config := settings.Config("", "https://hub.corp.example/")
	if config.Security != SecurityTLS || config.Port != 465 || config.Timeout != 10*time.Second {
		t.Fatalf("port 465 should imply tls and the timeout its default, got %+v", config)
	}
	if got := config.Link("/reviews"); got != "https://hub.corp.example/reviews" {
		t.Fatalf("link should fall back to the public URL, got %q", got)
	}
	if !config.Allows(EventTaskFailed) || !config.Allows("something.new") {
		t.Fatal("switches default to on, including for events added later")
	}
}
