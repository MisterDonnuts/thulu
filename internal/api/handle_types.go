package api

import (
	"net/http"
)

func (s *Server) handleListTypes(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	discovered, err := s.agents(tenant).All()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// A nil slice serializes as null and leaves the "Loading…" placeholder in agents.astro stuck.
	writeJSON(w, http.StatusOK, nonNil(discovered))
}
