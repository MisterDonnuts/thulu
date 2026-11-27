package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A merged PR moves its task to the board's merge_target stage and leaves on the task the commit
// the merge created on the default branch (tasks.merge_shas), which is what a deploy is later
// checked against. Four paths reach onPRMerged: the pull_request webhook, a merge of an uploaded
// project, the held pr_merge, and the reconciler for installs without webhooks.

type prMerge struct {
	ProjectID string
	PRURL     string
	// The commit the merge left on the base branch (GitHub's merge_commit_sha).
	SHA  string
	Base string
	// The PR's head branch, when the source carries it; a merge of an uploaded project has none.
	Head string
	// "github:ana", or the label of the person who merged from Thulu.
	By string
	// webhook | hosted | held | reconcile; kept on the timeline event.
	Source string
	// The caller already wrote the merge on the timeline (handleTaskMerge does).
	Recorded bool
}

const (
	mergeSourceWebhook   = "webhook"
	mergeSourceHosted    = "hosted"
	mergeSourceHeld      = "held"
	mergeSourceReconcile = "reconcile"
)

type mergeOutcome string

const (
	// The task moved to the merge_target stage.
	mergeMoved mergeOutcome = "moved"
	// The SHA is saved and nothing moves: the board has no merge_target, or the task is already
	// there or past it.
	mergeRecorded mergeOutcome = "recorded"
	// Another of the task's repositories has not merged yet.
	mergeWaiting mergeOutcome = "waiting"
	// The task is in a stage before any that opens a PR: someone merged ahead of the flow.
	mergeOutOfOrder mergeOutcome = "out_of_order"
	// A run is working on the task; the reconciler moves it once the run ends.
	mergeDeferred mergeOutcome = "deferred"
	// Merged into another branch than the project's default: not on main, so not a release.
	mergeOtherBranch mergeOutcome = "other_branch"
	// The PR's head is not the task's branch: the merge carried someone else's code.
	mergeOtherHead mergeOutcome = "other_head"
	mergeFailed    mergeOutcome = "failed"
)

func (s *Server) onPRMerged(ctx context.Context, t *task.Task, m prMerge) mergeOutcome {
	if t == nil || m.SHA == "" || m.ProjectID == "" {
		return mergeFailed
	}
	if def := s.projectDefaultBranch(ctx, m.ProjectID); def != "" && m.Base != "" && m.Base != def {
		// The reconciler would say it again every round.
		if m.Source != mergeSourceReconcile {
			s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.other_branch",
				"url", m.PRURL, "base", m.Base, "default", def))
		}
		return mergeOtherBranch
	}
	// A run can record any PR of its repository on its task (RecordAgentPR), an old merged one
	// included; taking that merge would move the task past review without its code merging.
	if m.Head != "" && !taskOwnsBranch(t, m.Head) {
		if m.Source != mergeSourceReconcile {
			s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.other_head",
				"url", m.PRURL, "head", m.Head, "branch", project.TaskBranchName(t.ID)))
		}
		return mergeOtherHead
	}
	updated, changed, err := s.taskStore.SetMergeSHA(ctx, t.ID, m.ProjectID, m.SHA)
	if err != nil {
		logx.Warn(err, "api.merges.log.save_sha", "task", t.ID)
		return mergeFailed
	}
	if changed && !m.Recorded {
		s.recordPRMerge(ctx, updated, m)
	}
	out, stage := s.advanceMerged(ctx, updated, m.By)
	// Comment only the first time this SHA is seen: the webhook, the reconciler and a held merge
	// may all report the same merge.
	if changed {
		s.commentMergeOutcome(ctx, updated, m, out, stage)
	}
	return out
}

func (s *Server) recordPRMerge(ctx context.Context, t *task.Task, m prMerge) {
	payload := map[string]string{
		"project": m.ProjectID, "pr_url": m.PRURL, "merge_sha": m.SHA, "base": m.Base, "by": m.By, "source": m.Source,
	}
	summary := i18n.Text("api.merges.events.merged", "url", m.PRURL, "sha", shortSHA(m.SHA), "by", m.By)
	logx.Warn(s.taskStore.RecordEvent(ctx, t.ID, task.EventBranchMerged, m.ProjectID, m.By, summary, payload),
		"api.merges.log.record", "task", t.ID)
}

func (s *Server) commentMergeOutcome(ctx context.Context, t *task.Task, m prMerge, out mergeOutcome, stage task.Stage) {
	sha := shortSHA(m.SHA)
	switch out {
	case mergeMoved:
		s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.moved",
			"url", m.PRURL, "by", m.By, "sha", sha, "stage", s.taskDispatcher.StageNote(t, stage)))
	case mergeWaiting:
		s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.waiting",
			"url", m.PRURL, "sha", sha, "projects", strings.Join(s.unmergedProjectNames(ctx, t), ", ")))
	case mergeOutOfOrder:
		s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.out_of_order",
			"url", m.PRURL, "sha", sha, "stage", s.taskDispatcher.StageNote(t, t.Stage)))
	case mergeDeferred:
		s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.deferred",
			"url", m.PRURL, "sha", sha, "stage", s.taskDispatcher.StageNote(t, stage)))
	case mergeRecorded, mergeOtherBranch, mergeOtherHead, mergeFailed:
	}
}

func (s *Server) mergeNote(ctx context.Context, taskID string, n task.Note) {
	if _, err := s.taskStore.AddNote(ctx, taskID, "", task.SourceSystem, "octopus", n); err != nil {
		logx.Warn(err, "api.merges.log.comment", "task", taskID)
	}
}

// advanceMerged moves t to its board's merge_target stage once every repository it changed has
// merged. Idempotent: a task already there, or past it, stays.
func (s *Server) advanceMerged(ctx context.Context, t *task.Task, by string) (mergeOutcome, task.Stage) {
	reg := s.stagesFor(t)
	target, ok := mergeTargetOf(reg)
	if !ok || t.ArchivedAt != nil {
		return mergeRecorded, ""
	}
	if len(s.unmergedProjects(ctx, t)) > 0 {
		return mergeWaiting, target
	}
	cur, known := reg.Get(t.Stage)
	tgt, _ := reg.Get(target)
	if !known || cur.Terminal || cur.Position >= tgt.Position {
		return mergeRecorded, target
	}
	if !afterPROpening(reg, cur) {
		return mergeOutOfOrder, target
	}
	if t.Status == task.StatusRunning || s.taskDispatcher != nil && s.taskDispatcher.RunInFlight(t.ID) {
		return mergeDeferred, target
	}
	// Fenced: a launch can start between the read above and the move, and the task must not leave
	// for the next stage while that stage's agent starts on it.
	_, v, err := s.moveTaskStage(ctx, t, stageMove{
		To: target, Cause: task.GateMerge, Via: task.ViaMerge, Actor: by, Fenced: true,
		Extra:     map[string]any{"merge_shas": t.MergeSHAs},
		WaiveNote: i18n.Text("api.merges.waived_note", "by", by),
	})
	switch {
	case errors.Is(err, task.ErrRunInFlight):
		return mergeDeferred, target
	case task.IsStageChanged(err):
		// Someone else moved it between the read and the write; the next pass looks again.
		return mergeRecorded, target
	case err != nil:
		logx.Warn(err, "api.merges.log.move", "task", t.ID)
		return mergeFailed, target
	case !v.Passed:
		// GateMerge is administrative; a closed gate here means the approvals could not be read.
		return mergeFailed, target
	}
	return mergeMoved, target
}

// The branch the daemon opens the task's PR from, or the one its worktrees were created on.
func taskOwnsBranch(t *task.Task, head string) bool {
	return head == project.TaskBranchName(t.ID) || t.BranchName != "" && head == t.BranchName
}

func mergeTargetOf(reg stagepkg.Registry) (task.Stage, bool) {
	list := reg.List()
	for i := range list {
		if list[i].MergeTarget {
			return list[i].ID, true
		}
	}
	return "", false
}

// A merge before any stage that opens the PR is a person merging ahead of the flow: the task has not
// been verified, so it is recorded and left where it is.
func afterPROpening(reg stagepkg.Registry, cur types.StageInfo) bool {
	list := reg.List()
	for i := range list {
		if (list[i].OpenPR || list[i].PRDraft) && list[i].Position <= cur.Position {
			return true
		}
	}
	return false
}

// The repositories that must merge before the task moves: every one with a PR, and every uploaded
// project the task changed (those merge through Thulu, without a PR).
func (s *Server) unmergedProjects(ctx context.Context, t *task.Task) []string {
	var missing []string
	need := func(pid string) {
		if t.MergeSHAs[pid] == "" && !slices.Contains(missing, pid) {
			missing = append(missing, pid)
		}
	}
	for pid, url := range t.PRURLs {
		if url != "" {
			need(pid)
		}
	}
	for _, pid := range t.ProjectIDs {
		if t.PRURLs[pid] == "" && t.MergeSHAs[pid] == "" && s.isUploadProject(ctx, pid) && s.uploadChanged(ctx, t, pid) {
			need(pid)
		}
	}
	slices.Sort(missing)
	return missing
}

// Whether the task left anything to merge in an uploaded project: a result the agent pushed and the
// daemon has not integrated yet, commits its branch has and the default branch lacks, or edits not
// committed yet (the merge integrates and commits them first). With none, a merge from Thulu finds
// nothing and the task would wait for it forever. A task with neither a worktree nor a branch there
// holds nothing; when git cannot answer, the project counts as changed.
func (s *Server) uploadChanged(ctx context.Context, t *task.Task, pid string) bool {
	if _, err := s.projectStore.BranchCommit(ctx, pid, project.ResultBranchName(t.SandboxRootID())); err == nil {
		return true
	}
	wt, err := s.worktreeGitForProject(ctx, t, pid)
	if err != nil {
		_, berr := s.projectStore.BranchCommit(ctx, pid, project.TaskBranchName(worktreeOwner(s.kube != nil, t)))
		return apperr.KindOf(berr) != apperr.KindNotFound
	}
	if dirty, dirtyErr := wt.HasChanges(); dirtyErr != nil || dirty {
		return true
	}
	ahead, err := wt.HasCommitsAhead()
	return err != nil || ahead
}

func (s *Server) unmergedProjectNames(ctx context.Context, t *task.Task) []string {
	ids := s.unmergedProjects(ctx, t)
	for i, id := range ids {
		if p, err := s.projectStore.Get(ctx, id); err == nil && p.Name != "" {
			ids[i] = p.Name
		}
	}
	return ids
}

func (s *Server) isUploadProject(ctx context.Context, projectID string) bool {
	if s.projectStore == nil {
		return false
	}
	p, err := s.projectStore.Get(ctx, projectID)
	return err == nil && p.Hosted()
}

func (s *Server) projectDefaultBranch(ctx context.Context, projectID string) string {
	if s.projectStore == nil {
		return ""
	}
	p, err := s.projectStore.Get(ctx, projectID)
	if err != nil {
		return ""
	}
	return p.DefaultBranch
}

// githubMergeHook handles pull_request closed: merged moves the task, closed unmerged only says so.
// Attribution follows the installation, as for automation events.
func (s *Server) githubMergeHook(ctx context.Context, event string, raw []byte) {
	if event != "pull_request" {
		return
	}
	var ev githubEventPayload
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Action != "closed" || ev.PullRequest.HTMLURL == "" {
		return
	}
	ws := s.githubEventWorkspace(ctx, ev)
	t := s.taskForPR(ctx, ws, ev.PullRequest.HTMLURL)
	if t == nil {
		return
	}
	projectID := prProject(t, ev.PullRequest.HTMLURL)
	if !ev.PullRequest.Merged {
		s.mergeNote(ctx, t.ID, task.NoteOf("api.merges.comments.closed_unmerged",
			"url", ev.PullRequest.HTMLURL, "by", ev.Sender.Login))
		return
	}
	by := ev.PullRequest.MergedBy.Login
	if by == "" {
		by = ev.Sender.Login
	}
	s.onPRMerged(ctx, t, prMerge{
		ProjectID: projectID, PRURL: ev.PullRequest.HTMLURL, SHA: ev.PullRequest.MergeCommitSHA,
		Base: ev.PullRequest.Base.Ref, Head: ev.PullRequest.Head.Ref, By: "github:" + by, Source: mergeSourceWebhook,
	})
}

// prMergedFromAPI applies a PR read from GitHub's API (the held merge and the reconciler).
func (s *Server) prMergedFromAPI(ctx context.Context, t *task.Task, projectID string, pr *ghrest.PullRequest, by, source string) mergeOutcome {
	sha := ghrest.MergeSHAOf(pr)
	if sha == "" {
		return mergeFailed
	}
	if by == "" {
		by = "github:" + pr.MergedByLogin()
	}
	return s.onPRMerged(ctx, t, prMerge{
		ProjectID: projectID, PRURL: pr.HTMLURL, SHA: sha, Base: pr.Base.Ref, Head: pr.Head.Ref, By: by, Source: source,
	})
}

// heldMergeLanded applies a merge a person confirmed from a chat: the PR is read back for its URL and
// base, which the merge answer does not carry.
func (s *Server) heldMergeLanded(r *http.Request, token string, req *ghrest.MergeReq, res *ghrest.MergeResp) {
	ctx := context.WithoutCancel(r.Context())
	proj, err := s.heldGithubProject(r)
	if err != nil {
		return
	}
	pr, err := ghrest.GetPR(ctx, token, req.Owner, req.Repo, req.Number)
	if err != nil {
		logx.Warn(err, "api.merges.log.reconcile_read", "url", req.Owner+"/"+req.Repo)
		return
	}
	if !pr.Merged || pr.MergeCommitSHA == "" {
		// GitHub can answer the read before it shows the merge; the merge answer has the commit.
		pr.Merged, pr.MergeCommitSHA = true, res.SHA
	}
	t := s.taskForPR(ctx, proj.WorkspaceID, pr.HTMLURL)
	if t == nil {
		return
	}
	pid := prProject(t, pr.HTMLURL)
	if pid == "" {
		pid = proj.ID
	}
	s.prMergedFromAPI(ctx, t, pid, pr, authorLabel(r), mergeSourceHeld)
}

func prProject(t *task.Task, url string) string {
	for pid, u := range t.PRURLs {
		if u == url {
			return pid
		}
	}
	return ""
}

// prMergeState remembers when each repository last sent a webhook: the reconciler leaves alone the
// repositories whose webhooks arrive, and polls only the silent ones.
type prMergeState struct {
	mu       sync.Mutex
	lastHook map[string]time.Time
	// PR URLs the reconciler stops reading until the time given: closed without merging, or merged
	// into another branch. Nothing about them will change on its own.
	quiet map[string]time.Time
}

func (p *prMergeState) silence(url string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.quiet == nil {
		p.quiet = map[string]time.Time{}
	}
	p.quiet[url] = until
}

func (p *prMergeState) silenced(url string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.quiet[url]
	if ok && !now.Before(until) {
		delete(p.quiet, url)
		return false
	}
	return ok
}

func (p *prMergeState) sawHook(repo string, at time.Time) {
	if repo == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastHook == nil {
		p.lastHook = map[string]time.Time{}
	}
	p.lastHook[strings.ToLower(repo)] = at
}

func (p *prMergeState) hookedSince(repo string, since time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.lastHook[strings.ToLower(repo)]
	return ok && at.After(since)
}

func (s *Server) noteGithubHook(raw []byte) {
	var ev struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(raw, &ev) == nil {
		s.prMerges.sawHook(ev.Repository.FullName, time.Now())
	}
}
