package api

import (
	"context"
	"log"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	ws, err := s.authStore.GetWorkspace(r.Context(), wsID)
	if err != nil || ws == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.workspace.errors.not_found")
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	if !s.mayPurgeWorkspace(r, claims, ws.ID, ws.OwnerID) {
		writeErrKey(w, r, http.StatusForbidden, "api.workspace.errors.owner_only")
		return
	}

	var body struct {
		Confirm string `json:"confirm"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Confirm != ws.Slug {
		writeErrKey(w, r, http.StatusBadRequest, "api.workspace.errors.confirmation_required")
		return
	}

	if err := s.purgeWorkspace(r.Context(), wsID); err != nil {
		log.Printf("[purge] %s", i18n.Log("api.workspace.log.purge_failed", "workspace", logx.Safe(wsID), "err", err))
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.workspace.errors.erasure_failed"))
		return
	}

	if s.auditStore != nil {
		actorID := "api-key"
		if claims != nil {
			actorID = claims.UserID
		}
		s.auditStore.LogRequest(r, "workspace.deleted", wsID, "workspace",
			i18n.Text("api.workspace.audit.erased", "slug", ws.Slug), map[string]string{"actor": actorID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_id": wsID})
}

func (s *Server) mayPurgeWorkspace(r *http.Request, claims *auth.Claims, wsID, ownerID string) bool {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		if auth.APIKeyRunScope(ctx).IsRun() || auth.APIKeyRole(ctx) != auth.RoleOwner {
			return false
		}
		return auth.APIKeyWorkspace(ctx) == wsID || s.callerIsOperator(r)
	}
	return claims != nil && claims.UserID == ownerID
}

// Each step aborts on failure so a partial purge can be retried; the workspace row goes last.
func (s *Server) purgeWorkspace(ctx context.Context, wsID string) error {
	if err := s.purgeWorkspaceResources(ctx, wsID); err != nil {
		return err
	}
	if s.chatSvc != nil {
		if err := s.chatSvc.PurgeWorkspace(ctx, wsID); err != nil {
			return err
		}
	}
	if err := s.purgeWorkspaceConfig(ctx, wsID); err != nil {
		return err
	}
	// Must run before the identity step: once the workspace row is gone the purge cannot be
	// retried.
	if s.tenantTeardown != nil {
		if err := s.tenantTeardown(ctx, wsID); err != nil {
			return apperr.Wrap(err, "api.workspace.errors.tenant_teardown")
		}
	}
	_, err := s.authStore.DeleteWorkspace(ctx, wsID)
	return err
}

func (s *Server) purgeWorkspaceConfig(ctx context.Context, wsID string) error {
	if s.secretsStore != nil {
		if err := s.secretsStore.DeleteByWorkspace(ctx, wsID); err != nil {
			return err
		}
	}
	if s.credStore != nil {
		if err := s.credStore.DeleteByWorkspace(ctx, wsID); err != nil {
			return err
		}
	}
	if s.webhookStore != nil {
		if err := s.webhookStore.DeleteByWorkspace(ctx, wsID); err != nil {
			return err
		}
	}
	if s.entitlements != nil {
		if err := s.entitlements.DeleteByWorkspace(wsID); err != nil {
			return err
		}
	}
	if s.settingsStore != nil {
		if err := s.settingsStore.DeleteByWorkspace(ctx, wsID); err != nil {
			return err
		}
	}
	if s.gh.store != nil {
		return s.gh.store.DeleteByWorkspace(ctx, wsID)
	}
	return nil
}

func (s *Server) SetTenantTeardown(fn func(ctx context.Context, workspaceID string) error) {
	s.tenantTeardown = fn
}

func (s *Server) purgeWorkspaceResources(ctx context.Context, wsID string) error {
	if s.taskStore != nil {
		tasks, err := s.taskStore.ListByWorkspace(ctx, wsID)
		if err != nil {
			return err
		}
		for i := range tasks {
			if err := s.teardownTask(ctx, &tasks[i]); err != nil {
				return err
			}
		}
	}
	if err := s.purgeWorkspaceRunState(ctx, wsID); err != nil {
		return err
	}
	if s.projectStore != nil {
		projects, err := s.projectStore.ListByWorkspace(ctx, wsID)
		if err != nil {
			return err
		}
		for i := range projects {
			// Grants are not revoked here: the workspace's credentials are erased in
			// purgeWorkspaceConfig.
			if err := s.projectStore.Delete(ctx, projects[i].ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) purgeWorkspaceRunState(ctx context.Context, wsID string) error {
	if s.sandbox.store != nil {
		owners, err := s.sandbox.store.OwnersInWorkspace(ctx, wsID)
		if err != nil {
			return err
		}
		for _, owner := range owners {
			logx.Warn(s.destroySandbox(ctx, owner), "api.workspace.log.destroy_sandbox_failed", "owner", owner)
		}
	}
	if s.usageStore != nil {
		files, err := s.usageStore.DeleteByWorkspace(ctx, wsID)
		if err != nil {
			return err
		}
		s.removeRunFiles(files)
	}
	return nil
}
