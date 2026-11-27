package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A stage approval marked required_checks waits for GitHub. Approving it is refused while a pull
// request of the task has a required check (branch protection or rulesets) that failed, the branch
// is behind a base that asks to be up to date, or the requirement cannot be read. Checks that still
// run do not refuse it: the approval is taken, and the stage-exit gate (task.WithChecksGate, wired
// in wireApprovals) holds the task in its stage until they pass. A project without a GitHub
// credential, a PR that is not on GitHub and a base branch that requires nothing hold nothing.
// Waiving past checks that are not green is an admin's call.

// checksHold is one pull request whose required checks hold the merge.
type checksHold = task.ChecksHold

// approvalWaitsForChecks: a in its stage's definition asks for required checks.
func (s *Server) approvalWaitsForChecks(t *task.Task, a *task.Approval) bool {
	return task.WaitsForChecks(s.stagesFor(t), a)
}

// prOwnerTask is the task whose pull requests t answers to: a subtask shares its root's.
func (s *Server) prOwnerTask(ctx context.Context, t *task.Task) *task.Task {
	if len(t.PRURLs) > 0 || t.ParentID == "" {
		return t
	}
	root, err := s.taskStore.GetRow(ctx, t.SandboxRootID())
	if err != nil {
		logx.Warn(err, "api.approvals.log.checks_root", "task", t.ID)
		return t
	}
	return root
}

// requiredChecksHolds reads each pull request of t fresh from GitHub, refreshing the PR panel's
// cache with what it read.
func (s *Server) requiredChecksHolds(ctx context.Context, t *task.Task) []checksHold {
	owner := s.prOwnerTask(ctx, t)
	var out []checksHold
	for _, pid := range prProjectOrder(owner) {
		if h, ok := s.prChecksHold(ctx, owner, pid); ok {
			out = append(out, h)
		}
	}
	return out
}

func (s *Server) prChecksHold(ctx context.Context, t *task.Task, projectID string) (checksHold, bool) {
	prURL := t.PRURLs[projectID]
	owner, repo, number, ok := github.ParsePRURL(prURL)
	if !ok {
		return checksHold{}, false
	}
	if s.projectStore != nil {
		// Never spend one workspace's credential on a PR URL another workspace's task carries.
		if p, err := s.projectStore.Get(ctx, projectID); err == nil && p != nil &&
			p.WorkspaceID != "" && t.WorkspaceID != "" && p.WorkspaceID != t.WorkspaceID {
			return checksHold{}, false
		}
	}
	hold := checksHold{ProjectID: projectID, URL: prURL, Number: number}
	tok, checks, err := s.prStatusToken(ctx, projectID)
	if errors.Is(err, githubapp.ErrNoCredential) {
		return checksHold{}, false
	}
	if err != nil {
		logx.Warn(err, "api.pull_requests.log.token_failed", "project", projectID)
		hold.State = github.RequiredUnknown
		return hold, true
	}
	gen := s.gh.prs.generation()
	st, err := github.FetchPRStatus(ctx, tok.Token, owner, repo, number, checks)
	if err != nil {
		logx.Warn(err, "api.approvals.log.checks_read", "pr", prURL)
		hold.State = github.RequiredUnknown
		return hold, true
	}
	s.gh.prs.put(gen, prURL, owner, repo, number, st)
	if !st.Required.Holds() {
		return checksHold{}, false
	}
	hold.RequiredStatus = st.Required
	return hold, true
}

// checksBlock is why a decision on a checks-gated approval may not go ahead.
type checksBlock struct {
	waive bool
	label string
	holds []checksHold
}

func (b *checksBlock) key() string {
	if b.waive {
		return "api.approvals.errors.required_checks_waive"
	}
	return "api.approvals.errors.required_checks"
}

func (b *checksBlock) status() int {
	if b.waive {
		return http.StatusForbidden
	}
	return http.StatusConflict
}

// body is the refusal with each holding pull request, for a client that shows them.
func (b *checksBlock) body(ctx context.Context) map[string]any {
	return keyedRefusal(ctx, map[string]any{"required_checks": b.holds}, b.key(),
		"approval", b.label, "checks", task.ChecksText(ctx, b.holds))
}

// err is the refusal for the flags alias, which answers with errors only.
func (b *checksBlock) err(ctx context.Context) error {
	reason := task.ChecksText(ctx, b.holds)
	if b.waive {
		return apperr.Forbidden(b.key(), "approval", b.label, "checks", reason)
	}
	return apperr.Conflict(b.key(), "approval", b.label, "checks", reason)
}

// checksDecision says whether deciding a may go ahead. block refuses it: approving waits for checks
// that failed, lag behind the base or cannot be read, and only an admin waives past checks that are
// not green. waiting lists the pull requests whose checks still run when an approval goes ahead of
// them.
func (s *Server) checksDecision(
	ctx context.Context, t *task.Task, a *task.Approval, decision task.ApprovalStatus,
) (block *checksBlock, waiting []checksHold) {
	if decision != task.ApprovalApproved && decision != task.ApprovalWaived {
		return nil, nil
	}
	if !s.approvalWaitsForChecks(t, a) {
		return nil, nil
	}
	waive := decision == task.ApprovalWaived
	if waive && auth.CallerRole(ctx).AtLeast(auth.RoleAdmin) {
		return nil, nil
	}
	holds := s.requiredChecksHolds(ctx, t)
	if len(holds) == 0 {
		return nil, nil
	}
	if !waive && !task.AnyBlocks(holds) {
		return nil, holds
	}
	return &checksBlock{waive: waive, label: a.Label, holds: holds}, nil
}

// decisionChecks looks the approval up for checksDecision; one that is missing or belongs to
// another task passes, for DecideApproval to answer.
func (s *Server) decisionChecks(ctx context.Context, t *task.Task, aid string, decision task.ApprovalStatus) (*checksBlock, []checksHold) {
	a, err := s.taskStore.GetApproval(ctx, aid)
	if err != nil || a.TaskID != t.ID {
		return nil, nil
	}
	return s.checksDecision(ctx, t, a, decision)
}

// mergeChecksHolds lists the pull requests whose required checks hold a merge of t made from Thulu,
// when its flow asks for them before a merge (task.MergeWaitsForChecks). A task that answers to no
// such approval, or has no pull request on GitHub, merges freely.
func (s *Server) mergeChecksHolds(ctx context.Context, t *task.Task) []checksHold {
	reg := s.stagesFor(t)
	target, _ := mergeTargetOf(reg)
	list, err := s.taskStore.ListApprovals(ctx, t.ID)
	if err != nil {
		// Without the list no waiver counts: the checks decide.
		logx.Warn(err, "task.approvals.log.gate", "task", t.ID)
	}
	if !task.MergeWaitsForChecks(reg, t, list, target) {
		return nil
	}
	return s.requiredChecksHolds(ctx, t)
}

// noteApprovedAheadOfChecks tells the task's readers that a was approved while its checks still
// run, and that the task stays in its stage until they pass.
func (s *Server) noteApprovedAheadOfChecks(ctx context.Context, t *task.Task, a *task.Approval, waiting []checksHold) {
	if len(waiting) == 0 || a.Status != task.ApprovalApproved {
		return
	}
	n := task.NoteOf("api.approvals.comments.approved_ahead_of_checks", "approval", a.Label,
		"stage", s.taskDispatcher.StageNote(t, a.Stage), "checks", task.ChecksText(ctx, waiting))
	if _, err := s.taskStore.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus", n); err != nil {
		logx.Warn(err, "api.approvals.log.checks_note", "task", t.ID)
	}
}
