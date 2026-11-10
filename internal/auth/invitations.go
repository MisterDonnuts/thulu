package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// An invitation never bypasses the signup allowlist; it only decides where the person lands.

func invitationMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 29, SQL: `CREATE TABLE IF NOT EXISTS workspace_invitations (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
				email        TEXT NOT NULL,
				role         TEXT NOT NULL,
				invited_by   TEXT NOT NULL DEFAULT '',
				created_at   TIMESTAMPTZ NOT NULL,
				UNIQUE (workspace_id, email)
			);
			CREATE INDEX IF NOT EXISTS idx_workspace_invitations_email ON workspace_invitations(email)`},
	}
}

type Invitation struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	Email          string    `json:"email"`
	Role           Role      `json:"role"`
	InvitedBy      string    `json:"invited_by"`
	CreatedAt      time.Time `json:"created_at"`
	AwaitingAccess bool      `json:"awaiting_access"`
}

var ErrInvitationNotFound error = apperr.NotFound("auth.errors.invitation_not_found")

func (s *UserStore) UpsertInvitation(ctx context.Context, workspaceID, email string, role Role, invitedBy string) (*Invitation, error) {
	inv := &Invitation{
		ID:          genID(),
		WorkspaceID: workspaceID,
		Email:       strings.ToLower(strings.TrimSpace(email)),
		Role:        role,
		InvitedBy:   invitedBy,
		CreatedAt:   time.Now(),
	}
	err := s.db.QueryRowContext(ctx, s.rb(
		`INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, created_at)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT (workspace_id, email) DO UPDATE SET role = excluded.role
		 RETURNING id, invited_by, created_at`),
		inv.ID, inv.WorkspaceID, inv.Email, inv.Role, inv.InvitedBy, inv.CreatedAt,
	).Scan(&inv.ID, &inv.InvitedBy, &inv.CreatedAt)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *UserStore) ListInvitations(ctx context.Context, workspaceID string) ([]Invitation, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, workspace_id, email, role, invited_by, created_at
		 FROM workspace_invitations WHERE workspace_id = ? ORDER BY created_at, email`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(&inv.ID, &inv.WorkspaceID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *UserStore) SetInvitationRole(ctx context.Context, workspaceID, id string, role Role) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE workspace_invitations SET role = ? WHERE workspace_id = ? AND id = ?`), role, workspaceID, id)
	return invitationRowTouched(res, err)
}

func (s *UserStore) DeleteInvitation(ctx context.Context, workspaceID, id string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_invitations WHERE workspace_id = ? AND id = ?`), workspaceID, id)
	return invitationRowTouched(res, err)
}

func (s *UserStore) deleteInvitationFor(ctx context.Context, workspaceID, email string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_invitations WHERE workspace_id = ? AND email = ?`),
		workspaceID, strings.ToLower(strings.TrimSpace(email)))
	return err
}

func invitationRowTouched(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvitationNotFound
	}
	return nil
}

func (s *UserStore) countInvitations(ctx context.Context, workspaceID, exceptEmail string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM workspace_invitations WHERE workspace_id = ? AND email <> ?`),
		workspaceID, strings.ToLower(strings.TrimSpace(exceptEmail))).Scan(&n)
	return n, err
}

type AcceptedInvitation struct {
	WorkspaceID string
	Role        Role
}

// email must be provider-verified and belong to userID, or anyone could claim another person's
// invitations.
func (s *UserStore) AcceptInvitations(ctx context.Context, userID, email string) ([]AcceptedInvitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	found, err := s.takeInvitations(ctx, tx, email)
	if err != nil {
		return nil, err
	}

	var out []AcceptedInvitation
	for _, p := range found {
		role := p.Role
		if role == RoleOwner {
			claimed, claimErr := s.claimPlaceholderOwnership(ctx, tx, p.WorkspaceID, userID)
			if claimErr != nil {
				return nil, claimErr
			}
			if claimed {
				out = append(out, AcceptedInvitation{WorkspaceID: p.WorkspaceID, Role: RoleOwner})
				continue
			}
			role = RoleAdmin
		}
		if !isKnownRole(role) {
			role = RoleViewer
		}
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO workspace_members (workspace_id, user_id, role, invited_at) VALUES (?,?,?,?)
			 ON CONFLICT (workspace_id, user_id) DO NOTHING`),
			p.WorkspaceID, userID, role, time.Now()); err != nil {
			return nil, err
		}
		out = append(out, AcceptedInvitation{WorkspaceID: p.WorkspaceID, Role: role})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

type pendingInvitation struct {
	AcceptedInvitation
	at time.Time
}

func (s *UserStore) takeInvitations(ctx context.Context, tx *sql.Tx, email string) ([]pendingInvitation, error) {
	rows, err := tx.QueryContext(ctx, s.rb(
		`DELETE FROM workspace_invitations WHERE email = ?
		 RETURNING workspace_id, role, created_at`), email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []pendingInvitation
	for rows.Next() {
		var p pendingInvitation
		if err := rows.Scan(&p.WorkspaceID, &p.Role, &p.at); err != nil {
			return nil, err
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].at.Before(found[j].at) })
	return found, nil
}

type InvitationWaiting struct {
	Email      string    `json:"email"`
	Workspaces []string  `json:"workspaces"`
	InvitedAt  time.Time `json:"invited_at"`
	Requested  bool      `json:"requested,omitempty"`
	Name       string    `json:"name,omitempty"`
}

func (s *UserStore) waitingInvitations(ctx context.Context) ([]InvitationWaiting, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT i.email, w.name, i.created_at
		 FROM workspace_invitations i
		 JOIN workspaces w ON w.id = i.workspace_id
		 WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.email = i.email)
		 ORDER BY i.created_at, i.email`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationWaiting
	at := map[string]int{}
	for rows.Next() {
		var email, ws string
		var created time.Time
		if err := rows.Scan(&email, &ws, &created); err != nil {
			return nil, err
		}
		if i, ok := at[email]; ok {
			out[i].Workspaces = append(out[i].Workspaces, ws)
			continue
		}
		at[email] = len(out)
		out = append(out, InvitationWaiting{Email: email, Workspaces: []string{ws}, InvitedAt: created})
	}
	return out, rows.Err()
}

func (h *Handler) awaitingAccess(ctx context.Context, email string) bool {
	if SignupMode() != SignupAllowlist {
		return false
	}
	ok, err := h.store.SignupAllowlisted(ctx, email)
	return err == nil && !ok
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request, wsID, email string, role Role) {
	ctx := r.Context()
	if !plausibleEmail(email) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.signup_email_invalid"))
		return
	}
	if h.inviteSeatCapReached(ctx, wsID, email) {
		apperr.WriteJSON(w, r, http.StatusPaymentRequired, apperr.Forbidden("auth.errors.seat_limit_reached"))
		return
	}
	inv, err := h.store.UpsertInvitation(ctx, wsID, email, role, ActorFromRequest(r).Label)
	if err != nil {
		logx.Warn(err, "auth.log.invitation_create_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	inv.AwaitingAccess = h.awaitingAccess(ctx, email)
	h.auditRequest(r, "workspace.invitation_created", wsID, inv.ID, "invitation",
		i18n.Text("auth.audit.invitation_created", "email", email, "role", role))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"status": "invited", "invitation": inv})
}

func (h *Handler) inviteSeatCapReached(ctx context.Context, wsID, email string) bool {
	if h.seatLimit(wsID) == 0 {
		return false
	}
	s, err := h.seats(ctx, wsID, email)
	return err == nil && s.Full()
}

func (h *Handler) seatLimit(wsID string) int {
	if h.seatLimitFn == nil {
		return 0
	}
	return max(h.seatLimitFn(wsID), 0)
}

func (h *Handler) seats(ctx context.Context, wsID, exceptEmail string) (Seats, error) {
	used, err := h.seatsTaken(ctx, wsID, exceptEmail)
	if err != nil {
		return Seats{}, err
	}
	return Seats{Used: used, Limit: h.seatLimit(wsID)}, nil
}

func (h *Handler) seatsTaken(ctx context.Context, wsID, exceptEmail string) (int, error) {
	members, err := h.store.ListMembers(ctx, wsID)
	if err != nil {
		return 0, err
	}
	pending, err := h.store.countInvitations(ctx, wsID, exceptEmail)
	if err != nil {
		return 0, err
	}
	return len(members) + pending, nil
}

func (h *Handler) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleViewer, PermMembersRead) {
		return
	}
	invs, err := h.store.ListInvitations(r.Context(), wsID)
	if err != nil {
		logx.Warn(err, "auth.log.invitation_list_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	for i := range invs {
		invs[i].AwaitingAccess = h.awaitingAccess(r.Context(), invs[i].Email)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(invs)
}

func (h *Handler) handleUpdateInvitation(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}
	var req updateMemberRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	if !isKnownRole(req.Role) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.member_role_invalid", "role", req.Role))
		return
	}
	if req.Role == RoleOwner {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.owner_not_grantable_on_add"))
		return
	}
	err := h.store.SetInvitationRole(r.Context(), wsID, r.PathValue("invitationId"), req.Role)
	if errors.Is(err, ErrInvitationNotFound) {
		apperr.WriteJSON(w, r, http.StatusNotFound, err)
		return
	}
	if err != nil {
		logx.Warn(err, "auth.log.invitation_update_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteInvitation(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}
	id := r.PathValue("invitationId")
	err := h.store.DeleteInvitation(r.Context(), wsID, id)
	if errors.Is(err, ErrInvitationNotFound) {
		apperr.WriteJSON(w, r, http.StatusNotFound, err)
		return
	}
	if err != nil {
		logx.Warn(err, "auth.log.invitation_delete_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	h.auditRequest(r, "workspace.invitation_revoked", wsID, id, "invitation",
		i18n.Text("auth.audit.invitation_revoked", "id", id))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) acceptInvitations(r *http.Request, user *User) (first AcceptedInvitation, ok bool) {
	accepted, err := h.store.AcceptInvitations(r.Context(), user.ID, user.Email)
	if err != nil {
		logx.Warn(err, "auth.log.invitation_accept_failed", "user", user.ID)
		return AcceptedInvitation{}, false
	}
	for _, a := range accepted {
		h.auditUser(r, "workspace.invitation_accepted", user.ID, user.Email, a.WorkspaceID,
			i18n.Text("auth.audit.invitation_accepted", "email", user.Email, "role", a.Role))
	}
	if len(accepted) == 0 {
		return AcceptedInvitation{}, false
	}
	return accepted[0], true
}

type InvitationPreview struct {
	Workspace      string `json:"workspace"`
	Email          string `json:"email"`
	Role           Role   `json:"role"`
	AwaitingAccess bool   `json:"awaiting_access"`
}

func (s *UserStore) GetInvitationPreview(ctx context.Context, id string) (*InvitationPreview, error) {
	var p InvitationPreview
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT w.name, i.email, i.role FROM workspace_invitations i
		 JOIN workspaces w ON w.id = i.workspace_id WHERE i.id = ?`), id,
	).Scan(&p.Workspace, &p.Email, &p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Public: relies on the rate limit in RegisterRoutes and 64-bit random ids. Unknown and used ids
// get the same 404.
func (h *Handler) handleInvitationPreview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		apperr.WriteJSON(w, r, http.StatusNotFound, ErrInvitationNotFound)
		return
	}
	p, err := h.store.GetInvitationPreview(r.Context(), id)
	if err != nil {
		logx.Warn(err, "auth.log.invitation_list_failed", "workspace", "?")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if p == nil {
		apperr.WriteJSON(w, r, http.StatusNotFound, ErrInvitationNotFound)
		return
	}
	p.AwaitingAccess = h.awaitingAccess(r.Context(), p.Email)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (s *UserStore) invitingWorkspaces(ctx context.Context, email string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT w.name FROM workspace_invitations i JOIN workspaces w ON w.id = i.workspace_id
		 WHERE i.email = ? ORDER BY i.created_at`), strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
