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
		was, changed, err := w.store.RecordModelEndpointHealth(ctx, endpoint.ID, verdict, detail)
		if err != nil {
			w.logger.Warn("a model endpoint's health could not be recorded", "endpoint", endpoint.ID, "error", err)
			continue
		}
		if !changed {
			continue
		}
		w.logger.Info("a model endpoint's health changed", "endpoint", endpoint.Name,
			"was", was, "now", verdict, "detail", detail)
		w.announce(ctx, dependencyChange{
			Kind: "모델 엔드포인트", Name: endpoint.Name, Was: was, Now: verdict, Detail: detail,
			Good: verdict == modelprobe.OK, Where: "/admin/models",
		})
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
		was, changed, err := w.store.RecordAgentServerHealth(ctx, server.ID, health, detail)
		if err != nil {
			w.logger.Warn("an agent server's health could not be recorded", "server", server.ID, "error", err)
			continue
		}
		// Only the changes are worth a line. A working pool would otherwise write
		// one log entry per server every five minutes forever.
		if !changed {
			continue
		}
		w.logger.Info("an agent server's health changed", "server", server.Name,
			"was", was, "now", health, "detail", detail)
		w.announce(ctx, dependencyChange{
			Kind: "에이전트 서버", Name: server.Name, Was: was, Now: health, Detail: detail,
			Good: health == agentserver.Healthy, Where: "/admin/agent-servers",
		})
	}
}

// dependencyChange is one thing this deployment depends on becoming, or ceasing
// to be, usable.
type dependencyChange struct {
	Kind   string
	Name   string
	Was    string
	Now    string
	Detail string
	// Good says the new state is a working one. It is the difference between a
	// notice somebody must act on and a notice that closes one they already have.
	Good  bool
	Where string
}

// announce tells the administrators what changed.
//
// The platform knew these things and waited to be asked. An endpoint that stops
// answering at two in the afternoon is discovered at two in the morning, in the
// shape of a task that failed for reasons that read like the agent's fault.
//
// Recoveries are announced too. A deployment that only ever reports breakage
// leaves people chasing something that fixed itself twenty minutes ago, and the
// second notice is what makes the first one trustworthy.
func (w *DependencyWatch) announce(ctx context.Context, change dependencyChange) {
	if !worthAnnouncing(change) {
		return
	}
	admins, err := w.store.AdminIDs(ctx)
	if err != nil {
		w.logger.Warn("administrators could not be read to announce a change", "error", err)
		return
	}
	title := change.Kind + " 이상: " + change.Name
	// Written with an arrow rather than a sentence: the verdicts are nouns of
	// varying endings, and gluing Korean particles onto them produces the kind of
	// half-grammatical line that makes a notice look machine-written.
	message := dependencyWord(change.Was) + " → " + dependencyWord(change.Now) + " 상태가 됐습니다."
	kind := "dependency_down"
	if change.Good {
		title = change.Kind + " 복구: " + change.Name
		message = "다시 정상입니다(이전 상태: " + dependencyWord(change.Was) + ")."
		kind = "dependency_up"
	}
	if change.Detail != "" {
		message += " " + change.Detail
	}
	for _, admin := range admins {
		if err := w.store.CreateNotification(ctx, admin, kind, title, message, change.Where); err != nil {
			w.logger.Warn("a dependency change could not be announced", "to", admin, "error", err)
		}
	}
}

// worthAnnouncing keeps the first sweep from reading like an incident.
//
// A deployment that has just registered its servers learns about all of them at
// once, and every one of those is a change from "확인 전". Learning that a machine
// works is not a recovery — there is nothing to close, and a notice that says
// 복구 for something that was never broken teaches people to ignore the next one.
// Learning that a machine does not work is worth saying whenever it is found.
func worthAnnouncing(change dependencyChange) bool {
	if !change.Good {
		return true
	}
	return change.Was != "" && change.Was != "unknown"
}

// dependencyWord says a verdict in words. The stored values are the checks' own
// vocabulary, and a notification that says "model_missing" is a notification
// written for the platform rather than for the person reading it.
func dependencyWord(verdict string) string {
	switch verdict {
	case "ok", "healthy":
		return "정상"
	case "unknown", "":
		return "확인 전"
	case "unreachable":
		return "연결되지 않음"
	case "refused":
		return "에이전트 서버가 아님"
	case "unauthorised":
		return "인증 거절"
	case "wrong_path":
		return "주소 경로 문제"
	case "model_missing":
		return "지정한 모델 없음"
	case "reachable":
		return "응답하지만 모델 목록이 비어 있음"
	case "unconfigured":
		return "주소 없음"
	case "error":
		return "오류 응답"
	}
	return verdict
}
