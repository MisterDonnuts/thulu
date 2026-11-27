package stage

import (
	"regexp"
	"slices"
	"sort"

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
	return validateFlow(board)
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
	if err := validateEdgeFlags(st); err != nil {
		return err
	}
	if st.DefaultAgent != "" && !ValidAgentName(st.DefaultAgent) {
		return apperr.Invalid("stage.errors.default_agent_invalid", "id", st.ID, "agent", st.DefaultAgent)
	}
	return validateSkipUnless(st)
}

// Same shape agentstore enforces on names; repeated here because stage may not import it.
var agentNameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]{0,63}$`)

func ValidAgentName(s string) bool { return agentNameRe.MatchString(s) }

const maxSkipUnless = 8

// No agent runs in the initial stage and the terminal one already frees everything, so these flags
// would do nothing there.
func validateEdgeFlags(st types.StageInfo) error {
	if !st.Initial && !st.Terminal {
		return nil
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{st.AgentVerdict, "agent_verdict"},
		{st.LockTests, "lock_tests"},
		{len(st.SkipUnless) > 0, "skip_unless"},
		{st.ReleaseResources, "release_resources"},
		{st.PRDraft, "pr_draft"},
	} {
		if f.on {
			return apperr.Invalid("stage.errors.flag_on_edge_stage", "id", st.ID, "flag", f.name)
		}
	}
	if st.Initial && (st.MergeTarget || st.PRReady) {
		flag := "merge_target"
		if st.PRReady {
			flag = "pr_ready"
		}
		return apperr.Invalid("stage.errors.flag_on_edge_stage", "id", st.ID, "flag", flag)
	}
	return nil
}

// A skipped stage never opens its gates nor publishes its branch, so the flags that do either cannot
// sit on a stage that may be skipped.
func validateSkipUnless(st types.StageInfo) error {
	if len(st.SkipUnless) == 0 {
		return nil
	}
	if len(st.SkipUnless) > maxSkipUnless {
		return apperr.Invalid("stage.errors.skip_unless_too_many", "id", st.ID, "count", len(st.SkipUnless), "max", maxSkipUnless)
	}
	seen := make(map[string]bool, len(st.SkipUnless))
	for _, v := range st.SkipUnless {
		if !types.ValidStageID(v) {
			return apperr.Invalid("stage.errors.skip_unless_invalid", "id", st.ID, "value", v)
		}
		if seen[v] {
			return apperr.Invalid("stage.errors.skip_unless_duplicate", "id", st.ID, "value", v)
		}
		seen[v] = true
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{len(st.RequiredApprovals) > 0, "required_approvals"},
		{st.OpenPR, "open_pr"},
		{st.PRDraft, "pr_draft"},
		{st.PRReady, "pr_ready"},
		{st.PushBranch, "push_branch"},
		{st.LockTests, "lock_tests"},
		{st.ReleaseResources, "release_resources"},
		{st.MergeTarget, "merge_target"},
		{st.NoSkip, "no_skip"},
	} {
		if f.on {
			return apperr.Invalid("stage.errors.skip_unless_combined", "id", st.ID, "flag", f.name)
		}
	}
	return nil
}

// Rules that need the whole board: where a merged PR lands, what may follow a stage that freed the
// worktree, and which stages a kick-back may target.
func validateFlow(board []types.StageInfo) error {
	ordered := make([]types.StageInfo, len(board))
	copy(ordered, board)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })

	byID := make(map[types.Stage]types.StageInfo, len(ordered))
	for i := range ordered {
		byID[ordered[i].ID] = ordered[i]
	}
	var walk flowWalk
	for i := range ordered {
		if err := walk.step(&ordered[i], byID); err != nil {
			return err
		}
	}
	return nil
}

// What validateFlow has seen so far, in board order.
type flowWalk struct {
	opensPR     bool
	released    types.Stage
	mergeTarget types.Stage
}

func (w *flowWalk) step(st *types.StageInfo, byID map[types.Stage]types.StageInfo) error {
	if st.MergeTarget {
		if w.mergeTarget != "" {
			return apperr.Invalid("stage.errors.merge_target_multiple", "id", st.ID, "other", w.mergeTarget)
		}
		w.mergeTarget = st.ID
		if !w.opensPR {
			return apperr.Invalid("stage.errors.merge_target_without_pr", "id", st.ID)
		}
	}
	if st.OpenPR || st.PRDraft {
		w.opensPR = true
	}
	if !w.opensPR && slices.ContainsFunc(st.RequiredApprovals, func(a types.ApprovalRequirement) bool { return a.RequiredChecks }) {
		return apperr.Invalid("stage.errors.required_checks_without_pr", "id", st.ID)
	}
	if w.released != "" && !st.Terminal && (st.PushBranch || st.OpenPR || st.PRDraft || st.LockTests) {
		return apperr.Invalid("stage.errors.release_resources_worktree_after", "id", st.ID, "released", w.released)
	}
	if st.ReleaseResources {
		if st.ChangesKickBackTo != "" || st.OnChangesRequested == types.ChangesKickBack {
			return apperr.Invalid("stage.errors.release_resources_kick_back", "id", st.ID)
		}
		if w.released == "" {
			w.released = st.ID
		}
	}
	if target, ok := byID[st.ChangesKickBackTo]; ok && len(target.SkipUnless) > 0 {
		return apperr.Invalid("stage.errors.skip_unless_kick_back_target", "id", st.ID, "stage", target.ID)
	}
	return nil
}
