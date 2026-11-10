package api

import (
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	wid, unrestricted := s.callerWorkspace(r)
	if wid == "" && !unrestricted {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
		return
	}
	questions, err := s.taskStore.ListPendingQuestions(r.Context(), wid, unrestricted)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	approvals, err := s.taskStore.ListPendingApprovals(r.Context(), wid, unrestricted)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	stalled, err := s.taskStore.ListStalledTasks(r.Context(), wid, unrestricted)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	localizePendingApprovals(r.Context(), approvals)
	writeJSON(w, http.StatusOK, map[string]any{"questions": questions, "approvals": approvals, "stalled": stalled})
}
