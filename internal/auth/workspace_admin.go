package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const maxWorkspaceNameLen = 80

// The slug must not change: OCTOPUS_SSO_DEFAULT_WORKSPACE and links reference it.
func (s *UserStore) RenameWorkspace(ctx context.Context, id, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE workspaces SET name = ? WHERE id = ?`), name, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

type renameWorkspaceRequest struct {
	Name string `json:"name"`
}

func (h *Handler) handleRenameWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}
	var req renameWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.workspace_name_required"))
		return
	}
	if utf8.RuneCountInString(name) > maxWorkspaceNameLen {
		apperr.WriteJSON(w, r, http.StatusBadRequest,
			apperr.Invalid("auth.errors.workspace_name_too_long", "max", maxWorkspaceNameLen))
		return
	}
	found, err := h.store.RenameWorkspace(r.Context(), wsID, name)
	if err != nil {
		logx.Warn(err, "auth.log.rename_workspace_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if !found {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
		return
	}
	h.auditRequest(r, "workspace.renamed", wsID, wsID, "workspace",
		i18n.Text("auth.audit.workspace_renamed", "name", name))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": wsID, "name": name})
}

type Seats struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

func (s Seats) Full() bool { return s.Limit > 0 && s.Used >= s.Limit }

func (h *Handler) handleWorkspaceSeats(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleViewer, PermMembersRead) {
		return
	}
	s, err := h.seats(r.Context(), wsID, "")
	if err != nil {
		logx.Warn(err, "auth.log.seats_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func (s *UserStore) RevokeMemberAPIKeys(ctx context.Context, workspaceID, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET revoked_at = ?
		  WHERE workspace_id = ? AND (created_by = ? OR scope_delegated_by = ?) AND revoked_at IS NULL`),
		time.Now(), workspaceID, userID, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (h *Handler) revokeRemovedMemberKeys(r *http.Request, wsID, userID string) {
	n, err := h.store.RevokeMemberAPIKeys(r.Context(), wsID, userID)
	if err != nil {
		logx.Warn(err, "auth.log.removal_revoke_keys", "workspace", wsID)
		return
	}
	if n > 0 {
		h.auditRequest(r, "api_key.revoked", wsID, userID, "user",
			i18n.Text("auth.audit.member_keys_revoked", "user", userID, "count", n))
	}
}

type apiKeyView struct {
	APIKey
	CreatedByEmail string `json:"created_by_email,omitempty"`
}

func (h *Handler) withCreatorEmails(ctx context.Context, keys []APIKey) []apiKeyView {
	emails := map[string]string{}
	out := make([]apiKeyView, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		email, seen := emails[k.CreatedBy]
		if !seen {
			if u, err := h.store.GetUserByID(ctx, k.CreatedBy); err == nil && u != nil {
				email = u.Email
			}
			emails[k.CreatedBy] = email
		}
		out = append(out, apiKeyView{APIKey: *k, CreatedByEmail: email})
	}
	return out
}
