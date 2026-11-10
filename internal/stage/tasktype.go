package stage

import (
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A type only removes stages, never changes their flags, so terminality stays per board: the SQL
// predicate in
// internal/task/deps.go must never need to know about types.

const (
	MaxBoardTypes = 16
)

type TaskTypeInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Injected into markup as <use href="#i-tt-<icon>">; must stay validated against TaskTypeIcons.
	Icon        string `json:"icon,omitempty"`
	Color       string `json:"color,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Position    int    `json:"position"`
	Default     bool   `json:"default,omitempty"`
	// Stored as skips, not an enabled list, so a stage added to the board later joins every type.
	Skips     []types.Stage `json:"skips"`
	TaskCount int           `json:"task_count,omitempty"`
}

func ValidTaskTypeID(s string) bool { return boardIDRe.MatchString(s) }

func ValidateTaskTypeInfo(t TaskTypeInfo) error {
	if !ValidTaskTypeID(t.ID) {
		return apperr.Invalid("stage.errors.invalid_task_type_id", "id", t.ID)
	}
	if t.Label == "" {
		return apperr.Invalid("stage.errors.task_type_label_required", "id", t.ID)
	}
	if len(t.Label) > maxLabelLen {
		return apperr.Invalid("stage.errors.task_type_label_too_long", "id", t.ID, "max", maxLabelLen)
	}
	if t.Color != "" && !colorRe.MatchString(t.Color) {
		return apperr.Invalid("stage.errors.task_type_color_invalid", "id", t.ID, "color", t.Color)
	}
	if t.Icon != "" && !ValidTaskTypeIcon(t.Icon) {
		return apperr.Invalid("stage.errors.task_type_icon_invalid", "id", t.ID, "icon", t.Icon)
	}
	return nil
}

func ValidateBoardTypes(board []types.StageInfo, tt []TaskTypeInfo) error {
	if len(tt) > MaxBoardTypes {
		return apperr.Invalid("stage.errors.too_many_task_types", "count", len(tt), "max", MaxBoardTypes)
	}
	onBoard := make(map[types.Stage]types.StageInfo, len(board))
	for i := range board {
		onBoard[board[i].ID] = board[i]
	}
	seen := make(map[string]bool, len(tt))
	defaults := 0
	for i := range tt {
		t := &tt[i]
		if seen[t.ID] {
			return apperr.Invalid("stage.errors.duplicate_task_type", "id", t.ID)
		}
		seen[t.ID] = true
		if err := ValidateTaskTypeInfo(*t); err != nil {
			return err
		}
		if t.Default {
			defaults++
		}
		if err := validateTypeSkips(onBoard, *t); err != nil {
			return err
		}
	}
	if defaults > 1 {
		return apperr.Invalid("stage.errors.multiple_default_task_types", "count", defaults)
	}
	return ValidateApprovalTypes(board, tt)
}

func validateTypeSkips(onBoard map[types.Stage]types.StageInfo, t TaskTypeInfo) error {
	skipped := make(map[types.Stage]bool, len(t.Skips))
	for _, sk := range t.Skips {
		if skipped[sk] {
			return apperr.Invalid("stage.errors.skip_duplicate", "id", t.ID, "stage", sk)
		}
		skipped[sk] = true
		st, ok := onBoard[sk]
		if !ok {
			return apperr.Invalid("stage.errors.skip_not_on_board", "id", t.ID, "stage", sk)
		}
		if st.Initial {
			return apperr.Invalid("stage.errors.skip_initial", "id", t.ID, "stage", sk)
		}
		if st.Terminal {
			return apperr.Invalid("stage.errors.skip_terminal", "id", t.ID, "stage", sk)
		}
	}
	return nil
}

func FilterStages(board []types.StageInfo, skips []types.Stage) []types.StageInfo {
	if len(skips) == 0 {
		out := make([]types.StageInfo, len(board))
		copy(out, board)
		return out
	}
	skipped := make(map[types.Stage]bool, len(skips))
	for _, sk := range skips {
		skipped[sk] = true
	}
	out := make([]types.StageInfo, 0, len(board))
	for i := range board {
		if !skipped[board[i].ID] {
			out = append(out, board[i])
		}
	}
	return out
}

func DefaultTaskType(tt []TaskTypeInfo) string {
	for i := range tt {
		if tt[i].Default {
			return tt[i].ID
		}
	}
	return ""
}
