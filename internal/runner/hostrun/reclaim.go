package hostrun

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// A task's worktree (<WorkDir>/worktrees/<branch>/<repo>) outlives its runs: the next stage picks up
// the same checkout, node_modules and build caches. Nothing removed them, and on one machine they
// reached 73 GB, 70 GB of it Angular's cache. Now the runner asks the daemon which task branches are
// still open and removes the rest, once their work is pushed or saved in the runner's clone, and a
// run trims a build cache that went over build_cache_max_mb.

// DefaultBuildCacheMaxMB is how large a worktree's build cache may grow before a run's end removes it.
const DefaultBuildCacheMaxMB = 4096

// The build caches a run's end measures, relative to the worktree.
var buildCaches = []string{".angular/cache", ".next/cache", "node_modules/.cache"}

func (c *Config) buildCacheMaxMB() int64 { return orDefault(c.BuildCacheMaxMB, DefaultBuildCacheMaxMB) }

// pruneBuildCaches removes the build caches of the run's worktrees that went over the limit.
func (j *job) pruneBuildCaches() {
	limit := j.r.cfg.current().buildCacheMaxMB()
	if limit <= 0 {
		return
	}
	for _, wt := range j.wts {
		for _, rel := range buildCaches {
			dir := filepath.Join(wt, filepath.FromSlash(rel))
			if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
				continue
			}
			size, done := diskSize(dir, sizeWalkLimit)
			if size <= limit<<20 {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				logx.Warn(err, "hostrun.log.remove_failed", "path", dir)
				continue
			}
			log.Printf("[runner] %s", i18n.Log("hostrun.log.build_cache_pruned", "job", j.spec.ID, "path", dir,
				"size", bytesText(size, done), "max", limit))
		}
	}
}

func (c *Config) worktreesDir() string { return filepath.Join(c.WorkDir, "worktrees") }

// sweepWorktrees removes the task worktrees whose branch no open task uses, the entries of the
// worktrees folder that are no task's, and the clones of projects that are no longer this runner's.
// It removes nothing when the daemon does not answer: an empty answer would read as "every task is
// closed".
func (r *Runner) sweepWorktrees(ctx context.Context) {
	if r.cfg.WorkDir == "" {
		return
	}
	open, err := r.client.Worktrees(ctx)
	if err != nil {
		var ce *callError
		switch {
		case asCallError(err, &ce) && ce.Status == http.StatusNotFound:
			if !r.worktreesUnsupported {
				log.Printf("[runner] %s", i18n.Log("hostrun.log.worktrees_unsupported"))
				r.worktreesUnsupported = true
			}
		case ctx.Err() == nil:
			log.Printf("[runner] %s", i18n.Log("hostrun.log.worktrees_list_failed", "err", err))
		}
		return
	}
	keep := map[string]bool{}
	for _, branches := range open.Projects {
		for _, b := range branches {
			keep[branchDir.ReplaceAllString(b, "_")] = true
		}
	}
	busy, anyJob := r.busyBranches()
	entries, err := os.ReadDir(r.cfg.worktreesDir())
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if keep[name] || busy[name] {
				continue
			}
			r.sweepBranchDir(ctx, filepath.Join(r.cfg.worktreesDir(), name), anyJob)
		}
	}
	r.sweepMirrors(ctx, open.Projects)
}

// busyBranches names the branch folders the running jobs use, and says whether any job runs.
func (r *Runner) busyBranches() (map[string]bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	busy := map[string]bool{}
	for _, j := range r.jobs {
		for i := range j.spec.Projects {
			p := &j.spec.Projects[i]
			busy[branchDir.ReplaceAllString(p.Branch, "_")] = true
		}
	}
	return busy, len(r.jobs) > 0
}

func (r *Runner) branchBusy(name string) bool {
	busy, _ := r.busyBranches()
	return busy[name]
}

// sweepBranchDir removes the worktrees under dir, one per project of the task, and then dir. An
// entry that is no project's worktree (a report an agent wrote through ../) goes with it; a
// worktree whose work cannot be saved keeps dir. A folder that holds no worktree at all is removed
// only while no job runs: an agent may be writing there right now.
func (r *Runner) sweepBranchDir(ctx context.Context, dir string, anyJob bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A stray file at the top of worktrees/.
		if !anyJob {
			r.removeStray(dir)
		}
		return
	}
	kept, found := false, false
	for _, e := range entries {
		wt := filepath.Join(dir, e.Name())
		mirror := r.worktreeMirror(wt)
		if mirror == "" {
			continue
		}
		found = true
		if !r.reclaimWorktree(ctx, wt, mirror, filepath.Base(dir)) {
			kept = true
		}
	}
	if kept || (!found && anyJob) {
		return
	}
	r.removeStray(dir)
}

func (r *Runner) removeStray(path string) {
	size, done := diskSize(path, sizeWalkLimit)
	if err := os.RemoveAll(path); err != nil {
		logx.Warn(err, "hostrun.log.remove_failed", "path", path)
		return
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.worktree_stray_removed", "path", path, "size", bytesText(size, done)))
}

// worktreeMirror is the runner's clone that wt is a worktree of; empty when wt is not one.
func (r *Runner) worktreeMirror(wt string) string {
	gd := worktreeGitdir(wt)
	repos := realPath(filepath.Join(r.cfg.WorkDir, "repos"))
	// <WorkDir>/repos/<id>/.git/worktrees/<name>
	admin := filepath.Dir(gd)
	gitDir := filepath.Dir(admin)
	mirror := filepath.Dir(gitDir)
	if gd == "" || filepath.Base(admin) != "worktrees" || filepath.Base(gitDir) != ".git" || filepath.Dir(mirror) != repos {
		return ""
	}
	return mirror
}

// A commit for the sweep's save, as `git commit` reads it.
const sweepCommitMsg = "work left in a closed task's worktree" //i18n:ignore git commit subject

// reclaimWorktree saves what wt holds that is nowhere else and removes it, under the clone's lock.
// It tells whether wt is gone.
func (r *Runner) reclaimWorktree(ctx context.Context, wt, mirror, branchName string) bool {
	unlock := r.lockRepo(filepath.Base(mirror))
	defer unlock()
	// A job of the task may have started since the daemon answered.
	if r.branchBusy(branchName) {
		return false
	}
	// A detached HEAD has no branch to delete; its commit is still saved.
	branch, err := git(ctx, wt, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		branch = ""
	}
	if err := saveWorktree(ctx, wt, mirror, branch); err != nil {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.worktree_kept", "path", wt, "branch", branch, "err", apperr.Text(err)))
		return false
	}
	size, done := diskSize(wt, sizeWalkLimit)
	if _, err := git(ctx, mirror, "worktree", "remove", "--force", wt); err != nil {
		logx.Warn(err, "hostrun.log.worktree_remove_failed", "path", wt)
	}
	if err := os.RemoveAll(wt); err != nil {
		logx.Warn(err, "hostrun.log.remove_failed", "path", wt)
		return false
	}
	if _, err := git(ctx, mirror, "worktree", "prune"); err != nil {
		logx.Warn(err, "hostrun.log.prune_failed", "repo", mirror)
	}
	if branch != "" && validRef(ctx, mirror, branch) == nil {
		_, err := git(ctx, mirror, "branch", "-D", branch)
		logx.Warn(err, "hostrun.log.worktree_remove_failed", "path", wt)
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.worktree_removed", "path", wt, "branch", branch, "size", bytesText(size, done)))
	return true
}

// saveWorktree commits what wt holds and makes sure its HEAD is in a branch the clone knows: one of
// origin's (pushed or integrated), or an unsent ref it fetches into the clone now.
func saveWorktree(ctx context.Context, wt, mirror, branch string) error {
	if err := commitAll(ctx, wt, sweepCommitMsg); err != nil {
		return err
	}
	head, err := git(ctx, wt, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if contained(ctx, mirror, head, "refs/remotes/", unsentRefs, daemonRefs) {
		return nil
	}
	name := branch
	if name == "" {
		name = "detached"
	}
	ref := unsentRefs + branchDir.ReplaceAllString(name, "_") + "/sweep-" + time.Now().UTC().Format("20060102T150405Z")
	if err := saveUnsent(ctx, wt, mirror, ref); err != nil {
		return err
	}
	if !contained(ctx, mirror, head, ref) {
		return apperr.Precondition("hostrun.errors.worktree_not_saved", "ref", ref)
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.worktree_saved", "path", wt, "ref", ref))
	return nil
}

// contained tells whether a ref under one of prefixes holds commit.
func contained(ctx context.Context, repo, commit string, prefixes ...string) bool {
	args := append([]string{"for-each-ref", "--format=%(refname)", "--contains", commit}, prefixes...)
	out, err := git(ctx, repo, args...)
	return err == nil && strings.TrimSpace(out) != ""
}

// sweepMirrors removes the clones of projects that are neither mapped in runner.json nor known to
// the daemon for this runner's workspace, once no worktree uses them and no unsent ref waits in
// them.
func (r *Runner) sweepMirrors(ctx context.Context, projects map[string][]string) {
	dir := filepath.Join(r.cfg.WorkDir, "repos")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, id := range r.cfg.ProjectIDs() {
		known[branchDir.ReplaceAllString(id, "_")] = true
	}
	for id := range projects {
		known[branchDir.ReplaceAllString(id, "_")] = true
	}
	r.mu.Lock()
	for _, j := range r.jobs {
		for i := range j.spec.Projects {
			p := &j.spec.Projects[i]
			known[branchDir.ReplaceAllString(p.ID, "_")] = true
		}
	}
	r.mu.Unlock()
	for _, e := range entries {
		if !e.IsDir() || known[e.Name()] {
			continue
		}
		r.removeMirror(ctx, filepath.Join(dir, e.Name()))
	}
}

func (r *Runner) removeMirror(ctx context.Context, mirror string) {
	unlock := r.lockRepo(filepath.Base(mirror))
	defer unlock()
	if _, err := git(ctx, mirror, "worktree", "prune"); err != nil {
		return
	}
	list, err := git(ctx, mirror, "worktree", "list", "--porcelain")
	if err != nil || strings.Count(list, "worktree ") > 1 {
		return
	}
	if refs, rerr := git(ctx, mirror, "for-each-ref", "--format=%(refname)", unsentRefs); rerr != nil || refs != "" {
		return
	}
	size, done := diskSize(mirror, sizeWalkLimit)
	if err := os.RemoveAll(mirror); err != nil {
		logx.Warn(err, "hostrun.log.remove_failed", "path", mirror)
		return
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.mirror_removed", "path", mirror, "size", bytesText(size, done)))
}

// WorkDirSize measures the runner's work dir for `thulu runner doctor`.
func WorkDirSize(dir string, limit time.Duration) (int64, bool) { return diskSize(dir, limit) }

// Worktrees asks the daemon which task branches are still open, by project.
func (c *Client) Worktrees(ctx context.Context) (*runner.WorktreesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, worktreesTimeout)
	defer cancel()
	var out runner.WorktreesResponse
	if err := c.do(ctx, http.MethodGet, "worktrees", "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

var worktreesTimeout = 30 * time.Second
