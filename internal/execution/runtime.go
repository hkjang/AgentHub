package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/quota"
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
		o.store.TouchRuntime(ctx, existing.ID)
		return &acquiredRuntime{runtimeID: existing.ID, startedByTask: warmed}, nil
	}

	spec, instance, err := o.runtimeSpec(ctx, agent, existing, errors.Is(err, store.ErrNotFound))
	if err != nil {
		return nil, err
	}
	// Starting a runtime from a task went around both the limits and the rules
	// that govern starting one from the console. A person held to three runtimes
	// could hold thirty by scheduling them, and an agent a policy forbids anyone
	// from starting could be started by its own nightly job. Neither was decided;
	// the interactive path grew the checks and this one never picked them up.
	if refusal, waits := o.runtimeRefusal(ctx, agent, spec.Profile.ID, instance.ID); refusal != "" {
		o.event(ctx, run, "runtime.refused", refusal, map[string]any{"agentId": agent.ID, "waits": waits})
		if waits {
			// A limit clears when somebody else's runtime stops, so this is a wait
			// rather than a failure. Failing here spent one of the task's attempts
			// on a number that had nothing to do with the task, and a retry budget
			// exhausted against a busy afternoon leaves the work undone for a
			// reason nobody would defend.
			return nil, fmt.Errorf("%w: %s", ErrRuntimeQuota, refusal)
		}
		return nil, errors.New(refusal)
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
		// Marked running under the owner's quota rather than after asking about it:
		// the refusal above answers "may this start", and this is what makes the
		// answer still true at the moment the capacity is taken.
		if _, err := o.store.StartRuntimeWithinQuota(ctx, instance.ID, agent.OwnerID, spec.Profile.ID, true); err != nil {
			if errors.Is(err, quota.ErrExceeded) {
				// A limit that filled between the two is a wait, not a failure, for
				// the same reason the refusal above is.
				return nil, fmt.Errorf("%w: %s", ErrRuntimeQuota, err.Error())
			}
			return nil, err
		}
	}

	if err := o.waitForRuntime(ctx, run, spec, instance.ID); err != nil {
		return nil, err
	}
	// A runtime doing work for a task is not idle, and until now nothing in the
	// execution plane ever said so: last_activity_at was written only by a person's
	// browser session, so the idle culler measured a task's runtime from the moment
	// it started and stopped it mid-run once the profile's timeout passed. A short
	// idle timeout is exactly what an operator sets to save cluster money, and it
	// turned every long run into a runtime that failed for no stated reason.
	o.store.TouchRuntime(ctx, instance.ID)
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
	// The last thing the cluster said about why this Pod is not up. A wrong image
	// tag reads "런타임 이미지를 가져오지 못했습니다 … ErrImagePull" here every
	// tick, and the task used to report only that it had waited three minutes —
	// so the answer was written down and then thrown away.
	reason := ""
	for {
		status, err := o.spawner.Status(ctx, spec)
		if err == nil {
			if trimmed := strings.TrimSpace(status.FailureReason); trimmed != "" {
				reason = trimmed
			}
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
			return errors.New(runtimeWaitTimeout(reason))
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
	// Who started it is not the same question as who is in it now. An agent
	// allowed concurrent runs reuses the runtime the first task started, and the
	// first task to finish was stopping the Pod under the others; a person who
	// opened a terminal after the task started it lost their window the moment the
	// task ended. Both are somebody else's work in a runtime this task believes it
	// owns, and neither was asked about.
	if reason, err := o.store.RuntimeBusy(ctx, acquired.runtimeID, agent.ID, run.TaskID); err != nil {
		// A question that could not be answered is not a yes. Leaving a runtime up
		// costs money; stopping one out from under a person or a running task costs
		// their work.
		o.logger.Warn("runtime use could not be checked; leaving it running", "runtime", acquired.runtimeID, "error", err)
		return
	} else if reason != "" {
		o.event(ctx, run, "runtime.retained", reason+"라 중지하지 않았습니다.", map[string]any{"runtimeId": acquired.runtimeID})
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

// runtimeRefusal applies to a task's runtime the same two questions the console
// asks before starting one: is the owner (and their department) within the
// limits they were given, and does the platform's policy allow this agent to
// start at all.
//
// A refusal that cannot be evaluated is not a refusal. Both checks follow the
// rule the quota and the promotion gate already follow — a transient failure to
// read them lets the work through, because turning a query error into a blocked
// deployment is worse than the thing being guarded against.
// ErrRuntimeQuota parks a task whose runtime cannot start yet because a limit is
// full. It is a sentinel for the same reason the approval one is: the worker has
// to put the task back on the queue rather than record an attempt against it.
var ErrRuntimeQuota = errors.New("runtime quota is full")

func (o *Orchestrator) runtimeRefusal(ctx context.Context, agent store.Agent, profileID, runtimeID string) (refusal string, waits bool) {
	return runtimeStartRefusal(ctx, o.store, o.logger, agent, profileID, runtimeID)
}

// runtimeStartRefusal answers "may this runtime start", for whoever is asking.
//
// Three things on this platform start a runtime nobody pressed a button for: a
// task acquiring one, the warm pool starting one ahead of a schedule, and the
// console doing it on somebody's behalf. Starting from a task once went around
// both the limits and the rules that govern starting from the console; that was
// fixed by asking here. The pool was the third path and was never asked at all,
// so a person held to three runtimes could hold more by scheduling them with a
// warm-up, and an agent a policy forbids anyone from starting was started by its
// own nightly schedule a minute before it ran.
//
// It is a package function rather than a method because the pool is not an
// orchestrator, and the question is the same question.
func runtimeStartRefusal(ctx context.Context, db *store.Store, logger *slog.Logger, agent store.Agent, profileID, runtimeID string) (refusal string, waits bool) {
	// The record for this runtime already exists — it is created before the
	// profile is known — so it has to be left out of what is currently held. It
	// was not, and the check counted the runtime it was deciding about: with a
	// limit of one, a task waited forever behind itself.
	if err := db.CheckRuntimeQuotaExcept(ctx, agent.OwnerID, profileID, runtimeID); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			// The message already names the scope that refused — 사용자 or 부서 — which
			// is the part that decides what somebody does about it. And it clears
			// by itself, which is what makes this a wait.
			return err.Error(), true
		}
		logger.Warn("runtime quota is unreadable; starting the runtime", "agent", agent.ID, "error", err)
		return "", false
	}
	var document policy.Document
	if err := db.Setting(ctx, policy.SettingKey, &document); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			logger.Warn("policy document is unreadable; starting the runtime", "agent", agent.ID, "error", err)
		}
		return "", false
	}
	if len(document.Rules) == 0 {
		return "", false
	}
	owner, err := db.UserByID(ctx, agent.OwnerID)
	if err != nil {
		logger.Warn("runtime owner is unreadable; starting the runtime", "agent", agent.ID, "error", err)
		return "", false
	}
	decision := policy.Evaluate(document, policy.Request{
		Action: policy.ActionRuntimeStart, Role: owner.Role, User: owner.Username, UserID: owner.ID,
		Agent: agent.Name, AgentID: agent.ID,
	})
	if decision.Allowed() {
		return "", false
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "플랫폼 정책이 이 Agent의 Runtime 시작을 허용하지 않습니다."
	}
	db.Audit(ctx, &owner, "policy."+policy.ActionRuntimeStart, "policy", decision.RuleID, "denied", "",
		map[string]any{"effect": decision.Effect, "agent": agent.Name, "agentId": agent.ID})
	// A policy refusal does not clear on its own: somebody has to change a rule.
	return reason, false
}

// runtimeWaitTimeout says why the Pod never came up, when the cluster said.
//
// Waiting is the right behaviour even for an image that cannot be pulled — a
// registry recovers, and Kubernetes keeps trying — but reporting only that the
// wait ended sends somebody to look for a slow cluster when the answer, already
// recorded on the runtime, is a tag that does not exist.
func runtimeWaitTimeout(reason string) string {
	message := "Runtime이 준비되기를 기다리다 시간이 초과되었습니다"
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		if len(trimmed) > 300 {
			trimmed = trimmed[:300] + "…"
		}
		return message + ": " + trimmed
	}
	return message
}
