package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	SignupOpen      = "open"
	SignupAllowlist = "allowlist"
)

func SaaSMode() bool {
	return envOn("OCTOPUS_SAAS_MODE")
}

// An unknown value counts as allowlist: a typo must never open sign-up to the internet.
func SignupMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SIGNUP_MODE"))) {
	case SignupOpen:
		return SignupOpen
	case SignupAllowlist:
		return SignupAllowlist
	case "":
		if SaaSMode() {
			return SignupAllowlist
		}
		return SignupOpen
	}
	return SignupAllowlist
}

func validSignupModeEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SIGNUP_MODE"))) {
	case "", SignupOpen, SignupAllowlist:
		return true
	}
	return false
}

func seedAllowedEmails() []string {
	seeds := csvEnv("OCTOPUS_SSO_ALLOWED_EMAILS")
	for i, e := range seeds {
		seeds[i] = strings.ToLower(e)
	}
	return seeds
}

func envOn(name string) bool { return valueOn(os.Getenv(name)) }

func signupMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 27, SQL: `CREATE TABLE IF NOT EXISTS signup_allowlist (
				email      TEXT PRIMARY KEY,
				note       TEXT NOT NULL DEFAULT '',
				added_by   TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL
			)`},
	}
}

type SignupAllowlistEntry struct {
	Email     string    `json:"email"`
	Note      string    `json:"note"`
	AddedBy   string    `json:"added_by"`
	CreatedAt time.Time `json:"created_at"`
	Source    string    `json:"source"`
}

const (
	allowlistSourceList = "list"
	allowlistSourceEnv  = "env"
)

func (s *UserStore) ListSignupAllowlist(ctx context.Context) ([]SignupAllowlistEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT email, note, added_by, created_at FROM signup_allowlist ORDER BY created_at, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignupAllowlistEntry
	for rows.Next() {
		e := SignupAllowlistEntry{Source: allowlistSourceList}
		if err := rows.Scan(&e.Email, &e.Note, &e.AddedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *UserStore) AddSignupAllowlist(ctx context.Context, email, note, addedBy string) (*SignupAllowlistEntry, error) {
	e := &SignupAllowlistEntry{
		Email: strings.ToLower(strings.TrimSpace(email)), Note: strings.TrimSpace(note),
		AddedBy: addedBy, CreatedAt: time.Now().UTC(), Source: allowlistSourceList,
	}
	err := s.db.QueryRowContext(ctx, s.rb(
		`INSERT INTO signup_allowlist (email, note, added_by, created_at) VALUES (?,?,?,?)
		 ON CONFLICT (email) DO UPDATE SET note = excluded.note
		 RETURNING added_by, created_at`),
		e.Email, e.Note, e.AddedBy, e.CreatedAt,
	).Scan(&e.AddedBy, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *UserStore) RemoveSignupAllowlist(ctx context.Context, email string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM signup_allowlist WHERE email = ?`),
		strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *UserStore) SignupAllowlisted(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	for _, seed := range seedAllowedEmails() {
		if seed == email {
			return true, nil
		}
	}
	var one int
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT 1 FROM signup_allowlist WHERE email = ?`), email).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func operatorWorkspace() string {
	return strings.TrimSpace(os.Getenv("OCTOPUS_OPERATOR_WORKSPACE"))
}

func (s *UserStore) isOperatorMember(ctx context.Context, userID string) bool {
	op := operatorWorkspace()
	if op == "" || userID == "" {
		return false
	}
	role, err := s.GetMemberRole(ctx, op, userID)
	return err == nil && role != ""
}

// Only an already-linked account may prove operator membership; an email alone is what the
// allowlist exists to check.
func (h *Handler) signupAdmit(ctx context.Context, id ssoIdentity, existing *User) error {
	if SignupMode() != SignupAllowlist {
		return nil
	}
	if existing != nil && h.store.isOperatorMember(ctx, existing.ID) {
		return nil
	}
	if !id.EmailVerified {
		return apperr.Wrap(ErrSSONotAllowed, "auth.errors.sso_email_not_vouched")
	}
	ok, err := h.store.SignupAllowlisted(ctx, id.Email)
	if err != nil {
		return apperr.Wrap(err, "auth.errors.signup_allowlist_check")
	}
	if !ok {
		return apperr.Wrap(ErrSSONotAllowed, "auth.errors.signup_not_invited")
	}
	return nil
}

// Must stay in sync with callerIsOperator in internal/api. A run's key never passes.
func (h *Handler) requireOperator(w http.ResponseWriter, r *http.Request, perm Permission) bool {
	ctx := r.Context()
	op := operatorWorkspace()
	switch {
	case IsAPIKeyAuthed(ctx):
		if op != "" && !APIKeyRunScope(ctx).IsRun() && APIKeyWorkspace(ctx) == op &&
			roleAtLeast(APIKeyRole(ctx), RoleAdmin) && APIKeyGrants(ctx, perm) {
			return true
		}
	case ClaimsFromContext(ctx) != nil:
		if sessionIsOperator(ClaimsFromContext(ctx)) {
			return true
		}
	case DevSession(ctx):
		return true
	default:
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return false
	}
	apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.operator_only", "env", "OCTOPUS_OPERATOR_WORKSPACE"))
	return false
}

type signupAllowlistResponse struct {
	Mode    string                 `json:"mode"`
	Entries []SignupAllowlistEntry `json:"entries"`
	Waiting []InvitationWaiting    `json:"waiting"`
}

func (h *Handler) handleListSignupAllowlist(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperator(w, r, PermMembersRead) {
		return
	}
	rows, err := h.store.ListSignupAllowlist(r.Context())
	if err != nil {
		logx.Warn(err, "auth.log.signup_allowlist_list_failed")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	listed := make(map[string]bool, len(rows))
	for _, e := range rows {
		listed[e.Email] = true
	}
	entries := rows
	var seeds []SignupAllowlistEntry
	for _, email := range seedAllowedEmails() {
		if !listed[email] {
			listed[email] = true
			seeds = append(seeds, SignupAllowlistEntry{Email: email, Source: allowlistSourceEnv})
		}
	}
	sort.Slice(seeds, func(i, j int) bool { return seeds[i].Email < seeds[j].Email })
	entries = append(seeds, entries...)
	if entries == nil {
		entries = []SignupAllowlistEntry{}
	}
	waiting := []InvitationWaiting{}
	if SignupMode() == SignupAllowlist {
		invited, werr := h.store.waitingInvitations(r.Context())
		logx.Warn(werr, "auth.log.invitation_list_failed", "workspace", "*")
		for _, inv := range invited {
			if !listed[inv.Email] {
				listed[inv.Email] = true
				waiting = append(waiting, inv)
			}
		}
		asked, rerr := h.store.signupRequests(r.Context())
		logx.Warn(rerr, "auth.log.signup_request_failed")
		for _, req := range asked {
			if !listed[req.Email] {
				waiting = append(waiting, req)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(signupAllowlistResponse{Mode: SignupMode(), Entries: entries, Waiting: waiting})
}

type addSignupAllowlistRequest struct {
	Email string `json:"email"`
	Note  string `json:"note"`
}

const maxAllowlistNote = 200

func plausibleEmail(e string) bool {
	if len(e) > 254 || strings.ContainsAny(e, " \t\r\n,;") {
		return false
	}
	local, domain, ok := strings.Cut(e, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@")
}

func (h *Handler) handleAddSignupAllowlist(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperator(w, r, PermMembersWrite) {
		return
	}
	var req addSignupAllowlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !plausibleEmail(email) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.signup_email_invalid"))
		return
	}
	if len([]rune(strings.TrimSpace(req.Note))) > maxAllowlistNote {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.signup_note_too_long", "max", maxAllowlistNote))
		return
	}
	e, err := h.store.AddSignupAllowlist(r.Context(), email, req.Note, ActorFromRequest(r).Label)
	if err != nil {
		logx.Warn(err, "auth.log.signup_allowlist_add_failed")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	_, reqErr := h.store.DeleteSignupRequest(r.Context(), email)
	logx.Warn(reqErr, "auth.log.signup_request_failed")
	h.auditRequest(r, "auth.signup_allowlist_added", "", email, "signup_allowlist",
		i18n.Text("auth.audit.signup_allowlist_added", "email", email))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(e)
}

// Refresh does not re-check the allowlist, so removal must revoke sessions or the person stays in
// until the family expires.
func (h *Handler) handleRemoveSignupAllowlist(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperator(w, r, PermMembersWrite) {
		return
	}
	ctx := r.Context()
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	removed, err := h.store.RemoveSignupAllowlist(ctx, email)
	if err != nil {
		logx.Warn(err, "auth.log.signup_allowlist_remove_failed")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if !removed {
		for _, seed := range seedAllowedEmails() {
			if seed == email {
				apperr.WriteJSON(w, r, http.StatusConflict,
					apperr.Conflict("auth.errors.signup_allowlist_from_env", "env", "OCTOPUS_SSO_ALLOWED_EMAILS"))
				return
			}
		}
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.signup_allowlist_not_found"))
		return
	}
	if SignupMode() == SignupAllowlist {
		h.evictUninvited(ctx, email)
	}
	h.auditRequest(r, "auth.signup_allowlist_removed", "", email, "signup_allowlist",
		i18n.Text("auth.audit.signup_allowlist_removed", "email", email))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) evictUninvited(ctx context.Context, email string) {
	user, err := h.store.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		logx.Warn(err, "auth.log.signup_evict_lookup", "email", email)
		return
	}
	if h.store.isOperatorMember(ctx, user.ID) {
		return
	}
	if still, checkErr := h.store.SignupAllowlisted(ctx, email); checkErr != nil || still {
		logx.Warn(checkErr, "auth.log.signup_evict_lookup", "email", email)
		return
	}
	logx.Warn(h.store.RevokeAllRefreshTokensForUser(ctx, user.ID), "auth.log.signup_evict_revoke", "user", user.ID)
	logx.Warn(h.store.BumpTokenVersion(ctx, user.ID), "auth.log.signup_evict_bump", "user", user.ID)
}
