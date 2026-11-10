package api

import (
	"context"
	"log"
	"runtime"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

// Every closure reads its source at call time: SetEntitlements may run after New, and settings or
// plan changes apply on the next launch.
func (s *Server) wireLimits(ctx context.Context) {
	saas := entitlements.SaaSMode()
	cpus := runtime.NumCPU()
	s.taskDispatcher.WithWorkspaceConcurrency(s.concurrencyLimitForWorkspace)
	s.taskDispatcher.WithRunQuota(serverRunQuota{s})
	s.taskDispatcher.WithRunDeadline(s.runDeadline)
	s.taskDispatcher.WithMaxQueueMinutes(func() int {
		if s.settingsStore == nil {
			return 0
		}
		return s.settingsStore.AgentMaxQueueMinutes(ctx)
	})
	if s.taskStore != nil {
		s.taskStore.WithStorageQuota(s.storageQuotaForWorkspace)
	}
	configured := func() int {
		if s.settingsStore == nil {
			return 0
		}
		n, err := s.settingsStore.AgentMaxContainers(ctx)
		if err != nil {
			return 0
		}
		return n
	}
	if saas && configured() <= 0 {
		log.Printf("[dispatcher] %s", i18n.Log("api.server.log.saas_agent_cap",
			"cap", settings.EffectiveAgentCap(0, true, cpus), "cpus", cpus))
	}
	s.taskDispatcher.WithAgentCap(func() int {
		return settings.EffectiveAgentCap(configured(), saas, cpus)
	})
}

// Fails open: a billing read error must not stall every board.
func (s *Server) concurrencyLimitForWorkspace(workspaceID string) int {
	if s.entitlements == nil || workspaceID == "" {
		return 0
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return 0
	}
	return ent.ConcurrencyLimit
}

func (s *Server) storageQuotaForWorkspace(workspaceID string) int64 {
	if s.entitlements == nil || workspaceID == "" {
		return 0
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return 0
	}
	return ent.StorageQuotaBytes
}

type serverRunQuota struct{ s *Server }

func (q serverRunQuota) ConsumeRun(ctx context.Context, workspaceID string) (entitlements.RunUsage, bool, error) {
	if q.s.entitlements == nil {
		return entitlements.RunUsage{}, true, nil
	}
	return q.s.entitlements.ConsumeRun(ctx, workspaceID)
}

func (q serverRunQuota) RefundRun(ctx context.Context, workspaceID, period string) error {
	if q.s.entitlements == nil {
		return nil
	}
	return q.s.entitlements.RefundRun(ctx, workspaceID, period)
}

func (s *Server) runDeadline(workspaceID string) time.Duration {
	if s.settingsStore == nil {
		if entitlements.SaaSMode() {
			return settings.SaaSDefaultMaxRunMinutes * time.Minute
		}
		return 0
	}
	return s.settingsStore.WorkspaceMaxRunDuration(context.Background(), workspaceID, entitlements.SaaSMode())
}

// The grace lets the dispatcher's own stop, which posts the explanation, normally fire before the
// kubelet's backstop.
const podDeadlineGrace = time.Minute

func podDeadlineSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + podDeadlineGrace) / time.Second)
}
