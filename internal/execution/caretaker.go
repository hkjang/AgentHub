package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Caretaker keeps the execution plane's own tables honest.
//
// Two things went wrong quietly before it existed. A task claimed by a worker
// that then died stayed at 'running' forever — the claim carried a lease, but
// nothing ever reaped one, and the claim query only looks at queued and retrying
// rows, so the task was stranded where nothing would find it again. And nothing
// ever removed finished history, so the largest tables on a busy deployment grew
// without bound until somebody noticed the disk.
//
// It runs on every worker. Every operation here is idempotent SQL, so two
// caretakers doing the same sweep at the same moment is not a problem worth
// coordinating away.
type Caretaker struct {
	store  *store.Store
	logger *slog.Logger
	// ReclaimGrace is how far past its lease a claim must be before the task is
	// taken back. It is generous on purpose: reclaiming a task whose worker is
	// merely slow would run it twice.
	ReclaimGrace time.Duration
	// ReclaimInterval is how often stranded tasks are looked for, and
	// SweepInterval how often history is trimmed.
	ReclaimInterval time.Duration
	SweepInterval   time.Duration
	// ForgetStoppedAfter is how long a cleanly stopped worker's row is kept, so
	// the list shows the deployment as it is now rather than every process that
	// ever ran.
	ForgetStoppedAfter time.Duration
}

func NewCaretaker(db *store.Store, logger *slog.Logger) *Caretaker {
	return &Caretaker{
		store: db, logger: logger,
		ReclaimGrace: time.Minute, ReclaimInterval: 30 * time.Second,
		SweepInterval: time.Hour, ForgetStoppedAfter: 24 * time.Hour,
	}
}

func (c *Caretaker) Run(ctx context.Context) error {
	reclaim := time.NewTicker(c.ReclaimInterval)
	defer reclaim.Stop()
	sweep := time.NewTicker(c.SweepInterval)
	defer sweep.Stop()
	c.logger.Info("execution caretaker started",
		"reclaimSeconds", c.ReclaimInterval.Seconds(), "sweepMinutes", c.SweepInterval.Minutes())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reclaim.C:
			c.reclaim(ctx)
		case <-sweep.C:
			c.expireSessions(ctx)
			c.sweep(ctx)
		}
	}
}

func (c *Caretaker) reclaim(ctx context.Context) {
	count, err := c.store.ReclaimStuckTasks(ctx, c.ReclaimGrace)
	if err != nil {
		c.logger.Warn("stuck tasks could not be reclaimed", "error", err)
		return
	}
	if count > 0 {
		// Worth a warning rather than an info line: a reclaim means a worker died
		// holding work, which is the thing an operator wants to know about.
		c.logger.Warn("reclaimed tasks from workers that stopped responding", "tasks", count)
	}
	if forgotten, err := c.store.ForgetStoppedWorkers(ctx, c.ForgetStoppedAfter); err != nil {
		c.logger.Debug("stopped workers could not be forgotten", "error", err)
	} else if forgotten > 0 {
		c.logger.Info("removed records of stopped workers", "workers", forgotten)
	}
}

// expireSessions clears out sessions that can no longer log anybody in.
//
// It runs whether or not retention is configured, because it is not retention: an
// expired session is not a record somebody might want, it is a row that cannot
// authenticate anything and sits in the index every authenticated request reads.
func (c *Caretaker) expireSessions(ctx context.Context) {
	removed, err := c.store.SweepExpiredSessions(ctx, c.ForgetStoppedAfter)
	if err != nil {
		c.logger.Warn("expired sessions could not be removed", "error", err)
		return
	}
	if removed > 0 {
		c.logger.Info("removed expired sessions", "sessions", removed)
	}
}

// sweep trims history according to the configured retention.
//
// Retention is off unless an administrator sets it: deleting a deployment's
// history because nobody chose a number would be the wrong default in exactly the
// environments this platform runs in.
func (c *Caretaker) sweep(ctx context.Context) {
	var settings store.OperationsSettings
	if err := c.store.Setting(ctx, store.OperationsSettingKey, &settings); err != nil {
		return
	}
	if settings.Retention == (store.RetentionPolicy{}) {
		return
	}
	if err := settings.Retention.Validate(); err != nil {
		c.logger.Warn("retention policy is invalid; skipping the sweep", "error", err)
		return
	}
	result, err := c.store.Cleanup(ctx, settings.Retention, false)
	if err != nil {
		c.logger.Warn("history sweep failed", "error", err)
		return
	}
	total := 0
	for _, count := range result.Counts {
		total += count
	}
	if total > 0 {
		c.logger.Info("swept history past its retention", "removed", result.Counts)
	}
}
