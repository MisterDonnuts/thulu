package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

func accountMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 28, SQL: `ALTER TABLE users ADD COLUMN IF NOT EXISTS last_workspace_id TEXT NOT NULL DEFAULT ''`},
	}
}

func (h *Handler) WithWorkspacePurge(fn func(ctx context.Context, workspaceID string) error) *Handler {
	h.purgeWorkspace = fn
	return h
}

type Membership struct {
	Workspace
	Role        Role `json:"role"`
	MemberCount int  `json:"member_count"`
	Current     bool `json:"current"`
}

func (s *UserStore) ListMemberships(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT w.id, w.name, w.slug, w.owner_id, w.created_at, m.role,
		       (SELECT COUNT(*) FROM workspace_members c WHERE c.workspace_id = w.id)
		FROM workspaces w
		JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = ?
		ORDER BY w.created_at ASC`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.Name, &m.Slug, &m.OwnerID, &m.CreatedAt, &m.Role, &m.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *UserStore) IdentityProviders(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT provider FROM oidc_identities WHERE user_id = ?
		  GROUP BY provider ORDER BY MIN(created_at)`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *UserStore) LastWorkspace(ctx context.Context, userID string) (wsID string, role Role, err error) {
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT m.workspace_id, m.role
		   FROM users u JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = u.last_workspace_id
		  WHERE u.id = ?`), userID,
	).Scan(&wsID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return wsID, role, err
}

func (s *UserStore) SetLastWorkspace(ctx context.Context, userID, workspaceID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE users SET last_workspace_id = ? WHERE id = ?`), workspaceID, userID)
	return err
}

var ErrNotWorkspaceOwner error = apperr.Forbidden("auth.account.errors.transfer_owner_only")

var ErrTransferTargetNotMember error = apperr.Invalid("auth.account.errors.transfer_target_not_member")

// The target's role is updated first so a non-member is refused before owner_id (FK to users) is
// touched.
// The owner check in the workspace UPDATE locks the row, so a racing second transfer rolls back.
// The old owner's owner-role keys drop to admin: a key's role is fixed at mint and could still
// erase the workspace.
func (s *UserStore) TransferOwnership(ctx context.Context, workspaceID, fromUserID, toUserID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	steps := []struct {
		query  string
		args   []any
		ifNone error
	}{
		{`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`,
			[]any{RoleOwner, workspaceID, toUserID}, ErrTransferTargetNotMember},
		{`UPDATE workspaces SET owner_id = ? WHERE id = ? AND owner_id = ?`,
			[]any{toUserID, workspaceID, fromUserID}, ErrNotWorkspaceOwner},
		{`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`,
			[]any{RoleAdmin, workspaceID, fromUserID}, ErrNotWorkspaceOwner},
	}
	for _, st := range steps {
		res, err := tx.ExecContext(ctx, s.rb(st.query), st.args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return st.ifNone
		}
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET role = ?
		  WHERE workspace_id = ? AND role = ? AND revoked_at IS NULL
		    AND (created_by = ? OR scope_delegated_by = ?)`),
		RoleAdmin, workspaceID, RoleOwner, fromUserID, fromUserID); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrAccountOwnsWorkspaces error = apperr.Conflict("auth.account.errors.still_owns_workspaces")

// Keys the person minted or delegated are revoked, never reassigned: moving them would hand someone
// a credential they never chose.
// Revoked rather than deleted so admins still see which integration stopped and when.
// Owned workspaces must already be purged or transferred.
func (s *UserStore) DeleteUser(ctx context.Context, userID string) (revokedKeys int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var owned int
	err = tx.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM workspaces WHERE owner_id = ?`), userID).Scan(&owned)
	if err != nil {
		return 0, err
	}
	if owned > 0 {
		return 0, ErrAccountOwnsWorkspaces
	}
	res, err := tx.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET revoked_at = ?
		  WHERE (created_by = ? OR scope_delegated_by = ?) AND revoked_at IS NULL`),
		time.Now(), userID, userID)
	if err != nil {
		return 0, err
	}
	if revokedKeys, err = res.RowsAffected(); err != nil {
		return 0, err
	}
	for _, q := range []string{
		`DELETE FROM refresh_tokens WHERE user_id = ?`,
		`DELETE FROM oidc_identities WHERE user_id = ?`,
		`DELETE FROM workspace_members WHERE user_id = ?`,
		`DELETE FROM user_preferences WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err = tx.ExecContext(ctx, s.rb(q), userID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// The cached token version must not outlive the user row, or outstanding access tokens keep
	// working.
	s.tokenVersions.invalidate(userID)
	return revokedKeys, nil
}

// API keys (even owner keys) and the no-login dev session are refused: these routes are a person's
// decision, not a workspace's.
func personSession(w http.ResponseWriter, r *http.Request) (*Claims, bool) {
	ctx := r.Context()
	c := ClaimsFromContext(ctx)
	if IsAPIKeyAuthed(ctx) || (c == nil && DevSession(ctx)) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.account.errors.session_only"))
		return nil, false
	}
	if c == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return nil, false
	}
	return c, true
}

type switchWorkspaceRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	RefreshToken string `json:"refresh_token"`
}

// Rotates within the same refresh family so the absolute lifetime cap holds and replay detection
// still works.
// A new family would let a session renew forever by switching back and forth.
func (h *Handler) handleSwitchWorkspace(w http.ResponseWriter, r *http.Request) {
	claims, ok := personSession(w, r)
	if !ok {
		return
	}
	var req switchWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	target := strings.TrimSpace(req.WorkspaceID)
	if target == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.account.errors.workspace_id_required"))
		return
	}
	role, err := h.store.GetMemberRole(r.Context(), target, claims.UserID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if role == "" {
		// Same answer whether the workspace exists or not: membership is the only thing this route
		// reveals.
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.account.errors.not_a_member"))
		return
	}

	refreshTok, fromBody := req.RefreshToken, req.RefreshToken != ""
	if !fromBody {
		if ck, ckErr := r.Cookie(CookieRefresh); ckErr == nil {
			refreshTok = ck.Value
		}
	}
	if refreshTok == "" {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_missing"))
		return
	}
	hash := HashRefreshToken(refreshTok)
	rec, err := h.store.LookupRefreshToken(r.Context(), hash)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if h.rejectBadRefresh(w, r, rec) {
		return
	}
	if rec.UserID != claims.UserID {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_invalid"))
		return
	}
	user, err := h.store.GetUserByID(r.Context(), claims.UserID)
	if err != nil || user == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.user_not_found"))
		return
	}

	tokens, err := h.rotateTokenPair(r.Context(), user, target, role, hash, rec.FamilyID, rec.FamilyStart, clientDevice(r))
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	logx.Warn(h.store.SetLastWorkspace(r.Context(), user.ID, target), "auth.account.log.set_last_workspace", "user", user.ID)
	if err := setSessionCookies(w, r, tokens); err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	// Recorded only in the target workspace; the source workspace is another tenant's business and
	// stays out.
	h.auditRequest(r, "auth.workspace_switched", target, user.ID, "user",
		i18n.Text("auth.account.audit.workspace_switched"))

	resp := map[string]any{
		"workspace_id": target,
		"role":         role,
		"expires_at":   tokens.ExpiresAt,
		"token_type":   tokens.TokenType,
	}
	// Tokens go in the body only to a client that sent its refresh token in the body, never to page
	// script.
	if fromBody {
		resp["access_token"] = tokens.AccessToken
		resp["refresh_token"] = tokens.RefreshToken
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type transferOwnershipRequest struct {
	UserID string `json:"user_id"`
}

func (h *Handler) handleTransferOwnership(w http.ResponseWriter, r *http.Request) {
	claims, ok := personSession(w, r)
	if !ok {
		return
	}
	ws, err := h.store.GetWorkspace(r.Context(), r.PathValue("id"))
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if ws == nil {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
		return
	}
	if ws.OwnerID != claims.UserID {
		apperr.WriteJSON(w, r, http.StatusForbidden, ErrNotWorkspaceOwner)
		return
	}
	var req transferOwnershipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	target := strings.TrimSpace(req.UserID)
	switch target {
	case "":
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.account.errors.transfer_user_required"))
		return
	case claims.UserID:
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.account.errors.transfer_to_self"))
		return
	}

	if err := h.store.TransferOwnership(r.Context(), ws.ID, claims.UserID, target); err != nil {
		if errors.Is(err, ErrNotWorkspaceOwner) || errors.Is(err, ErrTransferTargetNotMember) {
			apperr.WriteJSON(w, r, 0, err)
			return
		}
		log.Printf("[auth] %s", i18n.Log("auth.account.log.transfer_failed", "workspace", ws.ID, "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	// Bump ends access tokens stamped with the old roles; refresh tokens stay because a refresh
	// re-reads the role from membership.
	logx.Warn(h.store.BumpTokenVersion(r.Context(), claims.UserID), "auth.account.log.transfer_bump_version", "user", claims.UserID)
	logx.Warn(h.store.BumpTokenVersion(r.Context(), target), "auth.account.log.transfer_bump_version", "user", target)
	// The bump also killed the caller's access token; without a fresh pair the browser's next
	// request clears all cookies and signs out.
	logx.Warn(h.reissueCookieSession(w, r, claims.UserID), "auth.account.log.transfer_reissue", "user", claims.UserID)

	label := target
	if u, uErr := h.store.GetUserByID(r.Context(), target); uErr == nil && u != nil {
		label = u.Email
	}
	h.auditRequest(r, "workspace.ownership_transferred", ws.ID, target, "user",
		i18n.Text("auth.account.audit.ownership_transferred", "workspace", ws.Slug, "user", label))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "owner_id": target})
}

func (h *Handler) reissueCookieSession(w http.ResponseWriter, r *http.Request, userID string) error {
	ck, err := r.Cookie(CookieRefresh)
	if err != nil || ck.Value == "" {
		return nil
	}
	ctx := r.Context()
	hash := HashRefreshToken(ck.Value)
	rec, err := h.store.LookupRefreshToken(ctx, hash)
	if err != nil {
		return err
	}
	if rec.State != RefreshValid || rec.UserID != userID ||
		(!rec.FamilyStart.IsZero() && time.Since(rec.FamilyStart) > SessionMaxLifetime()) {
		return nil
	}
	role, err := h.store.GetMemberRole(ctx, rec.WorkspaceID, userID)
	if err != nil || role == "" {
		return err
	}
	user, err := h.store.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return err
	}
	tokens, err := h.rotateTokenPair(ctx, user, rec.WorkspaceID, role, hash, rec.FamilyID, rec.FamilyStart, clientDevice(r))
	if err != nil {
		return err
	}
	return setSessionCookies(w, r, tokens)
}

type deleteAccountRequest struct {
	Confirm string `json:"confirm"`
}

// An account without an email can never be confirmed: an empty confirm must not match an empty
// email.
func deleteConfirmed(confirm, email string) bool {
	return email != "" && strings.EqualFold(strings.TrimSpace(confirm), email)
}

type blockingWorkspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	MemberCount int    `json:"member_count"`
}

type accountDeleteConflict struct {
	apperr.Body
	Workspaces []blockingWorkspace `json:"workspaces"`
}

func (h *Handler) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := personSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	user, err := h.store.GetUserByID(ctx, claims.UserID)
	if err != nil || user == nil {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.user_not_found"))
		return
	}
	var req deleteAccountRequest
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	if !deleteConfirmed(req.Confirm, user.Email) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.account.errors.delete_confirm_required"))
		return
	}

	mss, err := h.store.ListMemberships(ctx, user.ID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	var owned []Membership
	var blocking []blockingWorkspace
	for _, m := range mss {
		if m.OwnerID != user.ID {
			continue
		}
		owned = append(owned, m)
		if m.MemberCount > 1 {
			blocking = append(blocking, blockingWorkspace{ID: m.ID, Name: m.Name, Slug: m.Slug, MemberCount: m.MemberCount})
		}
	}
	if len(blocking) > 0 {
		h.writeAccountDeleteConflict(w, r, blocking)
		return
	}
	if len(owned) > 0 && h.purgeWorkspace == nil {
		apperr.WriteJSON(w, r, http.StatusServiceUnavailable, apperr.Unavailable("auth.account.errors.purge_unavailable"))
		return
	}

	purged := make([]string, 0, len(owned))
	for _, m := range owned {
		if err = h.purgeWorkspace(ctx, m.ID); err != nil {
			log.Printf("[auth] %s", i18n.Log("auth.account.log.purge_failed", "workspace", m.ID, "err", err))
			apperr.WriteJSON(w, r, http.StatusInternalServerError,
				apperr.Wrap(err, "auth.account.errors.purge_failed", "workspace", m.Slug))
			return
		}
		purged = append(purged, m.ID)
		h.auditRequest(r, "workspace.deleted", m.ID, m.ID, "workspace",
			i18n.Text("auth.account.audit.workspace_deleted", "workspace", m.Slug))
	}

	revoked, err := h.store.DeleteUser(ctx, user.ID)
	if err != nil {
		if errors.Is(err, ErrAccountOwnsWorkspaces) {
			apperr.WriteJSON(w, r, 0, err)
			return
		}
		log.Printf("[auth] %s", i18n.Log("auth.account.log.delete_failed", "user", user.ID, "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	// Install-level event: the session's workspace may have just been purged.
	actor := ActorFromRequest(r)
	actor.WorkspaceID = ""
	h.emitAudit(r, AuditEvent{
		Kind:         "user.deleted",
		Actor:        actor,
		ResourceID:   user.ID,
		ResourceType: "user",
		Summary: i18n.Text("auth.account.audit.account_deleted",
			"count", len(purged), "keys", revoked),
	})
	ClearAuthCookies(w, requestIsSecure(r))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":             "deleted",
		"workspaces_deleted": purged,
		"api_keys_revoked":   revoked,
	})
}

func (h *Handler) writeAccountDeleteConflict(w http.ResponseWriter, r *http.Request, blocking []blockingWorkspace) {
	slugs := make([]string, len(blocking))
	for i, b := range blocking {
		slugs[i] = b.Slug
	}
	err := apperr.Conflict("auth.account.errors.owns_shared_workspaces",
		"count", len(blocking), "workspaces", strings.Join(slugs, ", "))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(accountDeleteConflict{Body: apperr.BodyFor(r, err), Workspaces: blocking})
}
