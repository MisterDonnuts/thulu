package stage

import (
	"regexp"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	MaxBoardStages = 32
	maxLabelLen    = 64
)

// archived and queued are pseudo-states the frontend treats as stage values.
var reservedIDs = map[types.Stage]string{
	types.StageMention: "stage.errors.id_reserved_virtual",
	types.StageChat:    "stage.errors.id_reserved_virtual",
	"archived":         "stage.errors.id_reserved_pseudo",
	"queued":           "stage.errors.id_reserved_pseudo",
}

// The value is injected into styles by the frontend; it must never carry anything but a hex color.
var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

func ValidateBoard(board []types.StageInfo) error {
	if len(board) == 0 {
		return apperr.Invalid("stage.errors.board_empty")
	}
	if len(board) > MaxBoardStages {
		return apperr.Invalid("stage.errors.too_many_stages", "count", len(board), "max", MaxBoardStages)
	}
	onBoard := make(map[types.Stage]types.StageInfo, len(board))
	for i := range board {
		onBoard[board[i].ID] = board[i]
	}
	seen := make(map[types.Stage]bool, len(board))
	initials, terminals := 0, 0
	for i := range board {
		st := &board[i]
		if seen[st.ID] {
			return apperr.Invalid("stage.errors.duplicate_stage", "id", st.ID)
		}
		seen[st.ID] = true
		if err := validateIdentity(*st); err != nil {
			return err
		}
		if err := validateFlags(*st); err != nil {
			return err
		}
		if err := validateApprovals(*st, onBoard); err != nil {
			return err
		}
		if st.Initial {
			initials++
		}
		if st.Terminal {
			terminals++
		}
	}
	if initials != 1 {
		return apperr.Invalid("stage.errors.initial_count", "count", initials)
	}
	if terminals == 0 {
		return apperr.Invalid("stage.errors.terminal_missing")
	}
	return nil
}

func validateIdentity(st types.StageInfo) error {
	if !types.ValidStageID(string(st.ID)) {
		return apperr.Invalid("stage.errors.invalid_stage_id", "id", st.ID)
	}
	if key, reserved := reservedIDs[st.ID]; reserved {
		return apperr.Invalid(key, "id", st.ID)
	}
	if st.Label == "" {
		return apperr.Invalid("stage.errors.stage_label_required", "id", st.ID)
	}
	if len(st.Label) > maxLabelLen {
		return apperr.Invalid("stage.errors.stage_label_too_long", "id", st.ID, "max", maxLabelLen)
	}
	if st.Color != "" && !colorRe.MatchString(st.Color) {
		return apperr.Invalid("stage.errors.stage_color_invalid", "id", st.ID, "color", st.Color)
	}
	return nil
}

func validateFlags(st types.StageInfo) error {
	if st.Virtual {
		return apperr.Invalid("stage.errors.stage_virtual", "id", st.ID)
	}
	switch st.WipGroup {
	case "", types.WipGroupWork, types.WipGroupReview:
	default:
		return apperr.Invalid("stage.errors.wip_group_invalid",
			"id", st.ID, "group", st.WipGroup, "work", types.WipGroupWork, "review", types.WipGroupReview)
	}
	if st.Initial && st.Terminal {
		return apperr.Invalid("stage.errors.initial_and_terminal", "id", st.ID)
	}
	if st.Terminal && st.AutoAdvance {
		return apperr.Invalid("stage.errors.terminal_auto_advance", "id", st.ID)
	}
	// No gated + auto_advance check on purpose: the run parks the task in awaiting_approval and the
	// last approval moves it.
	return nil
}
