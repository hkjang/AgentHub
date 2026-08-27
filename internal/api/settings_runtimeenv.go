package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/hkjang/AgentHub/internal/korean"
	"github.com/hkjang/AgentHub/internal/runtime"
)

// Pushing the runtime environment to runtimes that already exist.
//
// The platform-wide files and variables are copied into each runtime's object
// when that object is written, which meant saving the setting changed nothing
// anyone could see: every Pod kept the environment it was created with until
// somebody happened to restart it, and nothing said so. An administrator adding
// /etc/pip.conf on an offline site would watch their agents keep failing to
// install anything and reasonably conclude the feature did not work.
//
// Saving now rewrites the object of every runtime that has one. The operator
// folds the files into the Pod template hash, so a Pod whose content actually
// changed rolls, and one whose content did not is left alone.

// runtimeEnvironmentSync is how long the push may take before the save answers
// anyway. The save itself is already committed by then and the caretaker path —
// the next start of each runtime — still applies the setting, so a slow cluster
// delays the rollout rather than losing it.
const runtimeEnvironmentSync = 20 * time.Second

// syncRuntimeEnvironment rewrites every existing runtime object from the current
// settings and reports what happened.
func (s *Server) syncRuntimeEnvironment(ctx context.Context) (result syncResult) {
	runtimes, err := s.store.ProvisionedRuntimes(ctx)
	if err != nil {
		s.logger.Warn("runtimes could not be listed for a runtime environment sync", "error", err)
		return result
	}
	for _, rt := range runtimes {
		agent, err := s.store.AgentByID(ctx, rt.AgentID, rt.OwnerID, true)
		if err != nil {
			result.failed++
			continue
		}
		spec, err := s.runtimeSpecContext(ctx, rt, agent)
		if err != nil {
			result.failed++
			s.logger.Warn("runtime spec could not be built for a sync", "runtime", rt.ID, "error", err)
			continue
		}
		switch err := s.spawner.Sync(ctx, spec); {
		case err == nil:
			result.applied++
		case errors.Is(err, runtime.ErrNotConfigured):
			// No cluster configured: nothing to push, and nothing wrong either.
			result.skipped++
		case errors.Is(err, runtime.ErrProvisioningUnsupported):
			// The cluster accepted the write and dropped the environment from it.
			// Every runtime will do the same, so there is nothing to learn from
			// pushing the rest.
			result.pruned = true
			s.logger.Error("the AgentRuntime CRD is older than this control plane; the runtime environment is being pruned",
				"runtime", rt.ID)
			return result
		default:
			result.failed++
			s.logger.Warn("runtime environment could not be pushed", "runtime", rt.ID, "error", err)
		}
		if ctx.Err() != nil {
			return result
		}
	}
	return result
}

// syncResult is what one push accomplished.
type syncResult struct {
	applied, skipped, failed int
	// pruned means the cluster's CRD schema is older than this control plane and
	// silently drops the environment. It is not one runtime's problem, so it stops
	// the push and is reported on its own.
	pruned bool
}

// runtimeEnvironmentApplied describes the push for the person who just saved.
//
// The wording matters: "저장했습니다" alone is what made this feature look
// broken. What an administrator needs to know is whether the Pods they are
// looking at now carry the file, and Pods restart to pick it up.
func runtimeEnvironmentApplied(result syncResult) map[string]any {
	applied, skipped, failed := result.applied, result.skipped, result.failed
	message := "저장했습니다. 실행 중인 런타임은 없습니다 — 다음에 시작할 때 적용됩니다."
	switch {
	case result.pruned:
		message = "저장했지만 Kubernetes가 이 설정을 버리고 있습니다. 클러스터의 AgentRuntime CRD가 오래되었습니다 — deploy/kubernetes/crd.yaml을 다시 적용한 뒤 저장하세요."
	case applied > 0 && failed > 0:
		// The particle follows the count, and plural() has always ended in 개 —
		// so this shipped "2개은 실패했습니다" to every administrator who saved
		// while one runtime was unreachable.
		message = "저장했습니다. 런타임 " + plural(applied) + "에 적용했고 " + plural(failed) +
			korean.Topic(plural(failed)) + " 실패했습니다. 적용된 Pod는 새 설정으로 재시작됩니다."
	case applied > 0:
		message = "저장했습니다. 런타임 " + plural(applied) + "에 적용했습니다. 해당 Pod는 새 설정으로 재시작됩니다."
	case failed > 0:
		message = "저장했지만 런타임 " + plural(failed) + "에 적용하지 못했습니다. 로그를 확인하고, 런타임을 재시작하면 적용됩니다."
	case skipped > 0:
		message = "저장했습니다. Kubernetes가 연결되어 있지 않아 지금 적용할 런타임은 없습니다."
	}
	return map[string]any{"applied": applied, "skipped": skipped, "failed": failed, "crdOutdated": result.pruned, "message": message}
}

// plural formats a count for the message above. Korean has no plural form, so
// this is only about not writing "1 개".
func plural(count int) string { return strconv.Itoa(count) + "개" }
