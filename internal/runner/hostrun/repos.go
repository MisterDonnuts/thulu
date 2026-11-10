package hostrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Task worktrees come from the runner's own clone, never the person's checkout, so the agent's git
// (config, hooks, `git branch -D`) cannot reach their repository.

func (c *Config) mirrorDir(projectID string) string {
	return filepath.Join(c.WorkDir, "repos", branchDir.ReplaceAllString(projectID, "_"))
}

// Two runs of one project would otherwise race on the clone's fetches and worktree list.
func (r *Runner) lockRepo(projectID string) func() {
	v, _ := r.repoLocks.LoadOrStore(projectID, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		return func() {}
	}
	mu.Lock()
	return mu.Unlock
}

func ensureMirror(ctx context.Context, checkout, mirror string) (string, error) {
	origin, err := git(ctx, checkout, "remote", "get-url", "origin")
	if err != nil || origin == "" {
		origin = checkout
	}
	if _, serr := os.Stat(filepath.Join(mirror, ".git")); serr != nil {
		if err := os.MkdirAll(filepath.Dir(mirror), 0o750); err != nil {
			return "", apperr.Wrap(err, "hostrun.errors.worktree", "path", mirror)
		}
		if _, err := git(ctx, filepath.Dir(mirror), "clone", "--quiet", "--no-checkout", "--", checkout, mirror); err != nil {
			return "", err
		}
	}
	if cur, err := git(ctx, mirror, "remote", "get-url", "origin"); err != nil || cur != origin {
		if _, err := git(ctx, mirror, "remote", "set-url", "origin", origin); err != nil {
			return "", err
		}
	}
	return origin, nil
}

func checkRepo(p runner.JobProject, checkout, origin string) error {
	if p.RepoURL == "" || repoKey(origin) == repoKey(p.RepoURL) {
		return nil
	}
	return apperr.Precondition("hostrun.errors.wrong_repo", "project", p.Name, "path", checkout,
		"origin", redactRemote(origin), "want", redactRemote(p.RepoURL))
}

func repoKey(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	} else if at := strings.Index(u, "@"); at >= 0 && strings.Contains(u[at:], ":") {
		u = strings.Replace(u[at+1:], ":", "/", 1)
	} else {
		return filepath.Clean(u)
	}
	host, path, _ := strings.Cut(u, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return host + "/" + path
}

// Both ends are opened as roots so a listed path cannot leave either folder through ".." or a
// symlink.
func copyProjectFiles(checkout, wt string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	src, err := os.OpenRoot(checkout)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.project_file", "file", checkout)
	}
	defer logx.Close(src, checkout)
	dst, err := os.OpenRoot(wt)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.project_file", "file", wt)
	}
	defer logx.Close(dst, wt)
	for _, f := range files {
		rel := filepath.Clean(f)
		b, err := src.ReadFile(rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = dst.MkdirAll(filepath.Dir(rel), 0o750)
		}
		if err == nil {
			err = dst.WriteFile(rel, b, 0o600)
		}
		if err != nil {
			return apperr.Wrap(err, "hostrun.errors.project_file", "file", f)
		}
	}
	return nil
}

type checkoutState struct {
	path, head, status string
}

func snapshotCheckout(ctx context.Context, checkout string) checkoutState {
	head, err := git(ctx, checkout, "rev-parse", "HEAD")
	logx.Warn(err, "hostrun.log.checkout_state_failed", "path", checkout)
	status, err := git(ctx, checkout, "status", "--porcelain", "--untracked-files=normal")
	logx.Warn(err, "hostrun.log.checkout_state_failed", "path", checkout)
	sum := sha256.Sum256([]byte(status))
	return checkoutState{path: checkout, head: head, status: hex.EncodeToString(sum[:])}
}

func onTaskBranch(ctx context.Context, wt, branch string) (bool, string) {
	head, err := git(ctx, wt, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		head = "HEAD (detached)" //i18n:ignore git's own term, shown inside a sentence
	}
	return head == branch, head
}
