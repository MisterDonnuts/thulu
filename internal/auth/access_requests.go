package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Long enough to read the refusal screen, short enough that a leaked URL stops working the same
// afternoon.
const accessRequestTTL = time.Hour

// Distinguishes this token from other envelopes signed with the same secret (the OIDC state).
const accessRequestPurpose = "access_request"

func accessRequestMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 30, SQL: `CREATE TABLE IF NOT EXISTS signup_requests (
				email      TEXT PRIMARY KEY,
				name       TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL
			)`},
	}
}

type accessRequestPayload struct {
	Purpose string `json:"pu"`
	Email   string `json:"e"`
	Name    string `json:"nm,omitempty"`
	Exp     int64  `json:"exp"`
}

func issueAccessRequestToken(email, name string) (string, error) {
	return signEnvelope(accessRequestPayload{
		Purpose: accessRequestPurpose,
		Email:   strings.ToLower(strings.TrimSpace(email)),
		Name:    strings.TrimSpace(name),
		Exp:     time.Now().Add(accessRequestTTL).Unix(),
	})
}

func openAccessRequestToken(token string) (*accessRequestPayload, error) {
	var p accessRequestPayload
	if err := openEnvelope(token, &p); err != nil {
		return nil, err
	}
	if p.Purpose != accessRequestPurpose || p.Email == "" {
		return nil, apperr.Unauthenticated("auth.errors.access_request_invalid")
	}
	if time.Now().Unix() > p.Exp {
		return nil, apperr.Unauthenticated("auth.errors.access_request_expired")
	}
	return &p, nil
}

func (s *UserStore) AddSignupRequest(ctx context.Context, email, name string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO signup_requests (email, name, created_at) VALUES (?,?,?)
		 ON CONFLICT (email) DO NOTHING`),
		strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(name), time.Now().UTC())
	return err
}

func (s *UserStore) DeleteSignupRequest(ctx context.Context, email string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM signup_requests WHERE email = ?`),
		strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *UserStore) signupRequests(ctx context.Context) ([]InvitationWaiting, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT email, name, created_at FROM signup_requests ORDER BY created_at, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationWaiting
	for rows.Next() {
		w := InvitationWaiting{Workspaces: []string{}, Requested: true}
		if err := rows.Scan(&w.Email, &w.Name, &w.InvitedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

type accessRequestBody struct {
	Token string `json:"token"`
}

// Public: the requester has no session, so the token the OIDC callback signed for the verified
// email is the credential.
// Without it anyone could fill the operator's list with arbitrary emails.
func (h *Handler) handleAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req accessRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	p, err := openAccessRequestToken(strings.TrimSpace(req.Token))
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, err)
		return
	}
	if err := h.store.AddSignupRequest(r.Context(), p.Email, p.Name); err != nil {
		logx.Warn(err, "auth.log.signup_request_failed")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	h.emitAudit(r, AuditEvent{
		Kind:         "auth.access_requested",
		Actor:        Actor{Type: ActorUser, ID: "email:" + p.Email, Label: p.Email, Email: p.Email},
		ResourceID:   p.Email,
		ResourceType: "signup_allowlist",
		Summary:      i18n.Text("auth.audit.access_requested", "email", p.Email),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "requested", "email": p.Email})
}

func (h *Handler) handleDismissSignupRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperator(w, r, PermMembersWrite) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	found, err := h.store.DeleteSignupRequest(r.Context(), email)
	if err != nil {
		logx.Warn(err, "auth.log.signup_request_failed")
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if !found {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.signup_request_not_found"))
		return
	}
	h.auditRequest(r, "auth.access_request_dismissed", "", email, "signup_allowlist",
		i18n.Text("auth.audit.access_request_dismissed", "email", email))
	w.WriteHeader(http.StatusNoContent)
}
