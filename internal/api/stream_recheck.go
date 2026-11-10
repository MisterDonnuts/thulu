package api

import (
	"context"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

const defaultStreamRecheck = 30 * time.Second

// A DB error on a session check passes (next tick re-asks), as in the JWT middleware; a failed key
// lookup is indistinguishable from revocation, so it closes.
func (s *Server) streamCredentialCheck(r *http.Request) func(context.Context) bool {
	if s.authStore == nil {
		return func(context.Context) bool { return true }
	}
	if auth.IsAPIKeyAuthed(r.Context()) {
		raw := presentedAPIKey(r)
		return func(ctx context.Context) bool {
			return s.authStore.LookupAPIKeyInfo(ctx, raw) != nil
		}
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return func(context.Context) bool { return true }
	}
	return func(ctx context.Context) bool {
		if auth.TokenVersionStale(ctx, s.authStore, claims) {
			return false
		}
		role, err := s.authStore.GetMemberRole(ctx, claims.WorkspaceID, claims.UserID)
		return err != nil || role != ""
	}
}

func (s *Server) streamRecheckEvery() time.Duration {
	if s.streamRecheck > 0 {
		return s.streamRecheck
	}
	return defaultStreamRecheck
}
