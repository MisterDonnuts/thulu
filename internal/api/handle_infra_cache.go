package api

import (
	"context"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

func (s *Server) handleInfraCache(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "7d"
	}
	if _, ok := sandbox.CacheReportRanges[name]; !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.infra.errors.bad_cache_range", "range", name)
		return
	}
	rep, err := s.sandbox.store.CacheReport(r.Context(), name, time.Now())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.nameCacheReport(r.Context(), rep)
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) nameCacheReport(ctx context.Context, rep *sandbox.CacheReport) {
	type proj struct{ name, workspace string }
	projects := map[string]proj{}
	if s.projectStore != nil {
		list, err := s.projectStore.List(ctx)
		logx.Warn(err, "api.infra.log.cache_names_failed")
		for i := range list {
			projects[list[i].ID] = proj{list[i].Name, list[i].WorkspaceID}
		}
	}
	wsNames := map[string]string{}
	wsName := func(id string) string {
		if id == "" || s.authStore == nil {
			return ""
		}
		if n, ok := wsNames[id]; ok {
			return n
		}
		ws, err := s.authStore.GetWorkspace(ctx, id)
		logx.Warn(err, "api.infra.log.cache_names_failed")
		if ws != nil {
			wsNames[id] = ws.Name
		} else {
			wsNames[id] = ""
		}
		return wsNames[id]
	}
	for i := range rep.Projects {
		p := &rep.Projects[i]
		if known, ok := projects[p.ProjectID]; ok {
			p.ProjectName = known.name
			if p.WorkspaceID == "" {
				p.WorkspaceID = known.workspace
			}
		}
		p.WorkspaceName = wsName(p.WorkspaceID)
	}
	for i := range rep.Workspaces {
		rep.Workspaces[i].WorkspaceName = wsName(rep.Workspaces[i].WorkspaceID)
	}
}
