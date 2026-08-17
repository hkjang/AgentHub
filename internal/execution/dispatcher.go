package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Dispatcher turns platform events into tasks.
//
// It runs beside the cron Scheduler in the worker, and reads from the same kind
// of durable outbox: an offline deployment has no broker to lean on, and an
// in-memory bus would lose every event a restart caught in flight. Events are
// claimed and marked delivered in one statement, so several workers can run
// this without delivering anything twice.
type Dispatcher struct {
	store  *store.Store
	logger *slog.Logger
	// Interval is how often the outbox is drained.
	Interval time.Duration
	// Batch bounds one pass, so a burst cannot flood the task queue at once.
	Batch int
}

func NewDispatcher(db *store.Store, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{store: db, logger: logger, Interval: 5 * time.Second, Batch: 50}
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
	events, err := d.store.ClaimEvents(ctx, d.Batch)
	if err != nil {
		d.logger.Error("events could not be claimed", "error", err)
		return
	}
	for _, event := range events {
		d.deliver(ctx, event)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, event store.PlatformEvent) {
	triggers, err := d.store.TriggersForEvent(ctx, event)
	if err != nil {
		d.logger.Error("event subscribers could not be read", "event", event.ID, "type", event.Type, "error", err)
		return
	}
	for _, trigger := range triggers {
		title := trigger.TaskTitle
		if title == "" {
			title = trigger.Name
		}
		task, err := d.store.CreateAgentTask(ctx, store.CreateTaskInput{
			AgentID: trigger.AgentID, OwnerID: trigger.OwnerID,
			Title: title, Input: eventInput(trigger.TaskInput, event), Priority: trigger.Priority,
			Source: "event", TriggerID: &trigger.ID, CreatedBy: trigger.OwnerID,
		})
		if err != nil {
			d.logger.Error("event task could not be created", "event", event.ID, "trigger", trigger.ID, "error", err)
			continue
		}
		d.logger.Info("event task queued", "event", event.ID, "type", event.Type, "trigger", trigger.ID, "task", task.ID)
	}
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
