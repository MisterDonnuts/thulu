package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

func (h *Handler) WithTaskTitles(fn func(ctx context.Context, taskIDs []string) map[string]string) *Handler {
	h.taskTitles = fn
	return h
}

func (h *Handler) WithStageLabels(fn func(ctx context.Context, taskID, stage string) string) *Handler {
	h.stageLabels = fn
	return h
}

const DefaultRotateGrace = 24 * time.Hour

const MaxRotateGrace = 7 * 24 * time.Hour

type rotateAPIKeyRequest struct {
	GraceHours *int `json:"grace_hours"`
}

// The old key is retired by moving its expiry, not a scheduled revoke: the middleware already
// refuses expired keys.
func (h *Handler) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.isAdminOrAPIKey(r) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.admin_or_key_required"))
		return
	}
	if reason := keyManagementDenial(r); reason != nil {
		apperr.WriteJSON(w, r, http.StatusForbidden, reason)
		return
	}
	var req rotateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	grace, gerr := rotateGrace(req.GraceHours)
	if gerr != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, gerr)
		return
	}
	creatorID, ws, callerRole, ok := h.keyCaller(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	now := time.Now()
	old, status, kerr := h.rotatableKey(ctx, r.PathValue("id"), ws, now)
	if kerr != nil {
		apperr.WriteJSON(w, r, status, kerr)
		return
	}
	grant, rerr := rotationGrant(old, ws, callerRole, callerPermissions(r))
	// A key-initiated rotation never outlives the calling key (same rule as mint), shortened
	// instead of refused.
	if parent := apiKeyExpiry(ctx); rerr == nil && parent != nil &&
		(grant.ExpiresIn == 0 || grant.ExpiresIn > time.Until(*parent)) {
		rerr = capToParentExpiry(&grant, false, parent)
	}
	if rerr != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, rerr)
		return
	}
	rawKey, meta, err := h.store.CreateAPIKey(ctx, old.Name, creatorID, grant)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.rotate_api_key_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	until := now.Add(grace)
	if old.ExpiresAt != nil && old.ExpiresAt.Before(until) {
		until = *old.ExpiresAt
	}
	if _, err := h.store.RetireAPIKey(ctx, old.ID, ws, meta.ID, until); err != nil {
		if rerr := h.store.RevokeAPIKey(ctx, meta.ID); rerr != nil {
			log.Printf("[auth] %s", i18n.Log("auth.log.rotate_api_key_failed", "err", rerr))
		}
		log.Printf("[auth] %s", i18n.Log("auth.log.rotate_api_key_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	summary := i18n.Text("auth.audit.api_key_rotated_now", "name", old.Name)
	if until.After(now) {
		summary = i18n.Text("auth.audit.api_key_rotated", "name", old.Name,
			"until", until.UTC().Format(AuditTimeLayout))
	}
	h.auditRequest(r, "auth.api_key_rotated", ws, meta.ID, "api_key", summary)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":             meta.ID,
		"name":           meta.Name,
		"prefix":         meta.Prefix,
		"key":            rawKey,
		"created_at":     meta.CreatedAt,
		"expires_at":     meta.ExpiresAt,
		"workspace_id":   meta.WorkspaceID,
		"role":           meta.Role,
		"permissions":    meta.Permissions,
		"replaces":       old.ID,
		"old_expires_at": until,
	})
}

// Always UTC; internal/api's audit values re-render this layout in the reader's language, so change
// both together.
const AuditTimeLayout = "2006-01-02 15:04 UTC" //i18n:ignore a time layout

func rotationMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 31, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS replaced_by TEXT NOT NULL DEFAULT ''`},
	}
}

func rotateGrace(hours *int) (time.Duration, error) {
	if hours == nil {
		return DefaultRotateGrace, nil
	}
	g := time.Duration(*hours) * time.Hour
	if *hours < 0 || g > MaxRotateGrace {
		return 0, apperr.Invalid("auth.errors.rotate_grace_range", "max", int(MaxRotateGrace/time.Hour))
	}
	return g, nil
}

func (h *Handler) rotatableKey(ctx context.Context, id, ws string, now time.Time) (*APIKey, int, error) {
	old, err := h.store.GetWorkspaceAPIKey(ctx, id, ws)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.rotate_api_key_failed", "err", err))
		return nil, http.StatusInternalServerError, apperr.Internal("auth.errors.internal")
	}
	if old == nil || old.ScopeTaskID != "" || old.ScopeChatID != "" || old.ScopeAgent != "" {
		// A run's key is minted and revoked by its run; rotating it would hand out a credential
		// nobody asked for.
		return nil, http.StatusNotFound, apperr.NotFound("auth.errors.api_key_not_found")
	}
	if old.RevokedAt != nil || (old.ExpiresAt != nil && !old.ExpiresAt.After(now)) {
		return nil, http.StatusConflict, apperr.Conflict("auth.errors.rotate_dead_key")
	}
	return old, 0, nil
}

func rotationGrant(old *APIKey, workspaceID string, callerRole Role, callerPerms []Permission) (KeyGrant, error) {
	perms := make([]string, 0, len(old.Permissions))
	for _, p := range old.Permissions {
		perms = append(perms, string(p))
	}
	grant, err := resolveGrant(createAPIKeyRequest{Role: string(old.Role), Permissions: perms},
		workspaceID, callerRole, callerPerms)
	if err != nil {
		return KeyGrant{}, err
	}
	if old.ExpiresAt != nil {
		grant.ExpiresIn = old.ExpiresAt.Sub(old.CreatedAt)
		if maxTTL := APIKeyMaxTTL(); maxTTL > 0 && grant.ExpiresIn > maxTTL {
			grant.ExpiresIn = maxTTL
		}
	}
	return grant, nil
}

type runKeyView struct {
	APIKey
	TaskTitle  string `json:"task_title,omitempty"`
	StageLabel string `json:"stage_label,omitempty"`
}

func (h *Handler) handleListRunKeys(w http.ResponseWriter, r *http.Request) {
	if !h.isAdminOrAPIKey(r) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.admin_or_key_required"))
		return
	}
	if reason := keyManagementDenial(r); reason != nil {
		apperr.WriteJSON(w, r, http.StatusForbidden, reason)
		return
	}
	_, ws, _, ok := h.keyCaller(w, r)
	if !ok {
		return
	}
	keys, err := h.store.ListRunKeys(r.Context(), ws)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.list_api_keys_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	var titles map[string]string
	if h.taskTitles != nil {
		var ids []string
		for i := range keys {
			if keys[i].ScopeTaskID != "" {
				ids = append(ids, keys[i].ScopeTaskID)
			}
		}
		if len(ids) > 0 {
			titles = h.taskTitles(r.Context(), ids)
		}
	}
	out := make([]runKeyView, 0, len(keys))
	for i := range keys {
		v := runKeyView{APIKey: keys[i], TaskTitle: titles[keys[i].ScopeTaskID]}
		if h.stageLabels != nil && keys[i].ScopeStage != "" {
			v.StageLabel = h.stageLabels(r.Context(), keys[i].ScopeTaskID, keys[i].ScopeStage)
		}
		out = append(out, v)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
