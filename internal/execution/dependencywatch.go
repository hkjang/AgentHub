package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/hkjang/AgentHub/internal/agentserver"
	"github.com/hkjang/AgentHub/internal/modelprobe"
	"github.com/hkjang/AgentHub/internal/store"
)

// Keeping what is known about this deployment's dependencies true.
//
// Health was recorded when an administrator pressed a button, and then kept
// forever. A machine verified in March and one verified an hour ago looked
// identical — to the console, and worse, to placement, which prefers a healthy
// server over an unchecked one on the strength of whatever the last answer was.
//
// So the answer is refreshed on a timer. Nothing here decides anything; it only
// keeps the record current, which is what lets the decisions elsewhere be made
// on something recent.
type DependencyWatch struct {
	store  *store.Store
	logger *slog.Logger
	// Interval is how often every registered server is asked again.
	Interval time.Duration
	// Timeout bounds one server's answer, so a machine that accepts connections
	// and never replies cannot hold up the rest of the sweep.
	Timeout time.Duration
}

func NewDependencyWatch(db *store.Store, logger *slog.Logger) *DependencyWatch {
	return &DependencyWatch{store: db, logger: logger, Interval: 5 * time.Minute, Timeout: 10 * time.Second}
}

// Run refreshes what is known until the context ends.
//
// It runs on every worker. Asking one server twice is a GET and an update of the
// same row with the same answer, so two workers sweeping at once is not worth
// coordinating away.
func (w *DependencyWatch) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Minute
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	// Once at startup as well: a deployment that has just come up should not wait
	// out the first interval before it knows anything.
	w.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *DependencyWatch) sweep(ctx context.Context) {
	w.sweepAgentServers(ctx)
	w.sweepModelEndpoints(ctx)
}

// sweepModelEndpoints asks each endpoint whether it is there and still offers
// the model somebody typed.
//
// The question is a listing rather than a completion, so asking it every few
// minutes costs nothing — which is why it can be asked at all. A key rotated
// last week and an endpoint verified this morning used to look identical, and
// the difference showed up as a task failing at night on somebody else's agent.
func (w *DependencyWatch) sweepModelEndpoints(ctx context.Context) {
	endpoints, err := w.store.ModelEndpoints(ctx)
	if err != nil {
		w.logger.Warn("model endpoints could not be read for a health sweep", "error", err)
		return
	}
	for _, endpoint := range endpoints {
		if !endpoint.Enabled || ctx.Err() != nil {
			continue
		}
		// The key is read per endpoint because the listing does not carry it: a
		// credential in a list is a credential in a log line one refactor later.
		_, key, err := w.store.ModelEndpointByID(ctx, endpoint.ID)
		if err != nil {
			continue
		}
		ask, cancel := context.WithTimeout(ctx, w.Timeout)
		verdict, detail, _ := modelprobe.Ask(ask, endpoint.BaseURL, key, endpoint.DefaultModel)
		cancel()
		if err := w.store.RecordModelEndpointHealth(ctx, endpoint.ID, verdict, detail); err != nil {
			w.logger.Warn("a model endpoint's health could not be recorded", "endpoint", endpoint.ID, "error", err)
			continue
		}
		if verdict != endpoint.Health {
			w.logger.Info("a model endpoint's health changed", "endpoint", endpoint.Name,
				"was", endpoint.Health, "now", verdict, "detail", detail)
		}
	}
}

func (w *DependencyWatch) sweepAgentServers(ctx context.Context) {
	servers, err := w.store.AgentServers(ctx)
	if err != nil {
		w.logger.Warn("agent servers could not be read for a health sweep", "error", err)
		return
	}
	for _, server := range servers {
		if !server.Enabled {
			// A server an operator turned off is not asked. Nothing is sent to it,
			// and knocking on it every five minutes would be the platform ignoring
			// the switch.
			continue
		}
		if ctx.Err() != nil {
			return
		}
		ask, cancel := context.WithTimeout(ctx, w.Timeout)
		health, detail := agentserver.Probe(ask, server.BaseURL)
		cancel()
		if err := w.store.RecordAgentServerHealth(ctx, server.ID, health, detail); err != nil {
			w.logger.Warn("an agent server's health could not be recorded", "server", server.ID, "error", err)
			continue
		}
		// Only the changes are worth a line. A working pool would otherwise write
		// one log entry per server every five minutes forever.
		if health != server.Health {
			w.logger.Info("an agent server's health changed", "server", server.Name,
				"was", server.Health, "now", health, "detail", detail)
		}
	}
}
