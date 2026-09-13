package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hkjang/AgentHub/internal/store"
)

// Store is what the service needs from the database. The user lookup is the
// platform's own — mail keeps no directory of its own, because a second list
// of people drifts from the first.
type Store interface {
	Setting(ctx context.Context, key string, dst any) error
	SettingSecret(ctx context.Context, key string) (string, error)
	// UserEmails maps account ids to addresses. Accounts without an address,
	// and disabled ones, are simply absent.
	UserEmails(ctx context.Context, ids []string) (map[string]string, error)
	QueueMail(ctx context.Context, items []store.MailDelivery) error
	// ClaimMail takes queued deliveries that are due, marks them sending and
	// counts the attempt, in a way two senders cannot both do for one row.
	ClaimMail(ctx context.Context, limit int) ([]store.MailDelivery, error)
	SettleMail(ctx context.Context, ids []string, status, message string, retryAt *time.Time) error
	// ExpireMail gives up on deliveries that have sat unsent past the window.
	ExpireMail(ctx context.Context, olderThan time.Duration, message string) (int, error)
	MailDeliveries(ctx context.Context, status string, limit int) (store.MailPage, error)
}

// Delivery is one attempt to reach one person, kept whether or not it worked.
// It carries no body: the subject and the recipient are enough to answer "did
// it go out", and a record that carried the body would be a place the body
// could be read from later.
type Delivery = store.MailDelivery

// Page is what the administrator's delivery list returns.
type Page = store.MailPage

// Delivery statuses.
const (
	StatusQueued  = store.MailQueued
	StatusSending = store.MailSending
	StatusSent    = store.MailSent
	StatusFailed  = store.MailFailed
)

// Notice is one event on its way to people, before recipients are resolved.
type Notice struct {
	Event   string
	Subject string
	// Path is the console route the mail links to, made absolute with the
	// configured base address.
	Path string
}

// Delivery limits. Two attempts: a relay that briefly refuses a connection is
// common, and losing the notice is worse than a minute's wait. Past a day the
// notice is stale — the approval has been decided from the bell, the task
// re-run — and sending it would only confuse.
const (
	MaxAttempts   = 2
	retryDelay    = time.Minute
	staleAfter    = 24 * time.Hour
	claimBatch    = 100
	sweepInterval = 15 * time.Second
)

// Service queues notices and delivers them in the background.
type Service struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
	send   func(context.Context, Config, Message) error
	// Interval is how often queued deliveries are swept. Everything queued in
	// one interval for the same person goes out as one mail, which is what
	// keeps a task that trips three notices from sending three mails.
	Interval time.Duration
}

func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, logger: logger, now: func() time.Time { return time.Now().UTC() }, send: Deliver, Interval: sweepInterval}
}

// SetSender replaces the transport, which lets tests drive the service without
// a real relay.
func (s *Service) SetSender(sender func(context.Context, Config, Message) error) { s.send = sender }

// Config reads the saved settings, the password held beside them and the
// public address the links fall back to.
func (s *Service) Config(ctx context.Context) (Config, error) {
	settings := Defaults()
	if err := s.store.Setting(ctx, SettingKey, &settings); err != nil && !isNotFound(err) {
		return Config{}, err
	}
	password := ""
	if settings.Enabled {
		secret, err := s.store.SettingSecret(ctx, SettingKey)
		if err != nil && !isNotFound(err) {
			return Config{}, err
		}
		password = secret
	}
	var general struct {
		PublicURL string `json:"publicUrl"`
	}
	_ = s.store.Setting(ctx, "general", &general)
	return settings.Config(password, general.PublicURL), nil
}

// isNotFound: a fresh deployment has no mail row at all, and that means
// "defaults".
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// Notify resolves the recipients and queues one delivery each. It returns
// without touching the network, so the request that raised the event finishes
// the same whether the relay is up or not. Nil-safe: a process built without a
// mailer simply does not mail.
//
// The actor is whoever caused the event, and is dropped from the recipients:
// nobody needs a mail about what they just did.
func (s *Service) Notify(ctx context.Context, notice Notice, actorID string, recipients []string) {
	if s == nil || s.store == nil || len(recipients) == 0 {
		return
	}
	// The event's own request may be ending; the queue write should not end
	// with it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	config, err := s.Config(ctx)
	if err != nil {
		s.logger.Warn("mail settings could not be read; notice not mailed", "event", notice.Event, "error", err)
		return
	}
	if !config.Enabled || !config.Allows(notice.Event) {
		return
	}
	ids := wanted(recipients, actorID)
	if len(ids) == 0 {
		return
	}
	addresses, err := s.store.UserEmails(ctx, ids)
	if err != nil {
		s.logger.Warn("mail recipients could not be resolved", "event", notice.Event, "error", err)
		return
	}
	now := s.now()
	items := make([]Delivery, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		address := strings.TrimSpace(addresses[id])
		if address == "" || seen[strings.ToLower(address)] {
			continue
		}
		seen[strings.ToLower(address)] = true
		items = append(items, Delivery{
			ID: uuid.NewString(), Event: notice.Event, UserID: id, Recipient: address,
			Subject: trim(notice.Subject, 300), ResourceURL: notice.Path, ActorID: actorID,
			Status: StatusQueued, CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(items) == 0 {
		return
	}
	if err := s.store.QueueMail(ctx, items); err != nil {
		s.logger.Warn("mail delivery was not queued", "event", notice.Event, "error", err)
	}
}

// wanted is the recipient list minus blanks, duplicates and the actor.
func wanted(recipients []string, actorID string) []string {
	actor := strings.TrimSpace(actorID)
	seen := map[string]bool{}
	ids := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		id := strings.TrimSpace(recipient)
		if id == "" || seen[id] || (actor != "" && strings.EqualFold(id, actor)) {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// SendNow delivers one test mail immediately and reports the outcome, which
// is what the administrator's button needs: a relay setting is rarely right
// the first time, and "saved" says nothing about whether it works. The
// attempt is recorded like any other.
func (s *Service) SendNow(ctx context.Context, recipient, actorID string) (Delivery, error) {
	config, err := s.Config(ctx)
	if err != nil {
		return Delivery{}, err
	}
	if !config.Enabled {
		return Delivery{}, ErrDisabled
	}
	recipient = strings.TrimSpace(recipient)
	if !strings.Contains(recipient, "@") {
		return Delivery{}, fmt.Errorf("%w: 받는 사람은 메일 주소여야 합니다", ErrInvalid)
	}
	now := s.now()
	delivery := Delivery{
		ID: uuid.NewString(), Event: EventTest, Recipient: recipient, Subject: "메일 알림 시험 발송",
		ResourceURL: "/admin/settings", ActorID: actorID, Status: StatusSending, Attempts: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.QueueMail(ctx, []Delivery{delivery}); err != nil {
		return Delivery{}, err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.Timeout+5*time.Second)
	defer cancel()
	sendErr := s.send(sendCtx, config, Message{To: recipient, Subject: s.subject(config, delivery), Body: s.body(config, []Delivery{delivery})})
	status, message := StatusSent, ""
	if sendErr != nil {
		status, message = StatusFailed, sendErr.Error()
	}
	if err := s.store.SettleMail(sendCtx, []string{delivery.ID}, status, trim(message, 1000), nil); err != nil {
		s.logger.Warn("mail delivery status was not recorded", "error", err)
	}
	delivery.Status, delivery.ErrorMessage = status, message
	return delivery, sendErr
}

// Run sweeps the queue until the context ends. It belongs in one process —
// the control plane — though the claim is safe should two run.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	interval := s.Interval
	if interval <= 0 {
		interval = sweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep delivers what is due: one mail per person for everything queued to
// them, and one record update per delivery. It returns how many deliveries it
// settled, which is what the tests count.
func (s *Service) Sweep(ctx context.Context) int {
	config, err := s.Config(ctx)
	if err != nil {
		s.logger.Warn("mail settings could not be read", "error", err)
		return 0
	}
	if !config.Enabled {
		return 0
	}
	if expired, err := s.store.ExpireMail(ctx, staleAfter, "하루 안에 보내지 못해 포기했습니다"); err != nil {
		s.logger.Warn("stale mail could not be expired", "error", err)
	} else if expired > 0 {
		s.logger.Warn("stale mail given up", "count", expired)
	}
	items, err := s.store.ClaimMail(ctx, claimBatch)
	if err != nil {
		s.logger.Warn("queued mail could not be claimed", "error", err)
		return 0
	}
	settled := 0
	for _, group := range byRecipient(items) {
		settled += len(group)
		s.deliver(ctx, config, group)
	}
	return settled
}

// byRecipient bundles the claimed deliveries per address, oldest first, in a
// stable order so the same queue always makes the same mails.
func byRecipient(items []Delivery) [][]Delivery {
	index := map[string]int{}
	groups := [][]Delivery{}
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Recipient))
		at, known := index[key]
		if !known {
			at = len(groups)
			index[key] = at
			groups = append(groups, nil)
		}
		groups[at] = append(groups[at], item)
	}
	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool { return group[i].CreatedAt.Before(group[j].CreatedAt) })
	}
	return groups
}

// deliver sends one bundle and records the outcome on every delivery in it.
func (s *Service) deliver(ctx context.Context, config Config, group []Delivery) {
	ids := make([]string, 0, len(group))
	attempts := 0
	for _, item := range group {
		ids = append(ids, item.ID)
		if item.Attempts > attempts {
			attempts = item.Attempts
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, config.Timeout+5*time.Second)
	defer cancel()
	message := Message{To: group[0].Recipient, Subject: s.subject(config, group...), Body: s.body(config, group)}
	err := s.send(sendCtx, config, message)
	status, detail := StatusSent, ""
	var retryAt *time.Time
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalid) || attempts >= MaxAttempts:
		// A setting that is wrong will be wrong in a minute too; the record
		// says which one, and the readiness list says so as well.
		status, detail = StatusFailed, err.Error()
		s.logger.Warn("notification mail failed", "recipient", message.To, "count", len(group), "error", err)
	default:
		at := s.now().Add(retryDelay)
		status, detail, retryAt = StatusQueued, err.Error(), &at
		s.logger.Warn("notification mail will be retried", "recipient", message.To, "count", len(group), "error", err)
	}
	if err := s.store.SettleMail(context.WithoutCancel(ctx), ids, status, trim(detail, 1000), retryAt); err != nil {
		s.logger.Warn("mail delivery status was not recorded", "error", err)
	}
}

// subject is the mail's subject line: the notice's own for one, a count for a
// bundle. The service name in brackets is what an inbox rule can key on.
func (s *Service) subject(config Config, group ...Delivery) string {
	prefix := "[" + config.FromName + "] "
	if len(group) == 1 {
		return prefix + group[0].Subject
	}
	return fmt.Sprintf("%s알림 %d건: %s 외 %d건", prefix, len(group), group[0].Subject, len(group)-1)
}

// body lists each notice with its link and says why the mail came. A person
// who wants to stop them is pointed at the administrator, because the
// switches are theirs.
func (s *Service) body(config Config, group []Delivery) string {
	lines := []string{}
	if len(group) > 1 {
		lines = append(lines, fmt.Sprintf("%s 에서 알림 %d건이 있습니다.", config.FromName, len(group)), "")
	}
	for _, item := range group {
		lines = append(lines, "• "+item.Subject)
		if link := config.Link(item.ResourceURL); link != "" {
			lines = append(lines, "  "+link)
		}
		lines = append(lines, "")
	}
	lines = append(lines, "—", fmt.Sprintf("이 메일은 %s 의 메일 알림 설정에 따라 자동으로 발송되었습니다. 받지 않으려면 관리자에게 알려 주세요.", config.FromName))
	return strings.Join(lines, "\n")
}

// Deliveries lists what was sent, newest first, with a status breakdown.
func (s *Service) Deliveries(ctx context.Context, status string, limit int) (Page, error) {
	if s == nil || s.store == nil {
		return Page{Items: []Delivery{}, Summary: store.MailSummary{Status: map[string]int{}}}, nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return s.store.MailDeliveries(ctx, strings.TrimSpace(status), limit)
}

func trim(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
