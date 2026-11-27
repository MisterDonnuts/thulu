package task

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func (d *Dispatcher) rootTask(ctx context.Context, t *Task) *Task {
	if !t.IsSubtask() {
		return t
	}
	if parent, err := d.store.Get(ctx, t.ParentID); err == nil && parent != nil {
		return parent
	}
	return t
}

// The worktree stays owned by the parent id; subtasks never create or remove one.
func (d *Dispatcher) adoptParentWorktrees(ctx context.Context, t *Task) *Task {
	parent, err := d.store.Get(ctx, t.ParentID)
	if err != nil || parent == nil {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.subtask_parent_not_found"))
		return t
	}
	parent = d.ensureWorktrees(ctx, parent)
	if len(parent.WorktreePaths) == 0 {
		return t
	}
	patch := Patch{WorktreePaths: parent.WorktreePaths}
	if t.BranchName != parent.BranchName {
		b := parent.BranchName
		patch.BranchName = &b
	}
	if updated, err := d.store.Update(ctx, t.ID, patch); err == nil {
		return updated
	}
	t.WorktreePaths = parent.WorktreePaths
	t.BranchName = parent.BranchName
	return t
}

func (d *Dispatcher) ensureWorktrees(ctx context.Context, t *Task) *Task {
	// The hierarchy is one level deep, so the parent is never a subtask and this does not recurse.
	if t.IsSubtask() {
		return d.adoptParentWorktrees(ctx, t)
	}
	if t.WorktreePaths == nil {
		t.WorktreePaths = map[string]string{}
	}
	changed := false
	branch := ""
	for _, projectID := range t.ProjectIDs {
		if _, ok := t.WorktreePaths[projectID]; ok {
			continue
		}
		if d.projectCodeOnRunner(ctx, projectID) {
			if t.BranchName == "" {
				branch = project.TaskBranchName(t.ID)
				changed = true
			}
			continue
		}
		wt, err := d.createWorktree(ctx, projectID, t.ID)
		if err != nil {
			d.systemComment(ctx, t.ID, NoteOf("task.comments.worktree_create_failed", "project", d.projectName(ctx, projectID), "err", err))
			continue
		}
		if wt.StaleBase != nil {
			d.systemComment(ctx, t.ID, NoteOf("task.comments.worktree_stale_base",
				"project", d.projectName(ctx, projectID), "commit", wt.BaseCommit, "err", wt.StaleBase))
		}
		path, br := wt.Path, wt.Branch
		t.WorktreePaths[projectID] = path
		branch = br
		changed = true
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventBranchCreated, br, "octopus",
			i18n.Text("task.comments.worktree_at", "path", path),
			map[string]string{"project": projectID, "branch": br, "path": path}),
			"task.log.record_worktree_branch", "task", t.ID)
	}
	if changed {
		patch := Patch{WorktreePaths: t.WorktreePaths}
		if t.BranchName == "" && branch != "" {
			patch.BranchName = &branch
			t.BranchName = branch
		}
		if updated, err := d.store.Update(ctx, t.ID, patch); err == nil {
			return updated
		}
	}
	return t
}

func (d *Dispatcher) createWorktree(ctx context.Context, projectID, taskID string) (project.TaskWorktree, error) {
	token := d.fetchToken(ctx, projectID)
	if d.projectRunsOnRunner(ctx, projectID) {
		return d.projects.CreateRunnerTaskWorktree(ctx, projectID, taskID, token)
	}
	return d.projects.CreateTaskWorktreeAuth(ctx, projectID, taskID, token)
}

func (d *Dispatcher) fetchToken(ctx context.Context, projectID string) string {
	if d.secrets == nil {
		return ""
	}
	var (
		tok *githubapp.Token
		err error
	)
	if scoped, ok := d.secrets.(interface {
		GetGithubTokenFor(ctx context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error)
	}); ok {
		tok, err = scoped.GetGithubTokenFor(ctx, projectID, githubapp.PermsClone)
	} else {
		tok, err = d.secrets.GetGithubToken(ctx, projectID)
	}
	if err != nil || tok == nil {
		if err != nil && !errors.Is(err, githubapp.ErrNoCredential) {
			logx.Warn(err, "task.log.resolve_clone_credential", "project", projectID)
		}
		return ""
	}
	return tok.Token
}

// Sandbox and worktrees are owned by the root task and shared by its subtasks: tear down only once
// the whole group is Done,
// keyed by the root id, not the task that finished.
// True when it freed them.
func (d *Dispatcher) cleanupDoneTask(ctx context.Context, t *Task) bool {
	return d.freeGroupResources(ctx, t, d.store.AllInGroupDone, false)
}

// A stage with release_resources frees what a terminal stage would, once every task of the group
// has reached one of the two. The branch stays on the remote; the worktree paths are cleared so a
// person moving the task back gets a fresh worktree from that branch. True when it freed them.
func (d *Dispatcher) releaseTaskResources(ctx context.Context, t *Task) bool {
	return d.freeGroupResources(ctx, t, d.store.AllInGroupReleased, true)
}

// Whether a run in this stage works on the task's code. The initial stage never does, and a stage
// with release_resources already gave the worktrees up.
func (d *Dispatcher) needsWorktree(t *Task, stage Stage) bool {
	if t == nil || len(t.ProjectIDs) == 0 {
		return false
	}
	info, _ := d.stagesFor(t).Get(stage)
	return !info.Initial && !info.ReleaseResources
}

func (d *Dispatcher) stageReleased(t *Task, stage Stage) bool {
	info, _ := d.stagesFor(t).Get(stage)
	return info.ReleaseResources
}

func (d *Dispatcher) freeGroupResources(ctx context.Context, t *Task, groupDone func(context.Context, string) (bool, error), forget bool) bool {
	rootID := t.SandboxRootID()
	allDone, err := groupDone(ctx, rootID)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.done_cleanup_group_check", "group", rootID, "err", err))
		return false
	}
	if !allDone {
		return false
	}
	root := t
	if rootID != t.ID {
		if r, rerr := d.store.Get(ctx, rootID); rerr == nil && r != nil {
			root = r
		}
	}
	if d.sandbox != nil {
		if err := d.sandbox.Release(ctx, rootID); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.done_cleanup_release_sandbox", "group", rootID, "err", err))
		}
	}
	for _, pid := range root.ProjectIDs {
		if d.projectCodeOnRunner(ctx, pid) {
			continue
		}
		if err := d.projects.RemoveTaskWorktree(ctx, pid, rootID); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.done_cleanup_remove_worktree", "group", rootID, "project", pid, "err", err))
		}
	}
	logx.Warn(RemoveTaskSessions(d.sessionsDir, rootID, rootID), "task.sessions.log.remove_group", "group", rootID)
	if forget {
		d.forgetGroupWorktrees(ctx, rootID)
	}
	return true
}

func (d *Dispatcher) forgetGroupWorktrees(ctx context.Context, rootID string) {
	children, err := d.store.ListChildren(ctx, rootID)
	logx.Warn(err, "task.log.release_list_children", "task", rootID)
	ids := make([]string, 0, 1+len(children))
	ids = append(ids, rootID)
	for i := range children {
		ids = append(ids, children[i].ID)
	}
	for _, id := range ids {
		_, err := d.store.Update(ctx, id, Patch{WorktreePaths: map[string]string{}})
		logx.Warn(err, "task.log.release_forget_worktrees", "task", id)
	}
}

func (d *Dispatcher) commitAndPushWorktrees(ctx context.Context, t *Task, taskID string, stage Stage, agentName string, exitCode int) {
	// A stage that freed the worktree has nothing to commit, and its run had no code mounted.
	if t == nil || d.stageReleased(t, stage) {
		return
	}
	// Commit and push under the root id the worktree was created with, so the whole group lands on
	// one branch.
	ownerID := t.SandboxRootID()
	d.syncRunnerBranches(ctx, t.ProjectIDs, ownerID, taskID)
	projects := d.localProjects(ctx, t.ProjectIDs)
	// A pod or a machine pushed a result branch; bring it onto the daemon worktree first. A run that
	// pushed none leaves nothing to integrate. A project whose result could not be integrated must
	// not be pushed.
	stale := d.integrateAgentResults(ctx, projects, ownerID, taskID)
	msg := fmt.Sprintf("%s: %s (task %s)", stage, agentName, taskID) //i18n:ignore git commit subject, kept stable across locales
	for _, projectID := range projects {
		if err := d.projects.CommitWorktree(ctx, projectID, ownerID, msg); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.commit_worktree_failed", "project", projectID, "err", err))
		}
	}
	// The PR is deliberately opened by the agent, which has the title, body and sandbox link; the
	// daemon does not.
	if info, _ := d.stagesFor(t).Get(stage); !info.PushBranch || exitCode != 0 {
		return
	}
	owner := d.rootTask(ctx, t)
	for _, projectID := range projects {
		if !stale[projectID] {
			d.pushTaskBranchForProject(ctx, owner, projectID)
		}
	}
}

func (d *Dispatcher) localProjects(ctx context.Context, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !d.projectRunsOnRunner(ctx, id) {
			out = append(out, id)
		}
	}
	return out
}

// Runs after every run, not only after a reported push: the report of a run that ended while the
// daemon was down is lost.
// A remote without the branch costs one ls-remote.
func (d *Dispatcher) syncRunnerBranches(ctx context.Context, ids []string, ownerID, taskID string) {
	for _, id := range ids {
		p := d.runnerProject(ctx, id)
		if p == nil || !p.RunsOnRunner() || p.CodeOnRunner() {
			continue
		}
		if _, err := d.projects.SyncRunnerBranch(ctx, id, ownerID, d.fetchToken(ctx, id)); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.runner_sync_failed", "project", id, "err", err))
			d.systemComment(ctx, taskID, NoteOf("task.comments.runner_sync_failed", "project", p.Name, "err", err))
		}
	}
}

func (d *Dispatcher) integrateAgentResults(ctx context.Context, projects []string, ownerID, taskID string) map[string]bool {
	stale := map[string]bool{}
	ref := resultRefFor(ownerID)
	for _, projectID := range projects {
		if _, err := d.projects.IntegrateAgentResult(ctx, projectID, ownerID, ref); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.integrate_agent_result", "project", projectID, "ref", ref, "err", err))
			d.systemComment(ctx, taskID, NoteOf("task.comments.integrate_failed", "project", projectID, "err", err))
			stale[projectID] = true
			if apperr.KindOf(err) == apperr.KindConflict {
				d.parkAgentResult(ctx, projectID, ref, taskID)
			}
		}
	}
	return stale
}

// A result that drops the task's commits fails the same way on every later run, and a Docker run
// never pushes a new one: set it aside so this run alone skips the push.
func (d *Dispatcher) parkAgentResult(ctx context.Context, projectID, ref, taskID string) {
	parked, err := d.projects.ParkAgentResult(ctx, projectID, ref)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.integrate_agent_result", "project", projectID, "ref", ref, "err", err))
		return
	}
	if parked != "" {
		d.systemComment(ctx, taskID, NoteOf("task.comments.result_parked", "project", projectID, "ref", parked))
	}
}

func (d *Dispatcher) pushTaskBranchForProject(ctx context.Context, t *Task, projectID string) {
	taskID := t.ID
	pushed, err := d.pushBranch(ctx, t, projectID)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.auto_push_failed", "project", projectID, "err", err))
		d.systemComment(ctx, taskID, NoteOf("task.comments.auto_push_failed", "project", projectID, "err", err))
		return
	}
	if !pushed {
		return
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventBranchCreated, project.TaskBranchName(taskID),
		"octopus", i18n.Text("task.comments.branch_pushed"),
		map[string]string{"project": projectID, "branch": project.TaskBranchName(taskID)}),
		"task.log.record_branch_pushed", "task", taskID)
}

// The injected pusher runs git where the commits are (the sandbox pod in K8s) with the project's
// credential.
// The fallback runs host git without a credential and always fails on a K8s daemon.
func (d *Dispatcher) pushBranch(ctx context.Context, t *Task, projectID string) (bool, error) {
	// The runner pushes with its machine's git credentials (OCTOPUS_STAGE_PUSH,
	// runner.JobProject.Push).
	// The daemon's worktree only mirrors that branch; pushing from it (Docker rebases first) would
	// rewrite the runner's push.
	if d.projectRunsOnRunner(ctx, projectID) {
		return false, nil
	}
	if d.branchPusher != nil {
		pushCtx, cancel := context.WithTimeout(ctx, branchPushTimeout)
		defer cancel()
		return d.branchPusher(pushCtx, t, projectID)
	}
	ahead, err := d.projects.BranchHasCommitsAhead(ctx, projectID, t.ID)
	if err != nil || !ahead {
		return false, err
	}
	if _, err := d.projects.PushBranch(ctx, projectID, t.ID); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Dispatcher) projectName(ctx context.Context, projectID string) string {
	if d.projects == nil {
		return projectID
	}
	if p, err := d.projects.Get(ctx, projectID); err == nil && p != nil && p.Name != "" {
		return p.Name
	}
	return projectID
}
