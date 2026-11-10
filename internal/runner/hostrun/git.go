package hostrun

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed git verbs; refs come from the daemon's job and are checked
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.git", "args", strings.Join(args, " "),
			"output", strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func refExists(ctx context.Context, repo, ref string) bool {
	_, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func isAncestor(ctx context.Context, repo, a, b string) bool {
	_, err := git(ctx, repo, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func validRef(ctx context.Context, repo, ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return apperr.Invalid("hostrun.errors.bad_ref", "ref", ref)
	}
	if _, err := git(ctx, repo, "check-ref-format", "--branch", ref); err != nil {
		return apperr.Invalid("hostrun.errors.bad_ref", "ref", ref)
	}
	return nil
}

const fetchTimeout = 60 * time.Second

func baseRef(ctx context.Context, repo, defaultBranch string) string {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	_, err := git(fctx, repo, "fetch", "--quiet", "origin", defaultBranch)
	cancel()
	logx.Warn(err, "hostrun.log.fetch_failed", "branch", defaultBranch, "repo", repo)
	if r := "origin/" + defaultBranch; refExists(ctx, repo, r) {
		return r
	}
	return defaultBranch
}

func fetchBranch(ctx context.Context, repo, branch string) bool {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	if _, err := git(fctx, repo, "fetch", "--quiet", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return false
	}
	return refExists(ctx, repo, "refs/remotes/origin/"+branch)
}

func ensureTaskWorktree(ctx context.Context, repo, wt, branch, defaultBranch string) (bool, error) {
	onOrigin := fetchBranch(ctx, repo, branch)
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		if onOrigin {
			_, err := git(ctx, wt, "merge", "--quiet", "--ff-only", "origin/"+branch)
			logx.Warn(err, "hostrun.log.fast_forward_failed", "branch", branch, "path", wt)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(wt), 0o750); err != nil {
		return false, apperr.Wrap(err, "hostrun.errors.worktree", "path", wt)
	}
	if _, err := git(ctx, repo, "worktree", "prune"); err != nil {
		logx.Warn(err, "hostrun.log.prune_failed", "repo", repo)
	}
	var err error
	switch {
	case refExists(ctx, repo, "refs/heads/"+branch):
		_, err = git(ctx, repo, "worktree", "add", wt, branch)
		if err == nil && onOrigin {
			_, ferr := git(ctx, wt, "merge", "--quiet", "--ff-only", "origin/"+branch)
			logx.Warn(ferr, "hostrun.log.fast_forward_failed", "branch", branch, "path", wt)
		}
	case onOrigin:
		_, err = git(ctx, repo, "worktree", "add", "-b", branch, wt, "origin/"+branch)
	default:
		_, err = git(ctx, repo, "worktree", "add", "-b", branch, wt, baseRef(ctx, repo, defaultBranch))
	}
	if err != nil {
		return false, err
	}
	// Track origin's branch so the agent's plain `git push` lands where the runner's own push
	// would.
	if _, err = git(ctx, repo, "config", "branch."+branch+".remote", "origin"); err != nil {
		logx.Warn(err, "hostrun.log.upstream_failed", "branch", branch, "repo", repo)
		return true, nil
	}
	_, err = git(ctx, repo, "config", "branch."+branch+".merge", "refs/heads/"+branch)
	logx.Warn(err, "hostrun.log.upstream_failed", "branch", branch, "repo", repo)
	return true, nil
}

// info/exclude is shared by all the clone's worktrees and owned by the runner; it keeps the copied
// project files out of every commit.
func excludeProjectFiles(repo string, files []string) error {
	var b strings.Builder
	b.WriteString("# octopus runner: files copied from the checkout (project_files)\n") //i18n:ignore a comment in a git file
	for _, f := range files {
		b.WriteString("/" + filepath.ToSlash(filepath.Clean(f)) + "\n")
	}
	p := filepath.Join(repo, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.worktree", "path", p)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		return apperr.Wrap(err, "hostrun.errors.worktree", "path", p)
	}
	return nil
}

func readOnlyWorktree(ctx context.Context, repo, wt, branch, defaultBranch string) error {
	onOrigin := fetchBranch(ctx, repo, branch)
	local := refExists(ctx, repo, "refs/heads/"+branch)
	ref := branch
	switch {
	case local && onOrigin && isAncestor(ctx, repo, branch, "origin/"+branch):
		ref = "origin/" + branch
	case local:
	case onOrigin:
		ref = "origin/" + branch
	case refExists(ctx, repo, "origin/"+defaultBranch):
		ref = "origin/" + defaultBranch
	default:
		ref = defaultBranch
	}
	if err := os.MkdirAll(filepath.Dir(wt), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.worktree", "path", wt)
	}
	_, err := git(ctx, repo, "worktree", "add", "--detach", wt, ref)
	return err
}

func removeWorktree(ctx context.Context, repo, wt string) {
	_, err := git(ctx, repo, "worktree", "remove", "--force", wt)
	logx.Warn(err, "hostrun.log.worktree_remove_failed", "path", wt)
}

func commitAll(ctx context.Context, wt, msg string) (bool, error) {
	status, err := git(ctx, wt, "status", "--porcelain")
	if err != nil || status == "" {
		return false, err
	}
	if _, err := git(ctx, wt, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := git(ctx, wt, "commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

const pushTimeout = 2 * time.Minute

func pushBranch(ctx context.Context, wt, branch string) (commit, remote string, err error) {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	// The agent may have rebased the branch; the lease lets that through and still fails if someone
	// else pushed since the last fetch.
	if _, err = git(ctx, wt, "push", "--quiet", "--force-with-lease", "--set-upstream", "origin", branch); err != nil {
		return "", "", err
	}
	if commit, err = git(ctx, wt, "rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	remote, rerr := git(ctx, wt, "remote", "get-url", "origin")
	logx.Warn(rerr, "hostrun.log.remote_url_failed", "path", wt)
	return commit, redactRemote(remote), nil
}

func redactRemote(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
		rest = rest[at+1:]
	}
	return scheme + "://" + rest
}
