package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func BareRepo(t *testing.T) string {
	t.Helper()
	return BareRepoAt(t, filepath.Join(t.TempDir(), "remote.git"))
}

func BareRepoAt(t *testing.T, dest string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(i18n.Log("testgit.errors.mkdir_failed", "path", src, "err", err))
	}
	// Identity is set on the repo: a CI runner with no git identity cannot commit.
	git(t, src, "init", "-b", "main")
	git(t, src, "config", "user.email", "test@example.com")
	git(t, src, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(i18n.Log("testgit.errors.write_readme_failed", "err", err))
	}
	git(t, src, "add", ".")
	git(t, src, "commit", "-m", "init")
	git(t, "", "clone", "--bare", src, dest)
	return dest
}

func Clone(t *testing.T, src, dest string) {
	t.Helper()
	git(t, "", "clone", src, dest)
}

// Pass root with symlinks resolved (filepath.EvalSymlinks of t.TempDir() on macOS): git records
// real paths in the worktree's .git pointer.
func LinkedWorktree(t *testing.T, src, root, name, branch string) string {
	t.Helper()
	clone := filepath.Join(root, name)
	if _, err := os.Stat(clone); err != nil {
		Clone(t, src, clone)
	}
	wt := filepath.Join(root, ".worktrees", name+"--"+branch)
	git(t, clone, "worktree", "add", "-b", branch, wt)
	return wt
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(i18n.Log("testgit.errors.command_failed", "args", args, "output", out, "err", err))
	}
}
