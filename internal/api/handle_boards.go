package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/patch"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

// The board store is unscoped on purpose (dispatcher, janitor and terminality need every board);
// workspace filtering of boards, stages and task types happens here, in the API layer.

func (s *Server) visibleBoards(r *http.Request) []stagepkg.BoardInfo {
	wid, unrestricted := s.callerWorkspace(r)
	all := s.boards.BoardList()
	out := make([]stagepkg.BoardInfo, 0, len(all))
	for _, b := range all {
		if workspaceVisible(b.WorkspaceID, wid, unrestricted) {
			out = append(out, b)
		}
	}
	return out
}

// Another workspace's board answers the same 404 as a missing one, so tenants cannot probe for each
// other's flows.
func (s *Server) boardForCaller(w http.ResponseWriter, r *http.Request, id string) (stagepkg.BoardInfo, bool) {
	b, ok := s.boardInfo(id)
	if ok {
		wid, unrestricted := s.callerWorkspace(r)
		ok = workspaceVisible(b.WorkspaceID, wid, unrestricted)
	}
	if !ok {
		writeErrKey(w, r, http.StatusNotFound, "api.boards.errors.unknown_board", "board", id)
		return stagepkg.BoardInfo{}, false
	}
	return b, true
}

func (s *Server) boardVisibleToCaller(r *http.Request, id string) bool {
	b, ok := s.boardInfo(id)
	if !ok {
		return false
	}
	wid, unrestricted := s.callerWorkspace(r)
	return workspaceVisible(b.WorkspaceID, wid, unrestricted)
}

// Seeds a board on first use: after adoption the shipped `default` board belongs to one workspace,
// and any other workspace would otherwise have no flow to create tasks on.
func (s *Server) callerDefaultBoard(r *http.Request) string {
	wid, unrestricted := s.callerWorkspace(r)
	if unrestricted {
		// Dev mode has no identity; on a single-workspace install use that workspace, or the
		// dashboard
		// points at the unowned seed "default" while tasks are created in the real one.
		wid = s.soleWorkspaceID(r.Context())
	}
	if wid != "" && s.stageAdmin != nil {
		b, err := s.stageAdmin.EnsureDefaultBoard(r.Context(), wid)
		logx.Warn(err, "api.boards.log.seed_default_board", "workspace", wid)
		if err == nil {
			return b.ID
		}
	}
	if wid, ok := s.requestWorkspace(r); ok {
		return s.boards.DefaultBoardIDFor(wid)
	}
	return ""
}

// A shared stage id has one label per board; the caller's default board's label wins.
func (s *Server) visibleCatalog(r *http.Request) []types.StageInfo {
	seen := map[types.Stage]bool{}
	out := []types.StageInfo{}
	boards := s.visibleBoards(r)
	if def := s.callerDefaultBoard(r); def != "" {
		sort.SliceStable(boards, func(i, j int) bool { return boards[i].ID == def && boards[j].ID != def })
	}
	for _, b := range boards {
		cols := s.boards.Board(b.ID).List()
		for i := range cols {
			if !seen[cols[i].ID] {
				seen[cols[i].ID] = true
				out = append(out, cols[i])
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return stagepkg.LocalizeSeeded(out, i18n.RequestLocale(r))
}

func (s *Server) visibleTypeCatalog(r *http.Request) []stagepkg.TaskTypeInfo {
	seen := map[string]bool{}
	out := []stagepkg.TaskTypeInfo{}
	for _, b := range s.visibleBoards(r) {
		tt := s.boards.BoardTypes(b.ID)
		for i := range tt {
			t := &tt[i]
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, stagepkg.TaskTypeInfo{
				ID: t.ID, Label: t.Label, Icon: t.Icon, Color: t.Color,
				WorkspaceID: t.WorkspaceID, Position: t.Position, Skips: []types.Stage{},
			})
		}
	}
	return out
}

func (s *Server) emitBoardsChanged(r *http.Request, kind audit.EventKind, boardID, summary string) {
	// Only the actor's visible boards: the full list would leak every tenant's flow names to the
	// webhook endpoint.
	s.emit(r, kind, webhook.EventBoardsChanged, boardID, "board", summary,
		map[string]any{"boards": s.visibleBoards(r)})
}

func localizedBoards(r *http.Request, boards []stagepkg.BoardInfo) []stagepkg.BoardInfo {
	locale := i18n.RequestLocale(r)
	out := make([]stagepkg.BoardInfo, len(boards))
	for i := range boards {
		out[i] = stagepkg.LocalizeSeededBoard(boards[i], locale)
	}
	return out
}

func (s *Server) handleListBoards(w http.ResponseWriter, r *http.Request) {
	// Must run before visibleBoards: it may seed the workspace's default board, which the list has
	// to include.
	defaultID := s.callerDefaultBoard(r)
	boards := s.visibleBoards(r)
	if s.taskStore != nil {
		for i := range boards {
			tasks, err := s.taskStore.ListByBoard(r.Context(), boards[i].ID)
			if err != nil {
				continue
			}
			boards[i].TaskCount = len(tasks)
			boards[i].OpenCount = openTaskCount(tasks, s.boards.Board(boards[i].ID))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"boards":  localizedBoards(r, boards),
		"default": defaultID,
	})
}

func openTaskCount(tasks []task.Task, reg stagepkg.Registry) int {
	n := 0
	for i := range tasks {
		t := &tasks[i]
		if t.IsSubtask() || t.ArchivedAt != nil {
			continue
		}
		if info, _ := reg.Get(t.Stage); info.Terminal {
			continue
		}
		n++
	}
	return n
}

func (s *Server) handleListBoardStages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.boardForCaller(w, r, id); !ok {
		return
	}
	reg := s.boards.Board(id)
	if typeID := r.URL.Query().Get("type"); typeID != "" {
		if s.stageAdmin != nil && !s.stageAdmin.HasBoardType(id, typeID) {
			writeErrKey(w, r, http.StatusNotFound, "api.boards.errors.board_lacks_type", "board", id, "type", typeID)
			return
		}
		reg = s.boards.BoardFor(id, typeID)
	}
	writeJSON(w, http.StatusOK, boardColumns(r, reg))
}

func boardColumns(r *http.Request, reg stagepkg.Registry) []types.StageInfo {
	cols := reg.List()
	if v := r.URL.Query().Get("include_virtual"); v == "1" || v == "true" {
		cols = reg.ListWithVirtual()
	}
	return stagepkg.LocalizeSeeded(cols, i18n.RequestLocale(r))
}

type CreateBoardRequest struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Stages      []types.StageInfo `json:"stages,omitempty"`
	CloneFrom   string            `json:"clone_from,omitempty"`
	Default     bool              `json:"default,omitempty"`
}

func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	var req CreateBoardRequest
	if !decodeBody(w, r, &req) {
		return
	}
	stages, ok := s.boardSeedStages(w, r, req)
	if !ok {
		return
	}
	// Always the caller's workspace, never one from the body, so a request cannot create a board in
	// another tenancy.
	boardWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	info := stagepkg.BoardInfo{
		ID: req.ID, Label: req.Label, Description: req.Description,
		WorkspaceID: boardWID,
	}
	if err := s.stageAdmin.CreateBoard(r.Context(), info, stages); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Default {
		if err := s.stageAdmin.SetDefaultBoard(r.Context(), req.ID); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	s.addStagePriorities(r.Context(), s.boards.Board(req.ID).List())
	s.emitBoardsChanged(r, audit.KindBoardCreated, req.ID,
		i18n.Text("api.boards.audit.board_created", "board", req.ID, "stages", len(stages)))
	writeJSON(w, http.StatusCreated, map[string]any{
		"board":  stagepkg.LocalizeSeededBoard(info, i18n.RequestLocale(r)),
		"stages": s.boardStages(r, req.ID),
	})
}

// Clone sources go through boardForCaller: cloning reads columns and labels, so a board the caller
// cannot list must not be clonable.
func (s *Server) boardSeedStages(
	w http.ResponseWriter, r *http.Request, req CreateBoardRequest,
) ([]types.StageInfo, bool) {
	switch {
	case len(req.Stages) > 0:
		return req.Stages, true
	case req.CloneFrom != "":
		if _, ok := s.boardForCaller(w, r, req.CloneFrom); !ok {
			return nil, false
		}
		return s.boards.Board(req.CloneFrom).List(), true
	default:
		// Seed from the caller's visible catalog only; the install catalog would leak another
		// tenant's labels.
		return stagepkg.SeedFrom(s.visibleCatalog(r), i18n.RequestLocale(r)), true
	}
}

// The id is immutable: it is stamped on every task row and used as a metric label.
type BoardPatch struct {
	Label       *string `json:"label"`
	Description *string `json:"description"`
	// Only true is honored: exactly one board per workspace is default, so a board loses it only by
	// another gaining it.
	Default *bool `json:"default"`
}

func (s *Server) handleUpdateBoard(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	id := r.PathValue("id")
	info, ok := s.boardForCaller(w, r, id)
	if !ok {
		return
	}
	var p BoardPatch
	if !decodeBody(w, r, &p) {
		return
	}
	patch.Set(&info.Label, p.Label)
	patch.Set(&info.Description, p.Description)
	if err := s.stageAdmin.UpdateBoard(r.Context(), info); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	kind := audit.KindBoardUpdated
	summary := i18n.Text("api.boards.audit.board_updated", "board", id)
	if p.Default != nil && *p.Default {
		if err := s.stageAdmin.SetDefaultBoard(r.Context(), id); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		kind, summary = audit.KindBoardDefaults, i18n.Text("api.boards.audit.default_board_changed", "board", id)
	}
	s.emitBoardsChanged(r, kind, id, summary)
	writeJSON(w, http.StatusOK, localizedBoards(r, s.visibleBoards(r)))
}

func (s *Server) handleDeleteBoard(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.boardForCaller(w, r, id); !ok {
		return
	}
	moveTo := r.URL.Query().Get("move_to")
	fallback := types.Stage(r.URL.Query().Get("stage"))
	moved := 0

	// Second pass catches tasks created between the list and the delete; a board still holding
	// tasks after it is not deleted.
	for pass := range 2 {
		held, listErr := s.taskStore.ListByBoard(r.Context(), id)
		if listErr != nil {
			writeError(w, r, http.StatusInternalServerError, listErr)
			return
		}
		if len(held) == 0 {
			break
		}
		if pass == 1 {
			writeJSON(w, http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
				"task_count": len(held),
			}, "api.boards.errors.board_still_receiving", "board", id, "count", len(held)))
			return
		}
		status, body, ok := s.relocateBoardTasks(r, held, id, moveTo, fallback, &moved)
		if !ok {
			writeJSON(w, status, body)
			return
		}
	}

	if err := s.stageAdmin.DeleteBoard(r.Context(), id); err != nil {
		writeError(w, r, http.StatusConflict,
			apperr.Wrap(err, "api.boards.errors.delete_after_move_failed", "count", moved, "dest", moveTo))
		return
	}
	s.emitBoardsChanged(r, audit.KindBoardDeleted, id,
		i18n.Text("api.boards.audit.board_deleted", "board", id, "count", moved, "dest", moveTo))
	writeJSON(w, http.StatusOK, map[string]any{
		"boards":      localizedBoards(r, s.visibleBoards(r)),
		"moved_tasks": moved,
		"moved_to":    moveTo,
	})
}

// A destination in another workspace is refused as invalid; that is what stops moving tasks across
// tenants.
func (s *Server) relocateBoardTasks(
	r *http.Request, held []task.Task, from, moveTo string, fallback types.Stage, moved *int,
) (int, any, bool) {
	if moveTo == "" {
		return http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
			"task_count": len(held),
		}, "api.boards.errors.board_holds_tasks", "board", from, "count", len(held)), false
	}
	if moveTo == from || !s.boardVisibleToCaller(r, moveTo) {
		return http.StatusBadRequest, keyedRefusal(r.Context(), nil, "api.boards.errors.invalid_destination_board", "dest", moveTo), false
	}
	dest := s.boards.Board(moveTo)
	if fallback == "" {
		fallback = dest.Initial()
	}
	if !dest.Valid(fallback) || isVirtual(fallback) {
		return http.StatusBadRequest, keyedRefusal(r.Context(), nil,
			"api.boards.errors.stage_missing_on_board", "stage", fallback, "board", moveTo), false
	}
	n, err := s.moveTasksToBoard(r.Context(), held, from, moveTo, dest, fallback)
	*moved += n
	if err != nil {
		return http.StatusInternalServerError, errRefusal(r.Context(), map[string]any{
			"moved": n,
		}, apperr.Wrap(err, "api.boards.errors.board_left_in_place")), false
	}
	return 0, nil, true
}

// Administrative move only: no agent is dispatched. Errors are returned, not skipped, because the
// caller
// deletes the board next and a task left behind would point at a board that no longer exists.
func (s *Server) moveTasksToBoard(ctx context.Context,
	tasks []task.Task, from, to string, dest stagepkg.Registry, fallback types.Stage,
) (int, error) {
	moved := 0
	idle := task.StatusIdle
	for i := range tasks {
		t := &tasks[i]
		stage := t.Stage
		if !dest.Valid(stage) || isVirtual(stage) {
			stage = fallback
		}
		s.recordSkippedApprovals(ctx, t, t.Stage, task.OffBoard, task.GateBoardDeleted, "octopus")
		if _, err := s.taskStore.Update(ctx, t.ID, task.Patch{
			BoardID: &to, Stage: &stage, Status: &idle,
		}); err != nil {
			return moved, apperr.Wrap(err, "api.boards.errors.move_task_failed", "task", t.ID, "board", from)
		}
		s.recordEvent(ctx, t.ID, task.EventStageEntered, "", "octopus",
			i18n.Text("api.boards.timeline.moved_board_deleted", "from", t.Stage, "to", stage, "board", from),
			map[string]string{
				"from": string(t.Stage), "to": string(stage),
				"from_board": from, "to_board": to, "reason": "board_deleted",
			})
		webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventTaskStageChanged,
			map[string]string{
				"task_id": t.ID, "from": string(t.Stage), "to": string(stage),
				"from_board": from, "to_board": to,
			})
		moved++
	}
	return moved, nil
}

func (s *Server) boardInfo(id string) (stagepkg.BoardInfo, bool) {
	for _, b := range s.boards.BoardList() {
		if b.ID == id {
			return b, true
		}
	}
	return stagepkg.BoardInfo{}, false
}
