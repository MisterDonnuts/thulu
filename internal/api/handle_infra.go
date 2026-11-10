package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

func (s *Server) SetInfra(c *infra.Collector) { s.infra = c }

// Figures are install-wide (every tenant's containers), so infraGate's operator check is required
// on top of RoleAdmin: tenant owners must get 403.
func (s *Server) registerInfraRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /infra/overview", rr(auth.RoleAdmin, s.infraGate(s.handleInfraOverview)))
	mux.HandleFunc("GET /infra/history", rr(auth.RoleAdmin, s.infraGate(s.handleInfraHistory)))
	mux.HandleFunc("GET /infra/containers", rr(auth.RoleAdmin, s.infraGate(s.handleInfraContainers)))
	mux.HandleFunc("GET /infra/cache", rr(auth.RoleAdmin, s.cacheGate(s.handleInfraCache)))
	mux.HandleFunc("POST /infra/containers/{id}/stop", rr(auth.RoleAdmin, s.infraGate(s.handleInfraStopContainer)))
	mux.HandleFunc("GET /infra/retention", rr(auth.RoleAdmin,
		gated(s.settingsStore != nil, "api.server.errors.settings_store_unavailable", s.handleRetention)))
}

func (s *Server) infraGate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireOperator(w, r, "api.infra.labels.surface", "api.scope.operator_verbs.read") {
			return
		}
		if s.infra == nil {
			writeErrKey(w, r, http.StatusServiceUnavailable, "api.infra.errors.unavailable")
			return
		}
		h(w, r)
	}
}

func (s *Server) cacheGate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireOperator(w, r, "api.infra.labels.surface", "api.scope.operator_verbs.read") {
			return
		}
		if s.sandbox.store == nil {
			writeErrKey(w, r, http.StatusServiceUnavailable, "api.infra.errors.cache_unavailable")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleInfraOverview(w http.ResponseWriter, r *http.Request) {
	snap := s.infra.Overview()
	for i := range snap.Problems {
		snap.Problems[i].Message = apperr.LocalizeCtx(r.Context(), snap.Problems[i].Err)
	}
	if k := snap.Runtime.Kube; k != nil && len(k.AgentImages) > 0 {
		// The snapshot is shared: localize into copies.
		kube := *k
		kube.AgentImages = append([]infra.AgentImageBuild(nil), k.AgentImages...)
		for i := range kube.AgentImages {
			if err := kube.AgentImages[i].Err; err != nil {
				kube.AgentImages[i].Message = apperr.LocalizeCtx(r.Context(), err)
			}
		}
		snap.Runtime.Kube = &kube
	}
	writeJSON(w, http.StatusOK, snap)
}

// The collector keeps only two hours of history; nothing longer is accepted.
var infraRanges = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"2h":  2 * time.Hour,
}

func (s *Server) handleInfraHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "1h"
	}
	d, ok := infraRanges[name]
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.infra.errors.bad_range", "range", name)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":            name,
		"interval_seconds": int(s.infra.Interval() / time.Second),
		"points":           s.infra.History(d),
	})
}

func (s *Server) handleInfraContainers(w http.ResponseWriter, r *http.Request) {
	snap := s.infra.Overview()
	wid, unrestricted := s.callerWorkspace(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"collected_at": snap.CollectedAt,
		"mode":         snap.Runtime.Mode,
		"containers":   s.markOrphans(r.Context(), s.withOwnerNames(r.Context(), s.infra.Containers(), wid, unrestricted)),
	})
}

// Another workspace's container keeps only its id: the task title is the other tenant's content.
func (s *Server) withOwnerNames(ctx context.Context, cs []infra.Container, wid string, unrestricted bool) []infra.Container {
	var ids []string
	for i := range cs {
		if cs[i].TaskID != "" && workspaceVisible(cs[i].WorkspaceID, wid, unrestricted) {
			ids = append(ids, cs[i].TaskID)
		}
	}
	if len(ids) == 0 {
		return cs
	}
	titles := s.taskTitles(ctx, ids)
	nameOf := s.projectNames(ctx)
	owners := s.sandboxOwners(ctx)
	for i := range cs {
		c := &cs[i]
		if c.TaskID == "" || !workspaceVisible(c.WorkspaceID, wid, unrestricted) {
			continue
		}
		if title, ok := titles[c.TaskID]; ok {
			c.TaskTitle = title
			continue
		}
		if c.Kind == infra.KindSandbox {
			if projectID, ok := sandbox.PrewarmProjectID(owners(c)); ok {
				c.ProjectName = nameOf(projectID)
			}
		}
	}
	return cs
}

func (s *Server) sandboxOwners(ctx context.Context) func(c *infra.Container) string {
	var live []*sandbox.Sandbox
	loaded := false
	return func(c *infra.Container) string {
		if _, ok := sandbox.PrewarmProjectID(c.TaskID); ok {
			return c.TaskID
		}
		if !loaded {
			loaded = true
			if s.sandbox.store != nil {
				if rows, err := s.sandbox.store.ListLive(ctx); err == nil {
					live = rows
				}
			}
		}
		return sandboxOwnerOf(c, live)
	}
}

func (s *Server) handleInfraStopContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c *infra.Container
	containers := s.infra.Containers()
	for i := range containers {
		if containers[i].ID == id {
			c = &containers[i]
			break
		}
	}
	if c == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.infra.errors.container_not_found", "id", id)
		return
	}
	var err error
	switch c.Kind {
	case infra.KindSandbox:
		if s.sandbox.mgr == nil || s.sandbox.store == nil {
			writeErrKey(w, r, http.StatusConflict, "api.infra.errors.not_stoppable", "kind", string(c.Kind))
			return
		}
		live, lerr := s.sandbox.store.ListLive(r.Context())
		if lerr != nil {
			writeError(w, r, http.StatusInternalServerError, lerr)
			return
		}
		owner := sandboxOwnerOf(c, live)
		if owner == "" {
			writeErrKey(w, r, http.StatusNotFound, "api.infra.errors.sandbox_not_found", "id", id)
			return
		}
		err = s.sandbox.mgr.Release(r.Context(), owner)
	case infra.KindAgent, infra.KindChat:
		err = s.stopAgentOrChat(r, c)
	default:
		writeErrKey(w, r, http.StatusConflict, "api.infra.errors.not_stoppable", "kind", string(c.Kind))
		return
	}
	if errors.Is(err, errNoContainerRuntime) {
		writeErrKey(w, r, http.StatusConflict, "api.infra.errors.not_stoppable", "kind", string(c.Kind))
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// An orphan is removed, not stopped, so the reconcile pass never replays it as a finished run.
func (s *Server) stopAgentOrChat(r *http.Request, c *infra.Container) error {
	if s.orphanAgent(r.Context(), c, time.Now()) {
		ctx, cancel := removalContext(r.Context())
		defer cancel()
		if err := s.removeOrphanContainer(ctx, c); err != nil {
			return err
		}
		s.infra.Forget(c.ID)
		return nil
	}
	if c.Kind == infra.KindAgent {
		// Mark before stopping: the run's exit is processed as soon as it dies and must read as
		// stopped, not failed.
		s.markStopped(r, infraRunID(c, s.kube != nil))
	}
	switch {
	case s.kube != nil:
		return s.kube.DeletePod(r.Context(), c.Name, c.Namespace)
	case s.docker != nil:
		timeout := 5
		return s.docker.ContainerStop(r.Context(), c.ID, container.StopOptions{Timeout: &timeout})
	}
	return errNoContainerRuntime
}

// Must match the id the dispatcher keys runs by: the pod name on Kubernetes, the 12-char short
// container id the launcher returns on Docker.
func infraRunID(c *infra.Container, kube bool) string {
	if kube {
		return c.Name
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// Match by container, not label: on Kubernetes the label holds the owner made DNS-safe
// ("prewarm:p1" becomes "prewarm-p1").
func sandboxOwnerOf(c *infra.Container, live []*sandbox.Sandbox) string {
	for _, sb := range live {
		if sb.ContainerID == "" {
			continue
		}
		if sb.ContainerID == c.ID || sb.ContainerID == c.Name {
			return sb.TaskID
		}
	}
	return ""
}
