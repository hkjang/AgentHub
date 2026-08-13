package api

import (
	"context"
	"errors"
	"time"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
)

func (s *Server) RunBackground(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cullIdleRuntimes(ctx)
			}
		}
	}()
}

func (s *Server) cullIdleRuntimes(ctx context.Context) {
	items, err := s.store.IdleRuntimeCandidates(ctx)
	if err != nil {
		s.logger.Warn("find idle runtimes", "error", err)
		return
	}
	for _, item := range items {
		instance, runtimeErr := s.store.RuntimeByID(ctx, item.RuntimeID, item.OwnerID, true)
		agent, agentErr := s.store.AgentByID(ctx, item.AgentID, item.OwnerID, true)
		if runtimeErr != nil || agentErr != nil {
			continue
		}
		spec, specErr := s.runtimeSpecContext(ctx, instance, agent)
		if specErr != nil {
			s.logger.Warn("compile idle runtime spec", "runtime", item.RuntimeID, "error", specErr)
			continue
		}
		if stopErr := s.spawner.Stop(ctx, spec); stopErr != nil && !errors.Is(stopErr, appRuntime.ErrNotConfigured) {
			s.logger.Warn("idle culling failed", "runtime", item.RuntimeID, "error", stopErr)
			continue
		}
		if _, updateErr := s.store.UpdateRuntimeDesiredState(ctx, item.RuntimeID, item.OwnerID, "stopped", true); updateErr != nil {
			s.logger.Warn("record idle culling", "runtime", item.RuntimeID, "error", updateErr)
			continue
		}
		s.store.Audit(ctx, nil, "runtime.idle_cull", "runtime", item.RuntimeID, "success", "", map[string]any{"agentId": item.AgentID})
	}
}
