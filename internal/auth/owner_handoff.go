package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

var ErrOwnerNotPlaceholder error = apperr.Forbidden("auth.errors.owner_not_placeholder")

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *UserStore) placeholderOwner(ctx context.Context, q rowQuerier, wsID string) (string, bool, error) {
	var ownerID string
	if err := q.QueryRowContext(ctx, s.rb(`SELECT owner_id FROM workspaces WHERE id = ?`), wsID).Scan(&ownerID); err != nil {
		return "", false, err
	}
	var one int
	err := q.QueryRowContext(ctx, s.rb(`SELECT 1 FROM oidc_identities WHERE user_id = ? LIMIT 1`), ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ownerID, true, nil
	}
	return ownerID, false, err
}

func (s *UserStore) claimPlaceholderOwnership(ctx context.Context, tx *sql.Tx, wsID, userID string) (bool, error) {
	var locked string
	if err := tx.QueryRowContext(ctx, s.rb(`SELECT id FROM workspaces WHERE id = ? FOR UPDATE`), wsID).Scan(&locked); err != nil {
		return false, err
	}
	ownerID, placeholder, err := s.placeholderOwner(ctx, tx, wsID)
	if err != nil || !placeholder || ownerID == userID {
		return false, err
	}
	for _, st := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspace_members (workspace_id, user_id, role, invited_at) VALUES (?,?,?,?)
		  ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = excluded.role`,
			[]any{wsID, userID, RoleOwner, time.Now()}},
		{`UPDATE workspaces SET owner_id = ? WHERE id = ?`, []any{userID, wsID}},
		{`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`,
			[]any{RoleAdmin, wsID, ownerID}},
	} {
		if _, err := tx.ExecContext(ctx, s.rb(st.query), st.args...); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Only an owner-role API key may grant owner, and only while the owner is a placeholder. A
// signed-in owner is a real person and must use /transfer.
func (h *Handler) callerMayGrantOwner(r *http.Request, wsID string) error {
	ctx := r.Context()
	if !IsAPIKeyAuthed(ctx) || APIKeyRole(ctx) != RoleOwner {
		return apperr.Forbidden("auth.errors.owner_not_grantable_on_add")
	}
	_, placeholder, err := h.store.placeholderOwner(ctx, h.store.db, wsID)
	if err != nil {
		return err
	}
	if !placeholder {
		return ErrOwnerNotPlaceholder
	}
	return nil
}

func (h *Handler) handOwnerTo(w http.ResponseWriter, r *http.Request, wsID string, user *User) {
	ctx := r.Context()
	tx, err := h.store.db.BeginTx(ctx, nil)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	defer tx.Rollback()
	claimed, err := h.store.claimPlaceholderOwnership(ctx, tx, wsID, user.ID)
	if err == nil && claimed {
		err = tx.Commit()
	}
	if err != nil {
		logx.Warn(err, "auth.log.owner_handoff_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if !claimed {
		apperr.WriteJSON(w, r, http.StatusForbidden, ErrOwnerNotPlaceholder)
		return
	}
	logx.Warn(h.store.BumpTokenVersion(ctx, user.ID), "auth.log.role_change_bump_version")
	h.auditRequest(r, "workspace.ownership_transferred", wsID, user.ID, "user",
		i18n.Text("auth.audit.owner_handed_off", "email", user.Email))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "owner", "user_id": user.ID})
}
