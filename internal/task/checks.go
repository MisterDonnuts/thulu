package task

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A stage approval marked required_checks lets the task leave its stage only while the checks
// GitHub requires on the task's pull requests are green. A person may approve it while those
// checks still run; the stage-exit gate then holds every forward move that is not administrative
// until they pass, and the webhook that reports them finished re-evaluates a task parked in
// awaiting_approval. Reading GitHub belongs to the API layer, which installs the reader with
// WithChecksGate; without one, nothing holds.

// ChecksHold is one pull request whose required checks are not green.
type ChecksHold struct {
	ProjectID string `json:"project_id"`
	URL       string `json:"url"`
	Number    int    `json:"number"`
	github.RequiredStatus
}

// Blocks says whether approving has to wait: a check failed, the branch is behind a base that asks
// to be up to date, or the state could not be read. Checks that only run, or have not reported yet,
// leave room to approve ahead of them.
func (h *ChecksHold) Blocks() bool {
	return h.State != github.RequiredPending || h.Behind || len(h.Failing) > 0
}

// AnyBlocks is true when one of holds Blocks.
func AnyBlocks(holds []ChecksHold) bool {
	return slices.ContainsFunc(holds, func(h ChecksHold) bool { return h.Blocks() })
}

// ChecksReader lists the pull requests of t whose required checks hold; nil lets through the
// approvals that wait for them.
type ChecksReader func(ctx context.Context, t *Task) []ChecksHold

// WithChecksGate sets the reader the stage-exit gate asks. Set at boot, before the store is used
// concurrently; there is no lock.
func (s *Store) WithChecksGate(fn ChecksReader) *Store {
	s.readChecks = fn
	return s
}

// WaitsForChecks says whether a is its stage's required approval and that requirement waits for
// the pull request's required checks.
func WaitsForChecks(reg stagepkg.Registry, a *Approval) bool {
	if a.Source != ApprovalFromStage || a.Stage == "" {
		return false
	}
	info, ok := reg.Get(a.Stage)
	return ok && slices.ContainsFunc(info.RequiredApprovals, func(r types.ApprovalRequirement) bool {
		return r.Key == a.Key && r.RequiredChecks
	})
}

// MergeWaitsForChecks says whether t's flow asks for green required checks before its code merges:
// its current stage, or a stage between it and target (the merge_target stage; empty means every
// later stage), has an approval that waits for them. An approval an admin waived holds nothing.
func MergeWaitsForChecks(reg stagepkg.Registry, t *Task, list []Approval, target Stage) bool {
	cur, ok := reg.Get(t.Stage)
	if !ok {
		return false
	}
	tgt, hasTarget := reg.Get(target)
	waived := map[string]bool{}
	for i := range list {
		if list[i].Status == ApprovalWaived && list[i].Source == ApprovalFromStage {
			waived[string(list[i].Stage)+"/"+list[i].Key] = true
		}
	}
	stages := reg.List()
	for i := range stages {
		st := &stages[i]
		if st.Position < cur.Position || hasTarget && st.Position >= tgt.Position {
			continue
		}
		for _, req := range st.RequiredApprovals {
			if req.RequiredChecks && requirementApplies(req, t.TypeID) && !waived[string(st.ID)+"/"+req.Key] {
				return true
			}
		}
	}
	return false
}

// checksHeld lists the approved approvals a move out of from would carry the task past that wait
// for the required checks, and the pull requests holding them. A waived one passes: an admin
// already decided to go ahead without the checks.
func (s *Store) checksHeld(ctx context.Context, t *Task, reg stagepkg.Registry, from types.StageInfo,
	list []Approval, skipped map[Stage]bool,
) ([]Approval, []ChecksHold) {
	if s.readChecks == nil {
		return nil, nil
	}
	var waiting []Approval
	for i := range list {
		a := &list[i]
		if a.Status != ApprovalApproved || (a.Stage != from.ID && !skipped[a.Stage]) || !WaitsForChecks(reg, a) {
			continue
		}
		waiting = append(waiting, *a)
	}
	if len(waiting) == 0 {
		return nil, nil
	}
	holds := s.readChecks(ctx, t)
	if len(holds) == 0 {
		return nil, nil
	}
	return waiting, holds
}

// ChecksText says, per pull request and in ctx's locale, what keeps its merge waiting.
func ChecksText(ctx context.Context, holds []ChecksHold) string {
	out := make([]string, 0, len(holds))
	for i := range holds {
		out = append(out, i18n.TCtx(ctx, "api.approvals.checks.pr", "number", holds[i].Number,
			"reason", ChecksReason(ctx, &holds[i].RequiredStatus)))
	}
	return strings.Join(out, " · ")
}

// ChecksReason says what keeps one pull request's merge waiting.
func ChecksReason(ctx context.Context, st *github.RequiredStatus) string {
	if st.State == github.RequiredUnknown {
		return i18n.TCtx(ctx, "api.approvals.checks.unknown")
	}
	var parts []string
	if len(st.Failing) > 0 {
		parts = append(parts, i18n.TCtx(ctx, "api.approvals.checks.failing", "checks", strings.Join(st.Failing, ", ")))
	}
	var running []string
	for _, c := range st.Pending {
		if !slices.Contains(st.Missing, c) {
			running = append(running, c)
		}
	}
	if len(running) > 0 {
		parts = append(parts, i18n.TCtx(ctx, "api.approvals.checks.running", "checks", strings.Join(running, ", ")))
	}
	if len(st.Missing) > 0 {
		parts = append(parts, i18n.TCtx(ctx, "api.approvals.checks.missing", "checks", strings.Join(st.Missing, ", ")))
	}
	if st.Behind {
		parts = append(parts, i18n.TCtx(ctx, "api.approvals.checks.behind"))
	}
	return strings.Join(parts, "; ")
}

// ReopenApprovedBefore sends back to pending the approvals of taskID among ids that are approved
// and were decided before at. The condition sits in the UPDATE, so a webhook delivered twice, or
// one that arrives after a person approved again, reopens nothing.
func (s *Store) ReopenApprovedBefore(ctx context.Context, taskID string, ids []string, at time.Time) ([]Approval, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{taskID, at.UTC()}
	for _, id := range ids {
		args = append(args, id)
	}
	where := `task_id = ? AND status = 'approved' AND decided_at < ? AND id IN (` + placeholderList(len(ids)) + `)` //i18n:ignore SQL
	out, err := s.reopenWhere(ctx, where, args...)
	if len(out) > 0 {
		s.notifyTask(ctx, taskID)
	}
	return out, err
}
