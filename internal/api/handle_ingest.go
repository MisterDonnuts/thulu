package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
)

// Every route resolves the environment through requireEnv, project access first; never accept a
// bare environment id here.

// Read the handler per request: routes register before SetIngest runs.
// No ingest store refuses exactly like a bad token, so strangers cannot tell it is unconfigured.
func (s *Server) ingestRoute(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := s.ingest.handler
		if h == nil {
			ingest.Deny(w)
			return
		}
		h.ServeKind(kind, w, r)
	}
}

func (s *Server) requireIngest(w http.ResponseWriter, r *http.Request) bool {
	if s.ingest.store == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.ingest.errors.unavailable")
		return false
	}
	return true
}

func (s *Server) handleListIngestTokens(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireIngest(w, r) {
		return
	}
	rows, err := s.ingest.store.ListTokens(r.Context(), env.ID)
	respondRows(w, r, rows, err)
}

func (s *Server) handleCreateIngestToken(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireIngest(w, r) {
		return
	}
	var body struct {
		Name string `json:"name"`
		// Optional on purpose: autoscaled collectors have no known, stable egress address.
		AllowCIDRs []string `json:"allow_cidrs"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = env.Name + " collector"
	}
	raw, tok, err := s.ingest.store.CreateToken(r.Context(), env.ID, body.Name, authorLabel(r), body.AllowCIDRs)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": tok,
		"raw":   raw,
		"note":  i18n.TCtx(r.Context(), "api.ingest.hints.secret_shown_once"),
	})
}

// Name and secret stay immutable: renaming rewrites what audit rows say, and an in-place secret
// change would skip the rotation overlap window.
func (s *Server) handleUpdateIngestToken(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireIngest(w, r) {
		return
	}
	var body struct {
		AllowCIDRs []string `json:"allow_cidrs"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	err := s.ingest.store.SetAllowCIDRs(r.Context(), env.ID, r.PathValue("tokenId"), body.AllowCIDRs)
	switch {
	case errors.Is(err, ingest.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.ingest.errors.token_not_found")
	case err != nil:
		writeError(w, r, http.StatusBadRequest, err)
	default:
		writeStatus(w, "updated")
	}
}

func (s *Server) handleRevokeIngestToken(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireIngest(w, r) {
		return
	}
	err := s.ingest.store.RevokeToken(r.Context(), env.ID, r.PathValue("tokenId"))
	switch {
	case errors.Is(err, ingest.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.ingest.errors.token_not_found_or_revoked")
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		writeStatus(w, "revoked")
	}
}

func (s *Server) handleListIngestSources(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireIngest(w, r) {
		return
	}
	rows, err := s.ingest.store.ListSources(r.Context(), env.ID)
	respondRows(w, r, rows, err)
}
