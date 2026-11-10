package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Stable codes that web/src/lib/login-notice.ts maps to text; change both together.
const (
	loginReasonPendingAccess      = "pending_access"
	loginReasonPendingAccessFirst = "pending_access_first"
	loginReasonStateExpired       = "state_expired"
	loginReasonSessionMissing     = "session_missing"
	loginReasonEmailUnverified    = "email_unverified"
	loginReasonNotInvited         = "not_invited"
)

var (
	errSSOStateExpired              = apperr.Unauthenticated("auth.errors.sso_state_expired")
	errSSOSessionMissing            = apperr.Unauthenticated("auth.errors.sso_session_missing")
	errSSOEmailUnverifiedLink error = apperr.Forbidden("auth.errors.sso_email_unverified")
)

// Must match the keys signupAdmit wraps around ErrSSONotAllowed.
const (
	keySignupNotInvited   = "auth.errors.signup_not_invited"
	keySSOEmailNotVouched = "auth.errors.sso_email_not_vouched"
)

func errHasKey(err error, key string) bool {
	for err != nil {
		var e *apperr.Error
		if !errors.As(err, &e) {
			return false
		}
		if e.Key == key {
			return true
		}
		err = e.Err
	}
	return false
}

func roundTripReason(err error) string {
	switch {
	case errors.Is(err, errSSOStateExpired):
		return loginReasonStateExpired
	case errors.Is(err, errSSOSessionMissing):
		return loginReasonSessionMissing
	}
	return ""
}

func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrSSOEmailUnverified), errors.Is(err, errSSOEmailUnverifiedLink),
		errHasKey(err, keySSOEmailNotVouched):
		return loginReasonEmailUnverified
	case errHasKey(err, keySignupNotInvited):
		return loginReasonNotInvited
	}
	return ""
}

func (h *Handler) redirectRefused(w http.ResponseWriter, r *http.Request, cause error, providerName string, id ssoIdentity) {
	q := url.Values{}
	reason := refusalReason(cause)
	if reason != "" {
		q.Set("reason", reason)
	}
	switch reason {
	case loginReasonEmailUnverified:
		q.Set("provider", providerName)
	case loginReasonNotInvited:
		q.Set("email", id.Email)
		if token, err := issueAccessRequestToken(id.Email, id.Name); err == nil {
			q.Set("request", token)
		}
		names, err := h.store.invitingWorkspaces(r.Context(), id.Email)
		logx.Warn(err, "auth.log.invitation_list_failed", "workspace", "?")
		for _, n := range names {
			q.Add("invited", n)
		}
	}
	h.redirectToLoginWith(w, r, cause, q)
}

func (h *Handler) redirectPendingAccess(w http.ResponseWriter, r *http.Request, cause error, id ssoIdentity) {
	q := url.Values{}
	q.Set("reason", loginReasonPendingAccess)
	first, err := h.store.noSSOMemberYet(r.Context())
	logx.Warn(err, "auth.log.pending_first_check_failed")
	if err == nil && first {
		q.Set("reason", loginReasonPendingAccessFirst)
		q.Set("email", id.Email)
		if ws := h.firstOwnerWorkspace(r.Context()); ws != "" {
			q.Set("workspace", ws)
		}
	}
	h.redirectToLoginWith(w, r, cause, q)
}

func (h *Handler) firstOwnerWorkspace(ctx context.Context) string {
	if ws := operatorWorkspace(); ws != "" {
		return ws
	}
	ws, err := h.store.SoleWorkspaceID(ctx)
	logx.Warn(err, "auth.log.first_owner_workspace_failed")
	return ws
}

func (s *UserStore) noSSOMemberYet(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM workspace_members m JOIN oidc_identities o ON o.user_id = m.user_id LIMIT 1`,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}
