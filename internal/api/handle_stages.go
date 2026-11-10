package api

import (
	"context"
	"log"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/patch"
	"github.com/MisterDonnuts/thulu/internal/settings"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func (s *Server) stageAdminOK(w http.ResponseWriter, r *http.Request) bool {
	if s.stageAdmin == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.stages.errors.registry_read_only")
		return false
	}
	return true
}

// Reject an unknown or foreign board id instead of falling back: a typo must not edit the wrong
// flow.
func (s *Server) boardParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("board")
	if id == "" {
		return s.callerDefaultBoard(r), true
	}
	if _, ok := s.boardForCaller(w, r, id); !ok {
		return "", false
	}
	return id, true
}

func (s *Server) emitStagesChanged(r *http.Request, kind audit.EventKind, boardID, stageID, summary string) {
	s.emit(r, kind, webhook.EventStagesChanged, stageID, "stage", summary,
		map[string]any{"board": boardID, "stages": s.boards.Board(boardID).List()})
}

// Editors display this response as the board, so seeded names must be localized here; the webhook
// keeps stored names.
func (s *Server) boardStages(r *http.Request, boardID string) []types.StageInfo {
	return stagepkg.LocalizeSeeded(s.boards.Board(boardID).List(), i18n.RequestLocale(r))
}

type CreateStageRequest struct {
	types.StageInfo
	Index *int `json:"index,omitempty"`
}

func (s *Server) handleCreateStage(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	boardID, ok := s.boardParam(w, r)
	if !ok {
		return
	}
	var req CreateStageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	board := s.boards.Board(boardID).List()
	if _, exists := lookupStage(board, req.ID); exists || req.ID == "" {
		writeErrKey(w, r, http.StatusConflict, "api.stages.errors.stage_exists_or_missing_id", "stage", req.ID)
		return
	}
	idx := defaultInsertIndex(board)
	if req.Index != nil {
		idx = clamp(*req.Index, 0, len(board))
	}
	if req.Initial {
		board = clearInitial(board)
	}
	board = append(board[:idx], append([]types.StageInfo{req.StageInfo}, board[idx:]...)...)
	if err := s.stageAdmin.SaveBoard(r.Context(), boardID, board); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.addStagePriorities(r.Context(), s.boards.Board(boardID).List())
	s.emitStagesChanged(r, audit.KindStageCreated, boardID, string(req.ID),
		i18n.Text("api.stages.audit.stage_created", "stage", req.ID, "board", boardID))
	writeJSON(w, http.StatusCreated, s.boardStages(r, boardID))
}

// Old clients still send needs_agent; the decoder must keep ignoring unknown fields.
type StagePatch struct {
	Label              *string                      `json:"label"`
	Color              *string                      `json:"color"`
	AutoAdvance        *bool                        `json:"auto_advance"`
	BlockedByFlags     *bool                        `json:"blocked_by_flags"`
	Initial            *bool                        `json:"initial"`
	Terminal           *bool                        `json:"terminal"`
	IngestFlags        *bool                        `json:"ingest_flags"`
	PushBranch         *bool                        `json:"push_branch"`
	OpenPR             *bool                        `json:"open_pr"`
	WipGroup           *string                      `json:"wip_group"`
	RequiredApprovals  *[]types.ApprovalRequirement `json:"required_approvals"`
	OnChangesRequested *string                      `json:"on_changes_requested"`
	ChangesKickBackTo  *types.Stage                 `json:"changes_kick_back_to"`
}

func (s *Server) handleUpdateStage(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	boardID, ok := s.boardParam(w, r)
	if !ok {
		return
	}
	id := types.Stage(r.PathValue("id"))
	var p StagePatch
	if !decodeBody(w, r, &p) {
		return
	}
	board := s.boards.Board(boardID).List()
	idx, found := lookupStage(board, id)
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.stages.errors.stage_not_on_board", "stage", id, "board", boardID)
		return
	}
	// Clear the previous initial in the same write: a two-step edit would fail validation.
	if p.Initial != nil && *p.Initial {
		board = clearInitial(board)
	}
	applyStagePatch(&board[idx], p)
	if err := s.stageAdmin.SaveBoard(r.Context(), boardID, board); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.emitStagesChanged(r, audit.KindStageUpdated, boardID, string(id),
		i18n.Text("api.stages.audit.stage_updated", "stage", id, "board", boardID))
	writeJSON(w, http.StatusOK, s.boardStages(r, boardID))
}

type ReorderStagesRequest struct {
	IDs []types.Stage `json:"ids"`
}

func (s *Server) handleReorderStages(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	boardID, ok := s.boardParam(w, r)
	if !ok {
		return
	}
	var req ReorderStagesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	board := s.boards.Board(boardID).List()
	reordered, err := reorderBoard(board, req.IDs)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.stageAdmin.SaveBoard(r.Context(), boardID, reordered); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.emitStagesChanged(r, audit.KindStageReordered, boardID, "",
		i18n.Text("api.stages.audit.stages_reordered", "board", boardID))
	writeJSON(w, http.StatusOK, s.boardStages(r, boardID))
}

func (s *Server) handleDeleteStage(w http.ResponseWriter, r *http.Request) {
	if !s.stageAdminOK(w, r) {
		return
	}
	boardID, ok := s.boardParam(w, r)
	if !ok {
		return
	}
	id := types.Stage(r.PathValue("id"))
	board := s.boards.Board(boardID).List()
	idx, found := lookupStage(board, id)
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.stages.errors.stage_not_on_board", "stage", id, "board", boardID)
		return
	}
	if board[idx].Initial {
		writeErrKey(w, r, http.StatusConflict, "api.stages.errors.cannot_delete_initial")
		return
	}

	inStage, err := s.taskStore.ListInBoardStage(r.Context(), boardID, string(id))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	moveTo := types.Stage(r.URL.Query().Get("move_to"))
	if len(inStage) > 0 {
		if moveTo == "" {
			writeJSON(w, http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
				"task_count": len(inStage),
			}, "api.stages.errors.stage_holds_tasks", "stage", id, "count", len(inStage)))
			return
		}
		if moveTo == id || !s.boards.Board(boardID).Valid(moveTo) || isVirtual(moveTo) {
			writeErrKey(w, r, http.StatusBadRequest, "api.stages.errors.invalid_destination_stage", "dest", moveTo)
			return
		}
	}

	remaining := append(append([]types.StageInfo{}, board[:idx]...), board[idx+1:]...)
	// Validate before moving any task, so a refusal leaves no task moved.
	if err := stagepkg.ValidateBoard(remaining); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}

	// Tasks move before the board write: a task in a stage that no longer exists is inert.
	moved := s.relocateTasks(r.Context(), inStage, id, moveTo)
	if err := s.stageAdmin.SaveBoard(r.Context(), boardID, remaining); err != nil {
		writeError(w, r, http.StatusConflict,
			apperr.Wrap(err, "api.boards.errors.delete_after_move_failed", "count", moved, "dest", moveTo))
		return
	}
	s.dropStagePriority(r.Context(), id)
	s.emitStagesChanged(r, audit.KindStageDeleted, boardID, string(id),
		i18n.Text("api.stages.audit.stage_deleted", "stage", id, "board", boardID, "count", moved, "dest", moveTo))
	writeJSON(w, http.StatusOK, map[string]any{
		"stages":      s.boardStages(r, boardID),
		"moved_tasks": moved,
		"moved_to":    string(moveTo),
	})
}

// Deliberately no dispatch: deleting a column must not launch containers.
func (s *Server) relocateTasks(ctx context.Context, tasks []task.Task, from, to types.Stage) int {
	moved := 0
	idle := task.StatusIdle
	for i := range tasks {
		t := &tasks[i]
		// Must run before the move: afterwards the task is no longer in from.
		s.recordSkippedApprovals(ctx, t, from, to, task.GateStageDeleted, "octopus")
		if _, err := s.taskStore.Update(ctx, t.ID, task.Patch{Stage: &to, Status: &idle}); err != nil {
			log.Printf("[api] %s", i18n.Log("api.stages.log.move_task_failed", "task", t.ID, "err", err))
			continue
		}
		s.recordEvent(ctx, t.ID, task.EventStageEntered, "", "octopus",
			i18n.Text("api.stages.timeline.moved_stage_deleted", "from", from, "to", to),
			map[string]string{"from": string(from), "to": string(to), "reason": "stage_deleted"})
		webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventTaskStageChanged,
			map[string]string{"task_id": t.ID, "from": string(from), "to": string(to)})
		moved++
	}
	return moved
}

func (s *Server) addStagePriorities(ctx context.Context, board []types.StageInfo) {
	if s.settingsStore == nil {
		return
	}
	if _, err := s.settingsStore.AddStagePriorities(ctx, board); err != nil {
		log.Printf("[api] %s", i18n.Log("api.stages.log.add_priority_failed", "err", err))
	}
}

// Priority weights are catalog-scoped: only drop one when no other board still uses the stage.
func (s *Server) dropStagePriority(ctx context.Context, id types.Stage) {
	if s.settingsStore == nil || s.catalogHas(id) {
		return
	}
	prios, err := s.settingsStore.SandboxPriorities(ctx)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.stages.log.read_priorities_failed", "err", err))
		return
	}
	if _, ok := prios[string(id)]; !ok {
		return
	}
	delete(prios, string(id))
	if err := s.settingsStore.PutJSON(ctx, settings.SandboxPrioritiesKey, prios); err != nil {
		log.Printf("[api] %s", i18n.Log("api.stages.log.drop_priority_failed", "stage", id, "err", err))
	}
}

func lookupStage(board []types.StageInfo, id types.Stage) (int, bool) {
	for i := range board {
		if board[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

func defaultInsertIndex(board []types.StageInfo) int {
	for i := range board {
		if board[i].Terminal {
			return i
		}
	}
	return len(board)
}

func clearInitial(board []types.StageInfo) []types.StageInfo {
	for i := range board {
		board[i].Initial = false
	}
	return board
}

func applyStagePatch(st *types.StageInfo, p StagePatch) {
	setIf := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	patch.Set(&st.Label, p.Label)
	patch.Set(&st.Color, p.Color)
	patch.Set(&st.WipGroup, p.WipGroup)
	setIf(&st.AutoAdvance, p.AutoAdvance)
	setIf(&st.BlockedByFlags, p.BlockedByFlags)
	setIf(&st.Initial, p.Initial)
	setIf(&st.Terminal, p.Terminal)
	setIf(&st.IngestFlags, p.IngestFlags)
	setIf(&st.PushBranch, p.PushBranch)
	setIf(&st.OpenPR, p.OpenPR)
	patch.Set(&st.RequiredApprovals, p.RequiredApprovals)
	patch.Set(&st.OnChangesRequested, p.OnChangesRequested)
	patch.Set(&st.ChangesKickBackTo, p.ChangesKickBackTo)
}

func reorderBoard(board []types.StageInfo, ids []types.Stage) ([]types.StageInfo, error) {
	if len(ids) != len(board) {
		return nil, apperr.Invalid("api.stages.errors.order_incomplete", "want", len(board), "got", len(ids))
	}
	out := make([]types.StageInfo, 0, len(board))
	seen := make(map[types.Stage]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, apperr.Invalid("api.stages.errors.order_duplicate", "stage", id)
		}
		seen[id] = true
		idx, ok := lookupStage(board, id)
		if !ok {
			return nil, apperr.Invalid("api.stages.errors.order_unknown", "stage", id)
		}
		out = append(out, board[idx])
	}
	return out, nil
}

func isVirtual(id types.Stage) bool {
	for _, v := range types.VirtualStages {
		if v == id {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
