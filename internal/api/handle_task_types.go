package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func (s *Server) handleListTaskTypeIcons(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"icons": stagepkg.TaskTypeIcons})
}

func (s *Server) handleListTaskTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"task_types": s.visibleTypeCatalog(r)})
}

func (s *Server) handleListBoardTypes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.boardForCaller(w, r, id); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_types": s.boardTypesWithCounts(r.Context(), id),
		"untyped":    s.taskCountsByType(r.Context(), id)[""],
	})
}

// One grouped query per board: the editor calls this on every render.
func (s *Server) boardTypesWithCounts(ctx context.Context, boardID string) []stagepkg.TaskTypeInfo {
	tt := s.boards.BoardTypes(boardID)
	counts := s.taskCountsByType(ctx, boardID)
	for i := range tt {
		tt[i].TaskCount = counts[tt[i].ID]
	}
	return tt
}

func (s *Server) taskCountsByType(ctx context.Context, boardID string) map[string]int {
	out := map[string]int{}
	if s.taskStore == nil {
		return out
	}
	counts, err := s.taskStore.CountByType(ctx)
	if err != nil {
		logx.Warn(err, "api.boards.log.count_by_type", "board", boardID)
		return out
	}
	for _, c := range counts {
		if c.BoardID == boardID {
			out[c.TypeID] = c.Count
		}
	}
	return out
}

type SetBoardTypesRequest struct {
	Types []stagepkg.TaskTypeInfo `json:"types"`
}

func (s *Server) handleSetBoardTypes(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	id := r.PathValue("id")
	board, ok := s.boardForCaller(w, r, id)
	if !ok {
		return
	}
	var req SetBoardTypesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	for i := range req.Types {
		if req.Types[i].Skips == nil {
			req.Types[i].Skips = []types.Stage{}
		}
		// The owning board's workspace, never the body's: otherwise a request could file a type
		// under another tenant.
		req.Types[i].WorkspaceID = board.WorkspaceID
	}

	// Validate before any relocation runs; the store's later validation would come after tasks were
	// already re-typed.
	if err := stagepkg.ValidateBoardTypes(s.boards.Board(id).List(), req.Types); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	plan, status, body, ok := s.planTypeRelocations(id, req.Types, r)
	if !ok {
		writeJSON(w, status, body)
		return
	}
	moved, err := s.applyTypeRelocations(r.Context(), id, plan)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errRefusal(r.Context(), map[string]any{
			"moved": moved,
		}, apperr.Wrap(err, "api.boards.errors.type_list_unchanged")))
		return
	}

	if err := s.stageAdmin.SaveBoardTypes(r.Context(), id, req.Types); err != nil {
		writeError(w, r, http.StatusBadRequest,
			apperr.Wrap(err, "api.boards.errors.types_save_after_move_failed", "count", moved))
		return
	}
	tt := s.boardTypesWithCounts(r.Context(), id)
	s.emit(r, audit.KindTaskTypesChanged, webhook.EventTaskTypesChanged, id, "board",
		i18n.Text("api.boards.audit.task_types_changed", "board", id, "types", len(tt), "count", moved),
		map[string]any{"board": id, "task_types": tt})
	writeJSON(w, http.StatusOK, map[string]any{"task_types": tt, "moved_tasks": moved})
}

type typeRelocation struct {
	tasks   []task.Task
	toType  string
	toStage types.Stage
	reason  string
}

func (s *Server) planTypeRelocations(
	boardID string, incoming []stagepkg.TaskTypeInfo, r *http.Request,
) ([]typeRelocation, int, any, bool) {
	q := r.URL.Query()
	byID := make(map[string]stagepkg.TaskTypeInfo, len(incoming))
	for i := range incoming {
		byID[incoming[i].ID] = incoming[i]
	}

	var plan []typeRelocation
	current := s.boards.BoardTypes(boardID)
	for i := range current {
		if _, kept := byID[current[i].ID]; kept {
			continue
		}
		status, body, move, ok := s.planRemovedTypeRelocation(r.Context(), boardID, current[i].ID, byID, q)
		if !ok {
			return nil, status, body, false
		}
		if move != nil {
			plan = append(plan, *move)
		}
	}

	for i := range incoming {
		status, body, moves, ok := s.planSkipRelocations(r.Context(), boardID, incoming[i], q)
		if !ok {
			return nil, status, body, false
		}
		plan = append(plan, moves...)
	}
	return plan, 0, nil, true
}

func (s *Server) planRemovedTypeRelocation(ctx context.Context,
	boardID, removed string, byID map[string]stagepkg.TaskTypeInfo, q url.Values,
) (int, any, *typeRelocation, bool) {
	held, err := s.taskStore.ListByBoardType(ctx, boardID, removed)
	if err != nil {
		return http.StatusInternalServerError, errRefusal(ctx, nil, err), nil, false
	}
	if len(held) == 0 {
		return 0, nil, nil, true
	}
	if !q.Has("move_to") {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"task_type":  removed,
			"task_count": len(held),
		}, "api.boards.errors.type_holds_tasks", "type", removed, "count", len(held)), nil, false
	}
	dest := q.Get("move_to")
	if _, offered := byID[dest]; dest != "" && !offered {
		return http.StatusBadRequest, keyedRefusal(ctx, nil, "api.boards.errors.invalid_destination_type", "dest", dest), nil, false
	}
	reg := stagepkg.NewSnapshot(stagepkg.FilterStages(
		s.boards.Board(boardID).List(), byID[dest].Skips))
	for i := range held {
		if !reg.Valid(held[i].Stage) {
			return http.StatusConflict, keyedRefusal(ctx, map[string]any{
				"task_id":   held[i].ID,
				"stage":     string(held[i].Stage),
				"task_type": dest,
			}, "api.boards.errors.destination_type_skips_stage",
				"task", held[i].ID, "stage", held[i].Stage, "type", dest), nil, false
		}
	}
	return 0, nil, &typeRelocation{tasks: held, toType: dest, reason: "task_type_removed"}, true
}

func (s *Server) planSkipRelocations(ctx context.Context,
	boardID string, t stagepkg.TaskTypeInfo, q url.Values,
) (int, any, []typeRelocation, bool) {
	was := map[types.Stage]bool{}
	current := s.boards.BoardTypes(boardID)
	for i := range current {
		if current[i].ID != t.ID {
			continue
		}
		for _, sk := range current[i].Skips {
			was[sk] = true
		}
	}
	reg := stagepkg.NewSnapshot(stagepkg.FilterStages(s.boards.Board(boardID).List(), t.Skips))
	var plan []typeRelocation
	for _, sk := range t.Skips {
		if was[sk] {
			continue
		}
		held, err := s.taskStore.ListInBoardTypeStage(ctx, boardID, t.ID, string(sk))
		if err != nil {
			return http.StatusInternalServerError, errRefusal(ctx, nil, err), nil, false
		}
		if len(held) == 0 {
			continue
		}
		dest := types.Stage(q.Get("move_to_stage"))
		if dest == "" || !reg.Valid(dest) || isVirtual(dest) {
			return http.StatusConflict, keyedRefusal(ctx, map[string]any{
				"task_type":        t.ID,
				"stage":            string(sk),
				"task_count":       len(held),
				"available_stages": reg.List(),
			}, "api.boards.errors.skip_strands_tasks", "count", len(held), "type", t.ID, "stage", sk), nil, false
		}
		plan = append(plan, typeRelocation{tasks: held, toType: t.ID, toStage: dest, reason: "stage_skipped"})
	}
	return 0, nil, plan, true
}

// No agent dispatch here. A failed move aborts: the caller must not save a type list that no longer
// matches where tasks are.
func (s *Server) applyTypeRelocations(ctx context.Context, boardID string, plan []typeRelocation) (int, error) {
	moved := 0
	idle := task.StatusIdle
	for _, rel := range plan {
		for i := range rel.tasks {
			t := &rel.tasks[i]
			p := task.Patch{Status: &idle}
			toType := rel.toType
			p.TypeID = &toType
			stage := t.Stage
			if rel.toStage != "" && rel.toStage != t.Stage {
				stage = rel.toStage
				p.Stage = &stage
				s.recordSkippedApprovals(ctx, t, t.Stage, stage, task.GateTypeRelocation, "octopus")
			}
			if _, err := s.taskStore.Update(ctx, t.ID, p); err != nil {
				return moved, apperr.Wrap(err, "api.boards.errors.retype_failed", "task", t.ID, "board", boardID)
			}
			s.recordEvent(ctx, t.ID, task.EventStageEntered, "", "octopus",
				i18n.Text("api.boards.timeline.moved_type_change", "from", t.Stage, "to", stage, "reason", rel.reason),
				map[string]string{
					"from": string(t.Stage), "to": string(stage),
					"from_type": t.TypeID, "to_type": toType,
					"board": boardID, "reason": rel.reason,
				})
			webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventTaskStageChanged,
				map[string]string{
					"task_id": t.ID, "from": string(t.Stage), "to": string(stage),
					"from_type": t.TypeID, "to_type": toType, "board": boardID,
				})
			moved++
		}
	}
	return moved, nil
}
