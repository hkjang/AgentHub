package execution

import (
	"context"
	"errors"
	"strings"
	"time"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// acquiredRuntime records what a task got and whether the task is responsible
// for it. A runtime that was already running belongs to the user, so the task
// must not stop it when it finishes.
type acquiredRuntime struct {
	runtimeID string
	// startedByTask is true only when this task spawned or started the runtime.
	startedByTask bool
}

// runtimeReadyTimeout bounds how long a task waits for a Pod. Beyond this the
// attempt fails as retryable rather than holding a worker slot indefinitely.
const runtimeReadyTimeout = 3 * time.Minute

// acquireRuntime reuses the agent's existing Runtime when one is already up and
// spawns one otherwise. It deliberately goes through the same Runtime Manager the
// interactive path uses, so a task and a user share one Runtime rather than the
// execution plane growing a parallel Kubernetes implementation.
func (o *Orchestrator) acquireRuntime(ctx context.Context, run store.AgentRun, agent store.Agent) (*acquiredRuntime, error) {
	existing, err := o.store.LatestRuntimeForAgent(ctx, agent.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if err == nil && isReady(existing.Status) && existing.DesiredState == "running" {
		// A runtime the pool warmed was started for this work, so the task owns
		// it and must be allowed to release it; one a person started is theirs.
		warmed := existing.WarmUntil != nil
		if warmed {
			// Extend the hold past this run so the pool cannot stop it mid-task.
			if _, err := o.store.ClaimWarmRuntime(ctx, existing.ID, time.Now().Add(runtimeReadyTimeout)); err != nil {
				o.logger.Warn("warm hold not extended", "runtime", existing.ID, "error", err)
			}
		}
		o.event(ctx, run, "runtime.reused", "이미 실행 중인 Runtime을 재사용합니다.", map[string]any{"runtimeId": existing.ID, "podName": existing.PodName, "warm": warmed})
		return &acquiredRuntime{runtimeID: existing.ID, startedByTask: warmed}, nil
	}

	spec, instance, err := o.runtimeSpec(ctx, agent, existing, errors.Is(err, store.ErrNotFound))
	if err != nil {
		return nil, err
	}
	o.event(ctx, run, "runtime.acquiring", "Runtime을 시작합니다.", map[string]any{"runtimeId": instance.ID, "runtimeType": agent.RuntimeType})

	if instance.DesiredState != "running" || !isReady(instance.Status) {
		if err := o.spawner.Start(ctx, spec); err != nil {
			if errors.Is(err, appRuntime.ErrNotConfigured) {
				// Kubernetes is not wired up; the task still runs, just without a
				// Runtime, which is the same contract the interactive path has.
				o.event(ctx, run, "runtime.unavailable", "Kubernetes가 구성되지 않아 Runtime 없이 진행합니다.", nil)
				return nil, nil
			}
			return nil, err
		}
		if _, err := o.store.UpdateRuntimeDesiredState(ctx, instance.ID, agent.OwnerID, "running", true); err != nil {
			return nil, err
		}
	}

	if err := o.waitForRuntime(ctx, run, spec, instance.ID); err != nil {
		return nil, err
	}
	return &acquiredRuntime{runtimeID: instance.ID, startedByTask: true}, nil
}

// runtimeSpec resolves the spawn specification, creating the Runtime record when
// the agent has never been started.
func (o *Orchestrator) runtimeSpec(ctx context.Context, agent store.Agent, existing store.Runtime, needsCreate bool) (appRuntime.Spec, store.Runtime, error) {
	instance := existing
	if needsCreate {
		created, err := o.store.CreateRuntime(ctx, agent, "pending")
		if err != nil {
			return appRuntime.Spec{}, store.Runtime{}, err
		}
		instance = created
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	return spec, instance, err
}

// waitForRuntime polls until the Pod reports ready, recording the observed state
// so the run timeline shows the wait rather than an unexplained gap.
func (o *Orchestrator) waitForRuntime(ctx context.Context, run store.AgentRun, spec appRuntime.Spec, runtimeID string) error {
	deadline := time.Now().Add(runtimeReadyTimeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		status, err := o.spawner.Status(ctx, spec)
		if err == nil {
			_ = o.store.UpdateRuntimeObserved(ctx, runtimeID, status.Phase, status.PodName, status.NodeName, status.Endpoint, status.RestartCount, status.FailureReason)
			if isReady(status.Phase) {
				o.event(ctx, run, "runtime.ready", "Runtime이 준비되었습니다.", map[string]any{"runtimeId": runtimeID, "podName": status.PodName})
				return nil
			}
			if strings.EqualFold(status.Phase, "Failed") {
				return errors.New("Runtime이 실패 상태입니다: " + status.FailureReason)
			}
		} else if errors.Is(err, appRuntime.ErrNotConfigured) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("Runtime이 준비되기를 기다리다 시간이 초과되었습니다")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// releaseRuntime applies the agent's policy after the task finishes. A Runtime
// the task did not start is left alone: the user may be working in it.
func (o *Orchestrator) releaseRuntime(ctx context.Context, run store.AgentRun, agent store.Agent, goal store.AgentGoal, acquired *acquiredRuntime) {
	if acquired == nil || !goal.StopAfterTask {
		return
	}
	if !acquired.startedByTask {
		o.event(ctx, run, "runtime.retained", "사용자가 이미 사용 중이던 Runtime이라 중지하지 않았습니다.", map[string]any{"runtimeId": acquired.runtimeID})
		return
	}
	// Hold it instead of stopping it when the agent asks for a warm window: a
	// burst of tasks then pays the start cost once rather than per task. The
	// pool stops it when the hold expires and nothing is queued.
	if goal.KeepWarmSeconds > 0 {
		until := time.Now().Add(time.Duration(goal.KeepWarmSeconds) * time.Second)
		if _, err := o.store.ClaimWarmRuntime(ctx, acquired.runtimeID, until); err != nil {
			o.logger.Warn("warm hold not recorded", "runtime", acquired.runtimeID, "error", err)
		} else {
			o.event(ctx, run, "runtime.kept_warm", "다음 작업을 위해 Runtime을 유지합니다.",
				map[string]any{"runtimeId": acquired.runtimeID, "warmUntil": until})
			return
		}
	}
	instance, err := o.store.RuntimeByID(ctx, acquired.runtimeID, agent.OwnerID, true)
	if err != nil {
		o.logger.Warn("runtime could not be released", "runtime", acquired.runtimeID, "error", err)
		return
	}
	spec, err := o.specs.Build(ctx, instance, agent)
	if err != nil {
		o.logger.Warn("runtime release spec failed", "runtime", acquired.runtimeID, "error", err)
		return
	}
	if err := o.spawner.Stop(ctx, spec); err != nil && !errors.Is(err, appRuntime.ErrNotConfigured) {
		o.logger.Warn("runtime stop failed", "runtime", acquired.runtimeID, "error", err)
		return
	}
	if _, err := o.store.UpdateRuntimeDesiredState(ctx, acquired.runtimeID, agent.OwnerID, "stopped", true); err != nil {
		o.logger.Warn("runtime desired state not recorded", "runtime", acquired.runtimeID, "error", err)
	}
	o.event(ctx, run, "runtime.released", "Task 완료 후 Runtime을 중지했습니다.", map[string]any{"runtimeId": acquired.runtimeID})
}

func isReady(phase string) bool {
	switch strings.ToLower(phase) {
	case "running", "ready":
		return true
	}
	return false
}
