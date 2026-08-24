package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// Pool keeps runtimes warm around scheduled work.
//
// A scheduled task otherwise pays for a cold Pod: the image is local, but the
// volume, the adapter's init containers and the agent's own startup still take
// most of a minute, so a trigger that fires at 08:00 does not start working at
// 08:00. The pool starts the runtime inside the agent's warm-up window and holds
// it briefly afterwards, so a burst of tasks pays the start cost once.
//
// It only ever touches runtimes it started itself. The claim is recorded on the
// runtime row, and it is dropped the moment a person takes the runtime over, so
// the pool cannot stop a workspace somebody is working in.
type Pool struct {
	store   *store.Store
	spawner appRuntime.Spawner
	specs   specBuilder
	logger  *slog.Logger

	// Interval is how often the pool reconciles.
	Interval time.Duration
	// Batch bounds one pass so a large backlog cannot start dozens of Pods at
	// once and starve the cluster.
	Batch int
}

// specBuilder is the seam the orchestrator already uses to compile a spawn spec.
type specBuilder interface {
	Build(ctx context.Context, instance store.Runtime, agent store.Agent) (appRuntime.Spec, error)
}

func NewPool(db *store.Store, spawner appRuntime.Spawner, specs specBuilder, logger *slog.Logger) *Pool {
	return &Pool{store: db, spawner: spawner, specs: specs, logger: logger, Interval: 15 * time.Second, Batch: 5}
}

func (p *Pool) Run(ctx context.Context) error {
	p.logger.Info("runtime pool started", "intervalSeconds", p.Interval.Seconds(), "batch", p.Batch)
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		p.warm(ctx)
		p.cool(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// warm starts runtimes for schedules that are about to fire.
func (p *Pool) warm(ctx context.Context) {
	candidates, err := p.store.RuntimesToWarm(ctx, p.Batch)
	if err != nil {
		p.logger.Error("warm candidates could not be read", "error", err)
		return
	}
	for _, candidate := range candidates {
		agent, err := p.store.AgentByID(ctx, candidate.AgentID, candidate.OwnerID, true)
		if err != nil {
			p.logger.Warn("warm target agent could not be read", "agent", candidate.AgentID, "error", err)
			continue
		}
		instance, err := p.store.LatestRuntimeForAgent(ctx, agent.ID)
		if errors.Is(err, store.ErrNotFound) {
			created, createErr := p.store.CreateRuntime(ctx, agent, "pending")
			if createErr != nil {
				p.logger.Warn("warm runtime could not be created", "agent", agent.ID, "error", createErr)
				continue
			}
			instance = created
		} else if err != nil {
			p.logger.Warn("warm runtime could not be read", "agent", agent.ID, "error", err)
			continue
		}

		// Claim first. Losing the claim means another worker is already warming
		// this agent, and starting it twice would just fight over the same Pod.
		won, err := p.store.ClaimWarmRuntime(ctx, instance.ID, candidate.WarmUntil)
		if err != nil {
			p.logger.Warn("warm claim failed", "runtime", instance.ID, "error", err)
			continue
		}
		if !won {
			continue
		}

		spec, err := p.specs.Build(ctx, instance, agent)
		if err != nil {
			p.logger.Warn("warm spec could not be built", "runtime", instance.ID, "error", err)
			p.giveBack(ctx, instance.ID)
			continue
		}
		// Warming ahead of a schedule is still starting a runtime, and it was the
		// one path that never asked whether it may. A person held to three runtimes
		// could hold more by scheduling them with a warm-up, and an agent a policy
		// forbids anyone from starting was started by its own nightly schedule a
		// minute before it ran.
		//
		// A quota refusal clears by itself, so the hold is dropped and the next tick
		// tries again — there is still time before the trigger fires. A policy
		// refusal does not clear, and is already recorded where the console's
		// refusals are.
		if refusal, waits := runtimeStartRefusal(ctx, p.store, p.logger, agent, spec.Profile.ID, instance.ID); refusal != "" {
			p.logger.Info("runtime not warmed", "agent", agent.ID, "runtime", instance.ID, "reason", refusal, "waits", waits)
			p.giveBack(ctx, instance.ID)
			continue
		}
		if err := p.spawner.Start(ctx, spec); err != nil {
			if errors.Is(err, appRuntime.ErrNotConfigured) {
				// Without Kubernetes there is nothing to warm, and saying so once
				// per tick would drown the log.
				p.giveBack(ctx, instance.ID)
				continue
			}
			p.logger.Warn("warm start failed", "runtime", instance.ID, "error", err)
			p.giveBack(ctx, instance.ID)
			continue
		}
		// Under the owner's quota, like every other way of taking capacity. The
		// refusal above answers whether this may start; this keeps the answer true
		// at the moment it is taken, so two workers warming at the same tick cannot
		// both take the last runtime.
		if _, err := p.store.StartRuntimeWithinQuota(ctx, instance.ID, agent.OwnerID, spec.Profile.ID, true); err != nil {
			p.logger.Warn("warm desired state not recorded", "runtime", instance.ID, "error", err)
			continue
		}
		p.store.Audit(ctx, nil, "runtime.warmed", "runtime", instance.ID, "success", "", map[string]any{
			"agentId": agent.ID, "triggerId": candidate.TriggerID, "fireAt": candidate.FireAt,
		})
		p.logger.Info("runtime warmed ahead of a schedule", "agent", agent.Name, "runtime", instance.ID,
			"fireAt", candidate.FireAt, "warmUntil", candidate.WarmUntil)
	}
}

// cool stops runtimes whose hold has expired.
func (p *Pool) cool(ctx context.Context) {
	candidates, err := p.store.RuntimesToCool(ctx, p.Batch)
	if err != nil {
		p.logger.Error("cool candidates could not be read", "error", err)
		return
	}
	for _, candidate := range candidates {
		agent, err := p.store.AgentByID(ctx, candidate.AgentID, candidate.OwnerID, true)
		if err != nil {
			p.logger.Warn("cool target agent could not be read", "agent", candidate.AgentID, "error", err)
			continue
		}
		instance, err := p.store.RuntimeByID(ctx, candidate.RuntimeID, candidate.OwnerID, true)
		if err != nil {
			p.logger.Warn("cool target runtime could not be read", "runtime", candidate.RuntimeID, "error", err)
			continue
		}
		spec, err := p.specs.Build(ctx, instance, agent)
		if err != nil {
			p.logger.Warn("cool spec could not be built", "runtime", candidate.RuntimeID, "error", err)
			continue
		}
		// The query above knows about tasks; it cannot know about a person. Three
		// things on this platform stop a runtime nobody asked them to stop, and all
		// three have to ask the same question first.
		if reason, busyErr := p.store.RuntimeBusy(ctx, candidate.RuntimeID, candidate.AgentID, ""); busyErr != nil {
			p.logger.Warn("runtime use could not be checked; leaving it running", "runtime", candidate.RuntimeID, "error", busyErr)
			continue
		} else if reason != "" {
			p.logger.Info("warm runtime kept", "runtime", candidate.RuntimeID, "reason", reason)
			continue
		}
		if err := p.spawner.Stop(ctx, spec); err != nil && !errors.Is(err, appRuntime.ErrNotConfigured) {
			p.logger.Warn("cool stop failed", "runtime", candidate.RuntimeID, "error", err)
			continue
		}
		if _, err := p.store.UpdateRuntimeDesiredState(ctx, candidate.RuntimeID, candidate.OwnerID, "stopped", true); err != nil {
			p.logger.Warn("cool desired state not recorded", "runtime", candidate.RuntimeID, "error", err)
			continue
		}
		// The claim goes with the runtime: leaving it set would make the pool
		// look like it still holds something it has stopped.
		if err := p.store.ReleaseWarmRuntime(ctx, candidate.RuntimeID); err != nil {
			p.logger.Warn("warm claim not released", "runtime", candidate.RuntimeID, "error", err)
		}
		p.store.Audit(ctx, nil, "runtime.cooled", "runtime", candidate.RuntimeID, "success", "", map[string]any{"agentId": agent.ID})
		p.logger.Info("warm runtime stopped", "agent", agent.Name, "runtime", candidate.RuntimeID)
	}
}

// giveBack releases a runtime the pool did not manage to start.
//
// Releasing the hold alone left the row counted as running with no Pod behind
// it — and out of reach of the cooling sweep, which only looks at runtimes that
// still hold a claim. So the state goes back too, but only for a runtime that
// never came up: one that is actually running is somebody's work.
func (p *Pool) giveBack(ctx context.Context, runtimeID string) {
	stopped, err := p.store.AbandonUnstartedRuntime(ctx, runtimeID)
	if err != nil {
		p.logger.Warn("an unstarted runtime could not be given back", "runtime", runtimeID, "error", err)
		return
	}
	if !stopped {
		// It is running after all, so only the pool's own hold is dropped.
		if err := p.store.ReleaseWarmRuntime(ctx, runtimeID); err != nil {
			p.logger.Warn("warm claim not released", "runtime", runtimeID, "error", err)
		}
	}
}
