package gitworktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var ErrNotLinkedWorktree error = apperr.New("gitworktree.errors.not_linked")

// Resolved from commonDir's own worktrees/*/gitdir entries, never from the worktree's .git file,
// which the agent can rewrite.
func AdminDir(commonDir, worktreeDir string) (string, error) {
	if fi, err := os.Lstat(commonDir); err != nil || !fi.IsDir() {
		return "", apperr.New("gitworktree.errors.git_dir_not_directory", "dir", commonDir)
	}
	admin := filepath.Join(commonDir, "worktrees")
	entries, err := os.ReadDir(admin)
	if err != nil {
		return "", apperr.Wrap(err, "gitworktree.errors.not_registered", "worktree", worktreeDir, "common", commonDir)
	}
	want := map[string]bool{filepath.Join(filepath.Clean(worktreeDir), ".git"): true}
	if resolved, err := filepath.EvalSymlinks(worktreeDir); err == nil {
		want[filepath.Join(resolved, ".git")] = true
	}
	if abs, err := filepath.Abs(worktreeDir); err == nil {
		want[filepath.Join(abs, ".git")] = true
	}
	for _, e := range entries {
		// DirEntry types come from lstat, so symlinked entries are skipped; the result must be a
		// real dir.
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(admin, e.Name())
		//nolint:gosec // G304: path is <common>/worktrees/<entry>/gitdir, owned by whoever owns commonDir
		b, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err != nil {
			continue
		}
		if want[filepath.Clean(strings.TrimSpace(string(b)))] {
			return dir, nil
		}
	}
	return "", apperr.New("gitworktree.errors.not_registered", "worktree", worktreeDir, "common", commonDir)
}

// The agent can rewrite the .git pointer. Without the AdminDir cross-check it could aim the next
// container's mount at any directory the daemon can read.
func CommonDir(worktreeDir string) (string, error) {
	pointer := filepath.Join(worktreeDir, ".git")
	fi, err := os.Lstat(pointer)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.IsDir()) {
		return "", ErrNotLinkedWorktree
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", apperr.New("gitworktree.errors.pointer_not_regular", "pointer", pointer)
	}
	//nolint:gosec // G304: the path is <worktree>/.git; its content is verified below, never trusted
	b, err := os.ReadFile(pointer)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	gitdir = strings.TrimSpace(gitdir)
	if !ok || gitdir == "" {
		return "", apperr.New("gitworktree.errors.pointer_empty", "pointer", pointer)
	}
	// A relative pointer (worktree.useRelativePaths) resolves against the worktree's location,
	// which differs inside the container, so mounting the common dir would not fix it.
	if !filepath.IsAbs(gitdir) {
		return "", apperr.New("gitworktree.errors.pointer_relative", "pointer", pointer, "gitdir", gitdir)
	}
	gitdir = filepath.Clean(gitdir)

	cb, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return "", apperr.Wrap(err, "gitworktree.errors.admin_dir", "dir", gitdir)
	}
	common := strings.TrimSpace(string(cb))
	if common == "" {
		return "", apperr.New("gitworktree.errors.admin_dir_empty_commondir", "dir", gitdir)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	common = filepath.Clean(common)

	admin, err := AdminDir(common, worktreeDir)
	if err != nil {
		return "", err
	}
	if admin != gitdir {
		return "", apperr.New("gitworktree.errors.pointer_mismatch", "pointer", pointer, "gitdir", gitdir, "common", common, "admin", admin)
	}
	return common, nil
}
