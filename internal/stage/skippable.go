package stage

import "github.com/MisterDonnuts/thulu/internal/types"

// Why a task may not skip a column (task stage_skips). web/src/lib/stage-skips.ts mirrors this
// table so the drawer locks the same columns the daemon refuses.
const (
	SkipWhyInitial     = "initial"
	SkipWhyTerminal    = "terminal"
	SkipWhyVirtual     = "virtual"
	SkipWhyNoSkip      = "no_skip"
	SkipWhyRefinement  = "refinement"
	SkipWhyApprovals   = "approvals"
	SkipWhyPullRequest = "pull_request"
)

// SkippableReason is "" when a task may skip the column, else why not. Skipping a gate column would
// bypass a person's approval, a pull request column would break the PR and merge lifecycle, and the
// refinement column is where skips are decided. push_branch, lock_tests and agent_verdict stay
// skippable: the next column that runs pushes, and a column that is skipped locks nothing.
func SkippableReason(info types.StageInfo) string {
	switch {
	case info.Initial:
		return SkipWhyInitial
	case info.Terminal:
		return SkipWhyTerminal
	case info.Virtual:
		return SkipWhyVirtual
	case info.NoSkip:
		return SkipWhyNoSkip
	case info.IngestFlags:
		return SkipWhyRefinement
	case len(info.RequiredApprovals) > 0:
		return SkipWhyApprovals
	case info.OpenPR, info.PRDraft, info.PRReady, info.MergeTarget, info.ReleaseResources:
		return SkipWhyPullRequest
	}
	return ""
}
