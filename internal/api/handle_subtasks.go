package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func (s *Server) handleCreateSubtask(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if parent.IsSubtask() {
		writeJSON(w, http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
			"parent_id": parent.ParentID,
		}, "api.tasks.errors.subtask_of_subtask"))
		return
	}

	var body struct {
		Title            string                `json:"title"`
		Body             string                `json:"body"`
		StageAssignments map[task.Stage]string `json:"stage_assignments,omitempty"`
		Tags             []string              `json:"tags,omitempty"`
		Priority         string                `json:"priority,omitempty"`
		Stage            task.Stage            `json:"stage,omitempty"`
		DependsOn        []string              `json:"depends_on,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.title_required")
		return
	}

	assigns := make(map[task.Stage]string, len(parent.StageAssignments))
	for k, v := range parent.StageAssignments {
		assigns[k] = v
	}
	for k, v := range body.StageAssignments {
		assigns[k] = v
	}

	sub := &task.Task{
		Title:            body.Title,
		Body:             body.Body,
		ParentID:         parent.ID,
		ProjectIDs:       append([]string(nil), parent.ProjectIDs...),
		StageAssignments: assigns,
		Tags:             body.Tags,
		Priority:         body.Priority,
		WorkspaceID:      parent.WorkspaceID,
		BoardID:          parent.BoardID,
		TypeID:           parent.TypeID,
		OriginTaskID:     runOriginTask(r),
	}
	// Gate the initial stage before the insert so a refusal does not leave the subtask created in
	// Backlog.
	moveTo := body.Stage
	if moveTo != "" {
		sub.Stage = s.stagesFor(sub).Initial()
		if moveTo == sub.Stage {
			moveTo = ""
		} else if status, refusal, ok := s.initialStageGate(r.Context(), sub, moveTo); !ok {
			writeJSON(w, status, refusal)
			return
		}
	}
	if err := s.taskStore.Create(r.Context(), sub); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), sub.ID, task.EventTaskCreated, parent.ID, "user",
		i18n.Text("api.tasks.timeline.subtask_of", "parent", parent.ID, "title", sub.Title),
		map[string]string{"parent_id": parent.ID, "title": sub.Title})
	s.emit(r, "task.created", "task.created", sub.ID, "task",
		i18n.Text("api.tasks.audit.subtask_created", "title", sub.Title),
		map[string]string{"task_id": sub.ID, "parent_id": parent.ID, "title": sub.Title})
	s.publishTaskAutomationEvent(context.WithoutCancel(r.Context()), automation.EventTaskCreated, sub,
		map[string]any{"parent_id": parent.ID})

	// Must run before applyInitialStage: hard deps are refused once a task leaves the initial
	// stage.
	if !s.addSubtaskDependencies(w, r, sub.ID, parent.ID, body.DependsOn) {
		return
	}

	if moveTo != "" {
		s.applyInitialStage(r, sub, moveTo)
	}

	full, err := s.taskStore.Get(r.Context(), sub.ID)
	logx.Warn(err, "api.tasks.log.reload_subtask_after_create", "task", sub.ID)
	writeJSON(w, http.StatusCreated, full)
}

func (s *Server) addSubtaskDependencies(w http.ResponseWriter, r *http.Request, subID, parentID string, dependsOn []string) bool {
	for _, depID := range dependsOn {
		depID = strings.TrimSpace(depID)
		if depID == "" || depID == subID {
			continue
		}
		up, ok := s.taskForCaller(w, r, depID)
		if !ok {
			return false
		}
		if up.ParentID != parentID {
			writeJSON(w, http.StatusBadRequest, keyedRefusal(r.Context(), map[string]any{
				"offending":  depID,
				"parent_id":  parentID,
				"its_parent": up.ParentID,
			}, "api.tasks.errors.depends_on_not_sibling"))
			return false
		}
		if err := s.taskStore.AddDependency(r.Context(), subID, depID, task.DepHard); err != nil {
			writeError(w, r, http.StatusConflict, err)
			return false
		}
		s.recordEvent(r.Context(), subID, task.EventDepAdded, depID, "user",
			i18n.Text("api.tasks.timeline.depends_on", "task", depID),
			map[string]string{"depends_on": depID, "kind": string(task.DepHard)})
	}
	return true
}

func (s *Server) handleReorderSubtasks(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	children, err := s.taskStore.ListChildren(r.Context(), parent.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if key, fields, args := subtaskOrderProblem(children, body.IDs); key != "" {
		writeJSON(w, http.StatusBadRequest, keyedRefusal(r.Context(), fields, key, args...))
		return
	}
	if err = s.taskStore.SetChildOrder(r.Context(), parent.ID, body.IDs); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), parent.ID, task.EventSubtasksReordered, "", "user",
		i18n.Text("api.tasks.timeline.subtasks_reordered"),
		map[string]any{"ids": body.IDs})
	full, err := s.taskStore.ListChildren(r.Context(), parent.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"children": full})
}

func subtaskOrderProblem(children []task.Task, ids []string) (key string, fields map[string]any, args []any) {
	isChild := make(map[string]bool, len(children))
	for i := range children {
		isChild[children[i].ID] = true
	}
	seen := make(map[string]int, len(ids))
	for _, id := range ids {
		if !isChild[id] {
			return "api.tasks.errors.subtask_order_not_child",
				map[string]any{"offending": id}, []any{"id", id}
		}
		seen[id]++
	}
	var off []string
	for i := range children {
		if id := children[i].ID; seen[id] != 1 {
			off = append(off, id)
		}
	}
	if len(off) > 0 || len(ids) != len(children) {
		return "api.tasks.errors.subtask_order_incomplete",
			map[string]any{"offending": off}, []any{"ids", strings.Join(off, ", ")}
	}
	return "", nil, nil
}
