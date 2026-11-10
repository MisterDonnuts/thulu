package api

import (
	"net/http"
	"strings"
)

func (s *Server) handleEvidenceJobUnreachable(w http.ResponseWriter, r *http.Request) {
	ws, batch, ok := s.runnerBatch(w, r)
	if !ok {
		return
	}
	if s.evidenceSvc == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.evidenceSvc.FailUnreachable(r.Context(), ws, batch, strings.TrimSpace(body.Error)); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "failed"})
}
