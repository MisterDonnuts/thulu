package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type agentView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Project    string `json:"project"`
	TaskID     string `json:"task_id,omitempty"`
	Instance   string `json:"instance"`
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
	StartedAt  string `json:"started_at"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	// OutputDir is a daemon-host path: send it only to unrestricted callers.
	OutputDir string `json:"output_dir,omitempty"`
}

func (s *Server) toView(r *http.Request, a *agent.Agent) agentView {
	v := agentView{
		ID:         a.ID,
		Name:       a.Name,
		Type:       a.Type,
		Project:    a.Project,
		TaskID:     a.TaskID,
		Instance:   a.Instance,
		Status:     string(a.Status),
		Uptime:     formatDuration(a.Uptime()),
		StartedAt:  a.StartedAt.Format(time.RFC3339),
		ExitCode:   a.ExitCode,
		StopReason: a.StopReason,
	}
	if _, unrestricted := s.callerWorkspace(r); unrestricted {
		outDir, err := s.resolveOutputDir(r.Context(), a)
		logx.Warn(err, "api.agents.log.resolve_output_dir", "id", a.ID)
		v.OutputDir = outDir
	}
	return v
}

func podView(p *kubeapi.RunningPod) agentView {
	status := agent.StatusRunning
	uptime := time.Since(p.StartedAt)
	if !p.Running {
		status = agent.StatusExited
		if !p.FinishedAt.IsZero() {
			uptime = p.FinishedAt.Sub(p.StartedAt)
		} else {
			uptime = 0
		}
	}
	return agentView{
		ID:        p.Name,
		Name:      p.Name,
		Type:      p.AgentType,
		TaskID:    p.TaskID,
		Instance:  p.InstanceID,
		Status:    string(status),
		Uptime:    formatDuration(uptime),
		StartedAt: p.StartedAt.Format(time.RFC3339),
		ExitCode:  p.ExitCode,
	}
}

// Runs started by daemons predating thulu.workspace_id carry no label; fall back to their task's
// workspace.
// A run with neither is unattributable, and workspaceVisible treats it as nobody's.
func (s *Server) dockerRunWorkspace(ctx context.Context, a *agent.Agent) string {
	if a.WorkspaceID != "" {
		return a.WorkspaceID
	}
	if a.TaskID == "" || s.taskStore == nil {
		return ""
	}
	t, err := s.taskStore.GetRow(ctx, a.TaskID)
	if err != nil {
		return ""
	}
	return t.WorkspaceID
}

func (s *Server) agentForCaller(w http.ResponseWriter, r *http.Request) (*agent.Agent, bool) {
	id := r.PathValue("id")
	return forCaller(s, w, r,
		func() (*agent.Agent, error) { return s.registry.Get(id) },
		func(a *agent.Agent) string { return s.dockerRunWorkspace(r.Context(), a) },
		workspaceVisible,
		func(err error) error {
			if err != nil {
				return err
			}
			return apperr.NotFound("api.agents.errors.agent_not_found", "name", id)
		})
}

func (s *Server) podForCaller(w http.ResponseWriter, r *http.Request) (kubeapi.RunningPod, bool) {
	id := r.PathValue("id")
	p, ok := s.kube.FindPod(r.Context(), id)
	if ok {
		wid, unrestricted := s.callerWorkspace(r)
		ok = workspaceVisible(p.WorkspaceID, wid, unrestricted)
	}
	if !ok {
		writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.agent_not_found", "name", id)
		return kubeapi.RunningPod{}, false
	}
	return p, true
}

// In K8s mode nothing feeds the registry, so list pods. Both sources are install-wide and must be
// filtered to the caller's tenant.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	wid, unrestricted := s.callerWorkspace(r)
	if s.kube != nil {
		pods, err := s.kube.ListPodsAll(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		views := make([]agentView, 0, len(pods))
		for i := range pods {
			if !workspaceVisible(pods[i].WorkspaceID, wid, unrestricted) {
				continue
			}
			views = append(views, podView(&pods[i]))
		}
		writeJSON(w, http.StatusOK, views)
		return
	}
	agents := s.registry.List()
	views := make([]agentView, 0, len(agents))
	for _, a := range agents {
		if !workspaceVisible(s.dockerRunWorkspace(r.Context(), a), wid, unrestricted) {
			continue
		}
		views = append(views, s.toView(r, a))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	if s.kube != nil {
		p, ok := s.podForCaller(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, podView(&p))
		return
	}
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toView(r, a))
}

func (s *Server) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	if runner.IsRunnerRun(r.PathValue("id")) {
		s.stopRunnerRun(w, r)
		return
	}
	if s.kube != nil {
		s.deleteAgentPod(w, r, "stopped")
		return
	}
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}
	timeout := 5
	// Mark before stopping: the exit is processed as soon as the container dies and must read as
	// stopped, not failed.
	s.markStopped(r, a.ID)
	if err := s.docker.ContainerStop(r.Context(), a.ID, container.StopOptions{Timeout: &timeout}); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "agent": a.Name})
}

func (s *Server) markStopped(r *http.Request, runID string) {
	if s.taskDispatcher != nil {
		s.taskDispatcher.MarkStopped(runID, authorLabel(r))
	}
}

func (s *Server) handleRemoveAgent(w http.ResponseWriter, r *http.Request) {
	if runner.IsRunnerRun(r.PathValue("id")) {
		s.stopRunnerRun(w, r)
		return
	}
	if s.kube != nil {
		s.deleteAgentPod(w, r, "removed")
		return
	}
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}
	if err := s.docker.ContainerRemove(r.Context(), a.ID, container.RemoveOptions{Force: true}); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.registry.Remove(a.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "agent": a.Name})
}

func (s *Server) deleteAgentPod(w http.ResponseWriter, r *http.Request, verb string) {
	p, ok := s.podForCaller(w, r)
	if !ok {
		return
	}
	s.markStopped(r, p.Name)
	if err := s.kube.DeletePod(r.Context(), p.Name, p.Namespace); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": verb, "agent": p.Name})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.kube != nil {
		s.handlePodLogs(w, r)
		return
	}
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}

	follow := r.URL.Query().Get("follow") == "true"
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "100"
	}

	reader, err := s.docker.ContainerLogs(r.Context(), a.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain")
	if follow {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(http.StatusOK)

	if err := dockerx.Demux(flushOnWrite{w}, reader); err != nil {
		return
	}
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.podForCaller(w, r)
	if !ok {
		return
	}
	follow := r.URL.Query().Get("follow") == "true"
	tail := int64(100)
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			tail = n
		}
	}
	reader, err := s.kube.PodLogsTail(r.Context(), p.Name, p.Namespace, follow, tail)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain")
	if follow {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(flushOnWrite{w}, reader); err != nil {
		return
	}
}

type flushOnWrite struct{ w http.ResponseWriter }

func (f flushOnWrite) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}
