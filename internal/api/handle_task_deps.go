package api

import (
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func (s *Server) handleListTaskDependencies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	blockedBy, err := s.taskStore.GetDependencies(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	blocking, err := s.taskStore.GetDependents(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	isBlocked, _, err := s.taskStore.IsBlocked(r.Context(), id)
	logx.Warn(err, "api.tasks.log.is_blocked_check", "task", id)
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":    id,
		"is_blocked": isBlocked,
		"blocked_by": blockedBy,
		"blocking":   blocking,
	})
}

func (s *Server) handleAddTaskDependency(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	var body struct {
		DependsOnTaskID string `json:"depends_on_task_id"`
		Kind            string `json:"kind"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.DependsOnTaskID = strings.TrimSpace(body.DependsOnTaskID)
	if body.DependsOnTaskID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.depends_on_required")
		return
	}
	// The upstream task must be visible too, or a dependency edge becomes an existence probe into
	// another workspace.
	if _, ok := s.taskForCaller(w, r, body.DependsOnTaskID); !ok {
		return
	}
	kind := task.DepKind(body.Kind)
	if kind == "" {
		kind = task.DepHard
	}
	if !kind.Valid() {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.dep_kind_invalid")
		return
	}
	// The worktree already exists, created from a main without the blocker's work; soft deps gate
	// nothing.
	if info, _ := s.stagesFor(t).Get(t.Stage); kind == task.DepHard && !info.Initial {
		initial, _ := s.stagesFor(t).Get(s.stagesFor(t).Initial())
		writeJSON(w, http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
			"current_stage": t.Stage,
		}, "api.tasks.errors.hard_dep_after_initial", "stage", stageLabelIn(initial, i18n.FromContext(r.Context()))))
		return
	}
	if err := s.taskStore.AddDependency(r.Context(), id, body.DependsOnTaskID, kind); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventDepAdded, body.DependsOnTaskID, "user",
		i18n.Text("api.tasks.timeline.depends_on", "task", body.DependsOnTaskID),
		map[string]string{"depends_on": body.DependsOnTaskID, "kind": string(kind)})
	s.recordEvent(r.Context(), body.DependsOnTaskID, task.EventDepAdded, id, "user",
		i18n.Text("api.tasks.timeline.dependent_added", "task", id),
		map[string]string{"dependent": id, "kind": string(kind)})

	full, getErr := s.taskStore.Get(r.Context(), id)
	logx.Warn(getErr, "api.tasks.log.reload_after_dep_add", "task", id)
	writeJSON(w, http.StatusCreated, full)
}

func (s *Server) handleRemoveTaskDependency(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	depID := r.PathValue("depId")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	if err := s.taskStore.RemoveDependency(r.Context(), id, depID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventDepRemoved, depID, "user",
		i18n.Text("api.tasks.timeline.dependency_removed", "task", depID), nil)
	// depID is unchecked: skip the mirror event for a task the caller cannot see, so other
	// workspaces' timelines stay untouched and the answer matches a nonexistent id.
	if s.depVisible(r, depID) {
		s.recordEvent(r.Context(), depID, task.EventDepRemoved, id, "user",
			i18n.Text("api.tasks.timeline.dependent_removed", "task", id), nil)
	}
	writeStatus(w, "removed")
}

func (s *Server) depVisible(r *http.Request, id string) bool {
	t, err := s.taskStore.GetRow(r.Context(), id)
	if err != nil || t == nil {
		return false
	}
	wid, unrestricted := s.callerWorkspace(r)
	return workspaceVisible(t.WorkspaceID, wid, unrestricted)
}
