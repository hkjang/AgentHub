package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Dispatcher turns platform events into tasks.
//
// It runs beside the cron Scheduler in the worker and reads from a durable
// outbox: an offline deployment has no broker to lean on, and an in-memory bus
// would lose every event a restart caught in flight.
//
// Delivery is attempted under a lease and only marked done once it finished.
// Marking a batch delivered at claim time — which is what this used to do — meant
// a worker that died, or a task insert that failed, lost the event while the
// record said it had been delivered. Each subscriber's task is created together
// with its ledger row in one transaction, so a redelivery after a lost completion
// marker does not queue the same work twice.
type Dispatcher struct {
	store    *store.Store
	logger   *slog.Logger
	workerID string
	// Interval is how often the outbox is drained.
	Interval time.Duration
	// Batch bounds one pass, so a burst cannot flood the task queue at once.
	Batch int
	// Lease is how long a claim is held before another worker may take the event
	// over. It bounds how long a dead worker can sit on an undelivered event.
	Lease time.Duration
	// MaxAttempts is how many times an event is retried before it is parked for
	// an operator instead of being retried forever.
	MaxAttempts int
}

func NewDispatcher(db *store.Store, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store: db, logger: logger, workerID: "dispatcher",
		Interval: 5 * time.Second, Batch: 50, Lease: 2 * time.Minute, MaxAttempts: 5,
	}
}

// WithWorkerID names the worker holding the lease, which is what makes a stuck
// claim traceable to a process.
func (d *Dispatcher) WithWorkerID(id string) *Dispatcher {
	if id != "" {
		d.workerID = id
	}
	return d
}

func (d *Dispatcher) Run(ctx context.Context) error {
	d.logger.Info("event dispatcher started", "intervalSeconds", d.Interval.Seconds())
	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()
	for {
		d.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	events, err := d.store.ClaimEvents(ctx, d.workerID, d.Lease, d.Batch)
	if err != nil {
		d.logger.Error("events could not be claimed", "error", err)
		return
	}
	for _, event := range events {
		d.deliver(ctx, event)
	}
}

// deliver hands one event to every subscriber, then closes it out. Anything that
// fails leaves the event pending for another attempt rather than marking it
// delivered, and the subscribers that did get their task are recorded so the next
// attempt does not repeat them.
func (d *Dispatcher) deliver(ctx context.Context, event store.PlatformEvent) {
	// Completing an event must survive shutdown: an event delivered but not marked
	// so would be delivered again, which the ledger absorbs but the operator
	// should not have to reason about.
	finish := context.WithoutCancel(ctx)

	triggers, err := d.store.TriggersForEvent(ctx, event)
	if err != nil {
		d.retry(finish, event, "이벤트 구독자를 읽지 못했습니다: "+err.Error())
		return
	}
	delivered, failed := 0, ""
	for _, trigger := range triggers {
		title := trigger.TaskTitle
		if title == "" {
			title = trigger.Name
		}
		task, created, err := d.store.DeliverEventToTrigger(finish, event.ID, trigger, store.CreateTaskInput{
			AgentID: trigger.AgentID, OwnerID: trigger.OwnerID,
			Title: title, Input: eventInput(trigger.TaskInput, event), Priority: trigger.Priority,
			Source: "event", TriggerID: &trigger.ID, CreatedBy: trigger.OwnerID,
		})
		switch {
		case err != nil:
			// Keep going: one broken subscriber must not stop the others, and the
			// event stays pending so this one is tried again.
			d.logger.Error("event task could not be created", "event", event.ID, "trigger", trigger.ID, "error", err)
			failed = err.Error()
		case !created:
			d.logger.Info("event already delivered to this subscriber", "event", event.ID, "trigger", trigger.ID)
		default:
			delivered++
			d.logger.Info("event task queued", "event", event.ID, "type", event.Type, "trigger", trigger.ID, "task", task.ID)
		}
	}
	if failed != "" {
		d.retry(finish, event, failed)
		return
	}
	if err := d.store.MarkEventDelivered(finish, event.ID); err != nil {
		// The work is done; the marker is not. The ledger keeps the redelivery
		// harmless, so this is logged rather than retried differently.
		d.logger.Error("event could not be marked delivered", "event", event.ID, "error", err)
		return
	}
	if delivered > 0 {
		d.logger.Info("event delivered", "event", event.ID, "type", event.Type, "subscribers", delivered)
	}
}

// retry schedules another attempt, or parks the event when its budget is spent.
func (d *Dispatcher) retry(ctx context.Context, event store.PlatformEvent, reason string) {
	if event.Attempts >= d.MaxAttempts {
		if err := d.store.DeadLetterEvent(ctx, event.ID, reason); err != nil {
			d.logger.Error("event could not be dead-lettered", "event", event.ID, "error", err)
			return
		}
		d.logger.Error("event delivery gave up", "event", event.ID, "type", event.Type, "attempts", event.Attempts, "reason", reason)
		// Nobody is watching the dispatcher, so the owner is told: an event that
		// never arrived is otherwise indistinguishable from one nothing subscribed
		// to.
		if err := d.store.CreateNotification(ctx, event.OwnerID, "event",
			"이벤트를 전달하지 못했습니다", event.Type+" 이벤트 전달을 "+strconv.Itoa(event.Attempts)+"회 시도한 뒤 중단했습니다: "+reason, "/tasks"); err != nil {
			d.logger.Warn("dead-letter notification not delivered", "event", event.ID, "error", err)
		}
		return
	}
	delay := eventBackoff(event.Attempts)
	if err := d.store.RescheduleEvent(ctx, event.ID, delay, reason); err != nil {
		d.logger.Error("event could not be rescheduled", "event", event.ID, "error", err)
		return
	}
	d.logger.Warn("event delivery will be retried", "event", event.ID, "type", event.Type,
		"attempt", event.Attempts, "delaySeconds", delay.Seconds(), "reason", reason)
}

// eventBackoff grows with the attempt so a database or a subscriber that is
// having a bad minute is not hammered, and is capped so an event still lands
// within a useful window.
func eventBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := 5 * math.Pow(2, float64(attempt-1))
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// eventInput hands the agent the event that woke it. Without this the agent
// would be told to react to something it cannot see.
func eventInput(template string, event store.PlatformEvent) string {
	var b strings.Builder
	if strings.TrimSpace(template) != "" {
		b.WriteString(template)
		b.WriteString("\n\n")
	}
	b.WriteString("# 발생한 이벤트\n")
	b.WriteString("- 종류: ")
	b.WriteString(event.Type)
	b.WriteString("\n")
	if event.SubjectType != "" {
		b.WriteString("- 대상: ")
		b.WriteString(event.SubjectType)
		b.WriteString(" ")
		b.WriteString(event.SubjectID)
		b.WriteString("\n")
	}
	b.WriteString("- 발생 시각: ")
	b.WriteString(event.CreatedAt.Format(time.RFC3339))
	b.WriteString("\n")
	if payload := eventPayload(event.Payload); payload != "" {
		b.WriteString("- 상세:\n")
		b.WriteString(payload)
		b.WriteString("\n")
	}
	return b.String()
}

// eventPayload renders the payload as readable lines rather than raw JSON,
// since it goes into a prompt.
func eventPayload(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString("  - ")
		b.WriteString(key)
		b.WriteString(": ")
		switch value := fields[key].(type) {
		case string:
			b.WriteString(value)
		default:
			encoded, _ := json.Marshal(value)
			b.Write(encoded)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortStrings keeps the rendered payload stable, so the same event always
// produces the same prompt.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
