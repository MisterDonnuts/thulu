package stage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// FreeBoardID answers base when no board holds it, then base_<workspace>, then that with _2.._9.
// Board ids are install-wide, so a second workspace installing the same flow needs another id.
func (s *Store) FreeBoardID(base, wid string) (string, error) {
	if !ValidBoardID(base) {
		return "", apperr.Invalid("stage.errors.invalid_board_id", "id", base)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.freeIDLocked(base, wid)
}

func (s *Store) freeIDLocked(base, wid string) (string, error) {
	if !s.boardExistsLocked(base) {
		return base, nil
	}
	stem := base + "_" + slugWorkspace(wid)
	if len(stem) > 30 {
		stem = stem[:30]
	}
	if !s.boardExistsLocked(stem) {
		return stem, nil
	}
	for n := 2; n <= 9; n++ {
		if candidate := fmt.Sprintf("%s_%d", stem, n); !s.boardExistsLocked(candidate) {
			return candidate, nil
		}
	}
	return "", apperr.Conflict("stage.errors.no_free_board_id", "workspace", wid)
}

// InstallFlowBoard creates a board with its stages and task types in one transaction, so a
// refused type list leaves no half-made board behind.
func (s *Store) InstallFlowBoard(ctx context.Context, info BoardInfo, board []types.StageInfo, tt []TaskTypeInfo) error {
	stages, typeList, err := normalizeFlowBoard(info, board, tt)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boardExistsLocked(info.ID) {
		return apperr.Conflict("stage.errors.board_exists", "id", info.ID)
	}
	if n := s.workspaceBoardCountLocked(info.WorkspaceID); n >= MaxBoards {
		return apperr.Invalid("stage.errors.too_many_boards", "count", n, "max", MaxBoards)
	}
	return s.inTxLocked(ctx, func(tx *sql.Tx) error {
		if err := s.insertBoardRowTx(ctx, tx, info); err != nil {
			return err
		}
		if err := s.writeBoardTx(ctx, tx, info.ID, stages); err != nil {
			return err
		}
		return s.writeBoardTypesTx(ctx, tx, info.ID, typeList)
	})
}

// UpdateFlowBoard rewrites a flow board's columns, task types and flow origin. Stages and types
// already on the board keep their label, color and icon: a person renaming a column is not an
// edit the flow should undo. The board's own label and description stay too.
func (s *Store) UpdateFlowBoard(ctx context.Context, info BoardInfo, board []types.StageInfo, tt []TaskTypeInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.boardInfoLocked(info.ID)
	if !ok {
		return apperr.NotFound("stage.errors.unknown_board", "id", info.ID)
	}
	info.Label, info.Description, info.WorkspaceID = cur.Label, cur.Description, cur.WorkspaceID
	merged := keepStageIdentity(board, s.state.board(info.ID).List())
	mergedTypes := keepTypeIdentity(tt, s.state.types[info.ID])
	stages, typeList, err := normalizeFlowBoard(info, merged, mergedTypes)
	if err != nil {
		return err
	}
	name, revision, hash := info.flowColumns()
	return s.inTxLocked(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.rb(
			`UPDATE boards SET max_kick_backs = ?, flow = ?, flow_revision = ?, flow_hash = ? WHERE id = ?`),
			info.MaxKickBacks, name, revision, hash, info.ID,
		); err != nil {
			return err
		}
		if err := s.writeBoardTx(ctx, tx, info.ID, stages); err != nil {
			return err
		}
		return s.writeBoardTypesTx(ctx, tx, info.ID, typeList)
	})
}

func normalizeFlowBoard(info BoardInfo, board []types.StageInfo, tt []TaskTypeInfo) ([]types.StageInfo, []TaskTypeInfo, error) {
	if err := ValidateBoardInfo(info); err != nil {
		return nil, nil, err
	}
	stages := make([]types.StageInfo, len(board))
	copy(stages, board)
	for i := range stages {
		stages[i].Position = (i + 1) * 10
		stages[i].Virtual = false
	}
	if err := ValidateBoard(stages); err != nil {
		return nil, nil, err
	}
	typeList := copyTypes(tt)
	for i := range typeList {
		typeList[i].Position = (i + 1) * 10
		typeList[i].TaskCount = 0
		typeList[i].WorkspaceID = info.WorkspaceID
	}
	if err := ValidateBoardTypes(stages, typeList); err != nil {
		return nil, nil, err
	}
	return stages, typeList, nil
}

func keepStageIdentity(board, current []types.StageInfo) []types.StageInfo {
	have := make(map[types.Stage]types.StageInfo, len(current))
	for i := range current {
		have[current[i].ID] = current[i]
	}
	out := make([]types.StageInfo, len(board))
	copy(out, board)
	for i := range out {
		if cur, ok := have[out[i].ID]; ok {
			out[i].Label, out[i].Color = cur.Label, cur.Color
		}
	}
	return out
}

func keepTypeIdentity(tt, current []TaskTypeInfo) []TaskTypeInfo {
	have := make(map[string]TaskTypeInfo, len(current))
	for i := range current {
		have[current[i].ID] = current[i]
	}
	out := copyTypes(tt)
	for i := range out {
		if cur, ok := have[out[i].ID]; ok {
			out[i].Label, out[i].Icon, out[i].Color = cur.Label, cur.Icon, cur.Color
		}
	}
	return out
}

// Caller holds s.mu. Reloads the read state after a commit.
func (s *Store) inTxLocked(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

// Caller holds s.mu. A workspace's first board must be its default, or its fallbacks land on
// another tenant's flow.
func (s *Store) insertBoardRowTx(ctx context.Context, tx *sql.Tx, info BoardInfo) error {
	isDefault := boolInt(s.workspaceBoardCountLocked(info.WorkspaceID) == 0)
	flowName, flowRevision, flowHash := info.flowColumns()
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO boards (id, label, description, workspace_id, position, is_default,
		    max_kick_backs, flow, flow_revision, flow_hash)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		info.ID, info.Label, info.Description, info.WorkspaceID,
		(len(s.state.boards)+1)*10, isDefault,
		info.MaxKickBacks, flowName, flowRevision, flowHash,
	); err != nil {
		return apperr.Wrap(err, "stage.errors.insert_board", "id", info.ID)
	}
	return nil
}

// BoardAgents splits what a board says about the agent of each stage for a task of typeID: the
// column's default_agent, and the type's own agents (an empty name clears the column's). Stages the
// type skips are left out. Callers layer the project template between the two.
func BoardAgents(b Boards, boardID, typeID string) (columns, byType map[types.Stage]string) {
	columns = map[types.Stage]string{}
	byType = map[types.Stage]string{}
	if b == nil {
		return columns, byType
	}
	stages := b.BoardFor(boardID, typeID).List()
	on := make(map[types.Stage]bool, len(stages))
	for i := range stages {
		on[stages[i].ID] = true
		if stages[i].DefaultAgent != "" {
			columns[stages[i].ID] = stages[i].DefaultAgent
		}
	}
	if typeID == "" {
		return columns, byType
	}
	tt := b.BoardTypes(boardID)
	for i := range tt {
		if tt[i].ID != typeID {
			continue
		}
		for st, agent := range tt[i].Agents {
			if on[st] {
				byType[st] = agent
			}
		}
	}
	return columns, byType
}

// TypeAgentStages lists the stages where at least one task type of the board sets its own agent.
// Only there does a task's type change which agent the column expects.
func TypeAgentStages(b Boards, boardID string) map[types.Stage]bool {
	out := map[types.Stage]bool{}
	if b == nil {
		return out
	}
	tt := b.BoardTypes(boardID)
	for i := range tt {
		for st := range tt[i].Agents {
			out[st] = true
		}
	}
	return out
}
