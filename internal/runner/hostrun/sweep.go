package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const pidFile = ".octopus-run.pid"

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

// removeSession removes what Claude Code keeps about a run outside its folder: the session under
// ~/.claude/projects and the folder under /tmp/claude-<uid> where it writes the output of every
// background Bash command, which grows as long as the command logs.
func removeSession(root string) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	if dir := claudeProjectsDir(resolved); dir != "" {
		logx.Warn(os.RemoveAll(dir), "hostrun.log.remove_failed", "path", dir)
	}
	removeClaudeTmp(resolved)
}

// The machine's shared temp folder; tests point it at a folder of their own.
var tmpRoot = "/tmp"

// claudeTmpDir is the folder Claude Code keeps for a session started in dir (resolved): it ignores
// TMPDIR and uses /tmp/claude-<uid>/<dir with every non-alphanumeric as "-">, the same name as
// claudeProjectsDir. Checked in Claude Code 2.1.332, which reads CLAUDE_CODE_TMPDIR to move it.
func claudeTmpDir(dir string) string {
	return filepath.Join(realPath(tmpRoot), "claude-"+strconv.Itoa(os.Getuid()), nonAlnum.ReplaceAllString(dir, "-"))
}

func removeClaudeTmp(resolved string) {
	dir := claudeTmpDir(resolved)
	logx.Warn(os.RemoveAll(dir), "hostrun.log.remove_failed", "path", dir)
}
