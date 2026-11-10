package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type sandboxView struct {
	TaskID         string         `json:"task_id"`
	TaskTitle      string         `json:"task_title,omitempty"`
	ProjectName    string         `json:"project_name,omitempty"`
	ContainerID    string         `json:"container_id"`
	Image          string         `json:"image"`
	Status         sandbox.Status `json:"status"`
	HostPorts      map[int]int    `json:"host_ports"`
	CreatedAt      time.Time      `json:"created_at"`
	LastActivityAt time.Time      `json:"last_activity_at"`
	IdleSeconds    int            `json:"idle_seconds"`
	ReleasedAt     *time.Time     `json:"released_at,omitempty"`
	PublishesPorts bool           `json:"publishes_ports"`
}

func toSandboxView(sb *sandbox.Sandbox) sandboxView {
	v := sandboxView{
		TaskID:         sb.TaskID,
		ContainerID:    sb.ContainerID,
		Image:          sb.Image,
		Status:         sb.Status,
		HostPorts:      sb.HostPorts,
		CreatedAt:      sb.CreatedAt,
		LastActivityAt: sb.LastActivityAt,
		IdleSeconds:    int(time.Since(sb.LastActivityAt).Seconds()),
		ReleasedAt:     sb.ReleasedAt,
	}
	if v.HostPorts == nil {
		v.HostPorts = map[int]int{}
	}
	return v
}

type queueEntryView struct {
	TaskID     string    `json:"task_id"`
	TaskTitle  string    `json:"task_title,omitempty"`
	Stage      string    `json:"stage"`
	Priority   int       `json:"priority"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// The sandbox table is install-wide: filter to the caller's workspace.
// Run keys are refused; an agent reaches its own sandbox through the MCP gateway.
func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	if refuseRunKey(w, r) {
		return
	}
	includeAll := r.URL.Query().Get("include") == "all"
	var rows []*sandbox.Sandbox
	var err error
	if includeAll {
		rows, err = s.sandbox.store.ListAll(r.Context())
	} else {
		rows, err = s.sandbox.store.ListLive(r.Context())
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	rowOf := s.taskRows(r)
	nameOf := s.projectNames(r.Context())
	visible := s.sandboxVisibility(r, rowOf)
	publishes := s.sandbox.mgr != nil && s.sandbox.mgr.PublishesPorts()
	out := make([]sandboxView, 0, len(rows))
	for _, sb := range rows {
		if visible(sb.WorkspaceID, sb.TaskID) {
			v := toSandboxView(sb)
			v.PublishesPorts = publishes
			if t := rowOf(sb.TaskID); t != nil {
				v.TaskTitle = t.Title
			} else if projectID, ok := sandbox.PrewarmProjectID(sb.TaskID); ok {
				v.ProjectName = nameOf(projectID)
			}
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectNames(ctx context.Context) func(projectID string) string {
	seen := map[string]string{}
	return func(projectID string) string {
		if s.projectStore == nil {
			return ""
		}
		if name, ok := seen[projectID]; ok {
			return name
		}
		name := ""
		if p, err := s.projectStore.Get(ctx, projectID); err == nil {
			name = p.Name
		}
		seen[projectID] = name
		return name
	}
}

func (s *Server) handleListSandboxQueue(w http.ResponseWriter, r *http.Request) {
	if refuseRunKey(w, r) {
		return
	}
	rows, err := s.sandbox.store.ListQueue(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	rowOf := s.taskRows(r)
	visible := s.sandboxVisibility(r, rowOf)
	out := make([]queueEntryView, 0, len(rows))
	for _, e := range rows {
		if !visible("", e.TaskID) {
			continue
		}
		v := queueEntryView{
			TaskID:     e.TaskID,
			Stage:      e.Stage,
			Priority:   e.Priority,
			EnqueuedAt: e.EnqueuedAt,
		}
		if t := rowOf(e.TaskID); t != nil {
			v.TaskTitle = t.Title
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) taskRows(r *http.Request) func(taskID string) *task.Task {
	seen := map[string]*task.Task{}
	return func(taskID string) *task.Task {
		if taskID == "" || s.taskStore == nil {
			return nil
		}
		if t, ok := seen[taskID]; ok {
			return t
		}
		t, err := s.taskStore.GetRow(r.Context(), taskID)
		if err != nil {
			t = nil
		}
		seen[taskID] = t
		return t
	}
}

// Strict match: a row with no workspace on it or its task is hidden from every restricted caller.
// Queue entries never carry one.
func (s *Server) sandboxVisibility(r *http.Request, rowOf func(string) *task.Task) func(rowWID, taskID string) bool {
	wid, unrestricted := s.callerWorkspace(r)
	if unrestricted {
		return func(string, string) bool { return true }
	}
	return func(rowWID, taskID string) bool {
		if rowWID == "" {
			if t := rowOf(taskID); t != nil {
				rowWID = t.WorkspaceID
			}
		}
		return workspaceVisible(rowWID, wid, false)
	}
}

func refuseRunKey(w http.ResponseWriter, r *http.Request) bool {
	if auth.APIKeyRunScope(r.Context()).IsRun() {
		writeErrKey(w, r, http.StatusForbidden, "api.sandbox.errors.run_key_refused")
		return true
	}
	return false
}

func (s *Server) handleDeleteTaskSandbox(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.sandbox.errors.task_id_required")
		return
	}
	if s.sandbox.mgr == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.sandbox.errors.manager_unavailable")
		return
	}
	if _, ok := s.taskForCaller(w, r, taskID); !ok {
		return
	}
	if err := s.sandbox.mgr.Release(r.Context(), taskID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveFromSandboxQueue(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	rows, err := s.sandbox.store.ListQueue(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	queued := false
	for _, e := range rows {
		queued = queued || e.TaskID == t.ID
	}
	if !queued {
		writeErrKey(w, r, http.StatusNotFound, "api.sandbox.errors.not_queued")
		return
	}
	if err := s.sandbox.mgr.Queue().Remove(r.Context(), t.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	_, addErr := s.taskStore.AddNote(r.Context(), t.ID, "", task.SourceSystem, "octopus",
		task.NoteOf("api.sandbox.comments.removed_from_queue", "who", author.Name))
	logx.Warn(addErr, "api.sandbox.log.note_removed_from_queue", "task", t.ID)
	w.WriteHeader(http.StatusNoContent)
}

type taskSandboxView struct {
	sandboxView
	OwnerID string         `json:"owner_id"`
	URLs    map[int]string `json:"urls,omitempty"`
}

// No live sandbox answers 204, not 404: the drawer asks on every open and a 404 shows as a console
// error.
func (s *Server) handleGetTaskSandbox(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.sandbox.mgr == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	owner := t.ID
	if t.IsSubtask() {
		owner = t.ParentID
	}
	sb, err := s.sandbox.mgr.Sandbox(r.Context(), owner)
	if errors.Is(err, sandbox.ErrNotFound) || (err == nil && (sb.Status == sandbox.StatusEvicted || sb.Status == sandbox.StatusFailed)) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	view := taskSandboxView{
		sandboxView: toSandboxView(sb),
		OwnerID:     owner,
		URLs:        s.sandbox.mgr.HumanURLs(sb),
	}
	view.PublishesPorts = s.sandbox.mgr.PublishesPorts()
	writeJSON(w, http.StatusOK, view)
}
