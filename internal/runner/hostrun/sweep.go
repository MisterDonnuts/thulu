package hostrun

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const pidFile = ".octopus-run.pid"

// sweepLeftovers stops what a previous runner process left running and removes its run folders. A
// Thulu project's clone in one goes to its mirror first (saveLeftover); a folder whose work could not
// be saved stays for the next start.
func sweepLeftovers(ctx context.Context, runsDir string) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return
	}
	mirrors := filepath.Join(filepath.Dir(runsDir), "repos")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(runsDir, e.Name())
		killLeftover(ctx, root)
		if !saveLeftovers(ctx, root, mirrors, e.Name()) {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept", "path", root))
			continue
		}
		removeSession(root)
		if err := os.RemoveAll(root); err != nil {
			logx.Warn(err, "hostrun.log.remove_failed", "path", root)
			continue
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.swept", "job", e.Name()))
	}
}

// killLeftover stops the process group whose leader's pid a run left in root.
func killLeftover(ctx context.Context, root string) {
	b, err := os.ReadFile(filepath.Join(root, pidFile)) //nolint:gosec // G304: a file this runner wrote in its own work dir
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 && leftoverGroup(ctx, pid, root) {
		killGroup(pid)
	}
}

// saveLeftovers saves every Thulu project clone under root and reports whether all of them are safe.
// A folder project's entry is a symlink to a worktree that outlives the run.
func saveLeftovers(ctx context.Context, root, mirrors, jobID string) bool {
	entries, err := os.ReadDir(filepath.Join(root, "projects"))
	if err != nil {
		return true
	}
	saved := true
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		clone := filepath.Join(root, "projects", e.Name())
		if err := saveLeftover(ctx, clone, mirrors, jobID); err != nil {
			logx.Warn(err, "hostrun.log.leftover_save_failed", "path", clone)
			saved = false
		}
	}
	return saved
}

// saveLeftover commits what the agent left in a writable clone and keeps its HEAD in the mirror it
// borrows objects from, unless the mirror already has it. A read-only clone has no result branch.
func saveLeftover(ctx context.Context, clone, mirrors, jobID string) error {
	push, err := git(ctx, clone, "config", "remote.origin.push")
	resultRef, ok := strings.CutPrefix(push, "+HEAD:refs/heads/")
	if err != nil || !ok {
		return nil
	}
	if verr := validRef(ctx, clone, resultRef); verr != nil {
		return verr
	}
	mirror, err := cloneMirror(clone, mirrors)
	if err != nil {
		return err
	}
	msg := "run " + jobID + ": work left when the runner stopped" //i18n:ignore git commit subject
	if err := commitAll(ctx, clone, msg); err != nil {
		return err
	}
	if inMirror(ctx, clone, mirror) {
		return nil
	}
	return saveUnsent(ctx, clone, mirror, unsentRef(resultRef, jobID))
}

// cloneMirror reads the mirror a clone --shared borrows from. The agent could rewrite that file, so
// only a mirror of this runner's counts.
func cloneMirror(clone, mirrors string) (string, error) {
	alt := filepath.Join(clone, ".git", "objects", "info", "alternates")
	b, err := os.ReadFile(alt) //nolint:gosec // G304: a file in this runner's own work dir
	if err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.leftover_mirror", "path", clone)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	mirror := filepath.Dir(realPath(line))
	if filepath.Dir(mirror) != realPath(mirrors) {
		return "", apperr.Invalid("hostrun.errors.leftover_mirror", "path", clone)
	}
	return mirror, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// inMirror tells whether one of the mirror's branches already holds the clone's HEAD.
func inMirror(ctx context.Context, clone, mirror string) bool {
	tips, err := git(ctx, mirror, "for-each-ref", "--format=%(objectname)", daemonRefs, unsentRefs)
	if err != nil {
		return false
	}
	for _, tip := range strings.Fields(tips) {
		if isAncestor(ctx, clone, "HEAD", tip) {
			return true
		}
	}
	return false
}

func removeSession(root string) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	if dir := claudeProjectsDir(resolved); dir != "" {
		logx.Warn(os.RemoveAll(dir), "hostrun.log.remove_failed", "path", dir)
	}
}

// While the leader lives its command line must name root, so a pid reused by another process is
// left alone.
// Once the leader is gone, any process still in pgid is the run's: no new process gets a pid equal
// to a live group id.
func leftoverGroup(ctx context.Context, pgid int, root string) bool {
	if out, err := commandLine(ctx, pgid); err == nil {
		return strings.Contains(out, root)
	}
	return groupHasMembers(ctx, pgid)
}
