package hostrun

import (
	"context"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/gitremote"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// A project kept by Thulu comes from the daemon's /git/ with the run's key. The runner keeps one bare
// mirror per project and fetches into it by URL, never through a remote: the tasks sharing the mirror
// each reach it by their own URL. Every job copies the fetched tip into a clone of its own, so two
// jobs of one task never fight over a branch, and the agent's git config stays out of the mirror.

// Fetched branches land here, not in refs/heads, so nothing the agent does in its clone moves them.
const daemonRefs = "refs/octopus/heads/"

// A result that never reached the daemon waits here, under its result branch, until the next job of
// the same root task hands it over.
const unsentRefs = "refs/octopus/unsent/"

func unsentRef(resultRef, jobID string) string {
	return unsentRefs + resultRef + "/" + branchDir.ReplaceAllString(jobID, "_")
}

// A first fetch copies the whole history, often over a home uplink, so a fetch has no deadline: one
// that moves less than 1 KB/s for a minute fails instead.
var daemonFetchLimits = []string{"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=60"}

// The daemon answers a final push once the result is integrated, which may wait on a publish of the
// same task to GitHub (agentPublishTimeout, 2 minutes); the upload comes on top.
const resultPushTimeout = 5 * time.Minute

// The identity a Thulu project's clone commits with when the machine has none: the clone is Thulu's.
//
//i18n:ignore a git identity, not a message
var thuluIdentity = [][]string{{"user.name", "Thulu"}, {"user.email", "noreply@thulu.dev"}}

// The helper reads the key from the env when git asks, so it never lands in a URL or a config.
//
//i18n:ignore credential helper shell snippet
const daemonHelper = `!f() { test "$1" = get || exit 0; echo username=octopus; echo "password=${OCTOPUS_API_KEY}"; }; f`

// The empty value drops the person's helpers first: osxkeychain would otherwise store the run key.
var daemonCredential = []string{"-c", "credential.helper=", "-c", "credential.helper=" + daemonHelper}

func (j *job) daemonGitURL() string { return strings.TrimRight(j.r.cfg.DaemonURL, "/") + "/git/" }

func (j *job) daemonRepoURL(p runner.JobProject) string {
	return j.daemonGitURL() + url.PathEscape(j.spec.TaskID) + "/" + url.PathEscape(mountName(p))
}

func (j *job) daemonGit(ctx context.Context, dir string, args ...string) (string, error) {
	if j.gitEnv == nil {
		j.gitEnv = j.env()
	}
	return gitEnv(ctx, j.gitEnv, dir, slices.Concat(daemonCredential, args)...)
}

// linkDaemonProject puts a fresh copy of the task branch (or of the default branch, for a read-only
// run without one) at dir.
func (j *job) linkDaemonProject(ctx context.Context, p runner.JobProject, dir string) error {
	writable := !j.spec.ReadOnly
	if writable {
		if p.Branch == "" {
			return apperr.Invalid("hostrun.errors.no_task_branch", "project", p.Name)
		}
		if err := validRef(ctx, j.root, j.spec.ResultRef); err != nil {
			return err
		}
	}
	unlock := j.r.lockRepo(p.ID)
	defer unlock()
	mirror := j.r.cfg.mirrorDir(p.ID)
	if writable {
		j.sendUnsent(ctx, p, mirror)
	}
	start, err := j.fetchDaemon(ctx, p, mirror)
	if err != nil {
		return err
	}
	if err := j.jobClone(ctx, p, mirror, dir, start); err != nil {
		return apperr.Wrap(err, "hostrun.errors.daemon_clone", "project", p.Name)
	}
	if writable {
		j.wts[p.ID] = dir
	}
	return nil
}

// fetchDaemon brings the default branch and the task branch into the mirror and returns the branch the
// job starts from. Only a read-only run may do without the task branch, and only when the daemon has
// none: a failed listing or fetch fails the job.
func (j *job) fetchDaemon(ctx context.Context, p runner.JobProject, mirror string) (string, error) {
	if err := validRef(ctx, j.root, p.DefaultBranch); err != nil {
		return "", err
	}
	if p.Branch != "" {
		if err := validRef(ctx, j.root, p.Branch); err != nil {
			return "", err
		}
	}
	if err := initMirror(ctx, mirror); err != nil {
		return "", err
	}
	src := j.daemonRepoURL(p)
	refs, err := j.daemonRefsAt(ctx, mirror, src, p)
	if err != nil {
		return "", err
	}
	// Task worktrees branch from the daemon's origin/<default>; its local default stays where the clone left it.
	def := "refs/remotes/origin/" + p.DefaultBranch
	if !refs[def] {
		def = "refs/heads/" + p.DefaultBranch
	}
	if !refs[def] {
		return "", apperr.NotFound("hostrun.errors.daemon_missing_branch", "branch", p.DefaultBranch, "project", p.Name)
	}
	specs := []string{"+" + def + ":" + daemonRefs + p.DefaultBranch}
	start := p.DefaultBranch
	switch {
	case p.Branch != "" && refs["refs/heads/"+p.Branch]:
		specs = append(specs, "+refs/heads/"+p.Branch+":"+daemonRefs+p.Branch)
		start = p.Branch
	case !j.spec.ReadOnly:
		return "", apperr.NotFound("hostrun.errors.daemon_missing_branch", "branch", p.Branch, "project", p.Name)
	}
	args := slices.Concat(daemonFetchLimits, []string{"fetch", "--quiet", "--no-tags", "--end-of-options", src}, specs)
	if _, err := j.daemonGit(ctx, mirror, args...); err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.daemon_fetch", "branch", start, "project", p.Name)
	}
	pruneMirror(ctx, mirror, refs, p)
	return start, nil
}

func initMirror(ctx context.Context, mirror string) error {
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(mirror, 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.worktree", "path", mirror)
	}
	_, err := git(ctx, mirror, "init", "--quiet", "--bare")
	return err
}

// daemonRefsAt lists the daemon's refs, so a branch it lacks is told apart from a fetch that failed.
func (j *job) daemonRefsAt(ctx context.Context, mirror, src string, p runner.JobProject) (map[string]bool, error) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	out, err := j.daemonGit(fctx, mirror, "ls-remote", "--refs", "--end-of-options", src)
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.daemon_list", "project", p.Name)
	}
	refs := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			refs[ref] = true
		}
	}
	return refs, nil
}

// The daemon deletes a task's branch when the task ends; its copy here goes on the next fetch.
func pruneMirror(ctx context.Context, mirror string, refs map[string]bool, p runner.JobProject) {
	out, err := git(ctx, mirror, "for-each-ref", "--format=%(refname)", daemonRefs)
	if err != nil {
		logx.Warn(err, "hostrun.log.mirror_prune_failed", "project", p.Name)
		return
	}
	for _, ref := range strings.Fields(out) {
		if b := strings.TrimPrefix(ref, daemonRefs); b != p.DefaultBranch && !refs["refs/heads/"+b] {
			_, err := git(ctx, mirror, "update-ref", "-d", ref)
			logx.Warn(err, "hostrun.log.mirror_prune_failed", "project", p.Name)
		}
	}
}

// jobClone borrows the mirror's objects instead of copying them. origin is the daemon's /git/, as in a
// pod: the agent's own fetch, rebase and push work mid-run, and a push lands on the result branch.
// A read-only run gets the code on a detached HEAD.
func (j *job) jobClone(ctx context.Context, p runner.JobProject, mirror, dir, start string) error {
	tip, err := git(ctx, mirror, "rev-parse", "--verify", daemonRefs+start+"^{commit}")
	if err != nil {
		return err
	}
	base, err := git(ctx, mirror, "rev-parse", "--verify", daemonRefs+p.DefaultBranch+"^{commit}")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	if _, err := git(ctx, filepath.Dir(dir), "clone", "--quiet", "--shared", "--no-checkout", "--", mirror, dir); err != nil {
		return err
	}
	// Scoped to the daemon's /git/: another host the agent reaches from here must never get the key.
	helper := "credential." + j.daemonGitURL() + ".helper"
	steps := [][]string{
		{"remote", "set-url", "origin", j.daemonRepoURL(p)},
		{"config", "--replace-all", "remote.origin.fetch", "+refs/remotes/origin/*:refs/remotes/origin/*"},
		{"config", "--add", helper, ""},
		{"config", "--add", helper, daemonHelper},
		{"update-ref", "refs/remotes/origin/" + p.DefaultBranch, base},
	}
	checkout := []string{"checkout", "--quiet", "--detach", tip}
	if !j.spec.ReadOnly {
		// The person's signing setup may need a touch or a dialog nobody answers mid-run.
		steps = append(steps, []string{"config", "remote.origin.push", "+HEAD:refs/heads/" + j.spec.ResultRef},
			[]string{"config", "commit.gpgsign", "false"})
		for _, kv := range thuluIdentity {
			if v, err := git(ctx, dir, "config", kv[0]); err != nil || v == "" {
				steps = append(steps, []string{"config", kv[0], kv[1]})
			}
		}
		checkout = []string{"checkout", "--quiet", "-B", p.Branch, tip}
	}
	for _, args := range append(steps, checkout) {
		if _, err := git(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// pushResult hands the run's work to the daemon on the result branch, which the daemon integrates
// into the task's worktree. HEAD goes even when the agent left the task branch: the clone is deleted
// after the run, and the daemon refuses a result that drops the task's commits.
func (j *job) pushResult(ctx context.Context, p runner.JobProject, dir string) (runner.PushedBranch, error) {
	if err := j.finalPush(ctx, p, dir, "HEAD"); err != nil {
		return runner.PushedBranch{}, err
	}
	commit, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return runner.PushedBranch{}, err
	}
	return runner.PushedBranch{ProjectID: p.ID, Branch: j.spec.ResultRef, Commit: commit,
		Remote: redactRemote(j.daemonRepoURL(p))}, nil
}

// finalPush sends src from the repo at dir to the result branch, marked so the daemon integrates it
// and leaves publishing to the column.
func (j *job) finalPush(ctx context.Context, p runner.JobProject, dir, src string) error {
	ctx, cancel := context.WithTimeout(ctx, resultPushTimeout)
	defer cancel()
	_, err := j.daemonGit(ctx, dir, "-c", "http.extraHeader="+gitremote.FinalPushHeader+": 1",
		"push", "--quiet", "--end-of-options", j.daemonRepoURL(p), "+"+src+":refs/heads/"+j.spec.ResultRef)
	return apperr.Wrap(err, "hostrun.errors.result_push", "project", p.Name, "ref", j.spec.ResultRef)
}

// saveUnsent keeps the clone's HEAD in its mirror, where the next job of the task finds it. A fetch
// from the clone, not a push into the mirror: the agent's hooks and the person's push settings stay out.
func saveUnsent(ctx context.Context, clone, mirror, ref string) error {
	_, err := git(ctx, mirror, "fetch", "--quiet", "--no-tags", "--end-of-options", clone, "+HEAD:"+ref)
	return err
}

// sendUnsent hands the daemon what earlier jobs of this root task could not, oldest first, before the
// job fetches the task branch it would land on. Whatever still fails waits for the next job.
func (j *job) sendUnsent(ctx context.Context, p runner.JobProject, mirror string) {
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err != nil {
		return
	}
	out, err := git(ctx, mirror, "for-each-ref", "--sort=committerdate", "--format=%(refname)", unsentRefs+j.spec.ResultRef+"/")
	if err != nil {
		logx.Warn(err, "hostrun.log.unsent_failed", "project", p.Name)
		return
	}
	for _, ref := range strings.Fields(out) {
		if err := j.finalPush(ctx, p, mirror, ref); err != nil {
			logx.Warn(err, "hostrun.log.unsent_failed", "project", p.Name)
			return
		}
		_, err := git(ctx, mirror, "update-ref", "-d", ref)
		logx.Warn(err, "hostrun.log.unsent_failed", "project", p.Name)
		log.Printf("[runner] %s", i18n.Log("hostrun.log.unsent_sent", "project", p.Name, "ref", ref))
	}
}
