package project

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// What the locked-tests check reads from a task's worktree on the daemon host. Commits are full or
// abbreviated SHAs the daemon recorded itself, never agent input.

// The worktree's HEAD commit.
func (s *Store) WorktreeHead(ctx context.Context, projectID, taskID string) (string, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return "", err
	}
	out, err := w.Exec.Output(w.Dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.worktree_head", "project", projectID)
	}
	return strings.TrimSpace(out), nil
}

// The commit origin/<default> points at in the worktree's repo, or the local default branch when
// there is no remote-tracking ref. "" when neither resolves.
func (s *Store) WorktreeDefaultCommit(ctx context.Context, projectID, taskID string) string {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil || ValidateBranchName(w.DefaultBranch) != nil {
		return ""
	}
	for _, ref := range []string{"refs/remotes/origin/" + w.DefaultBranch, "refs/heads/" + w.DefaultBranch} {
		if out, err := w.Exec.Output(w.Dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

// Paths that differ between two commits, a rename counted as a delete plus an add. from "" means
// the merge base of to and the default branch.
func (s *Store) WorktreeChangedFiles(ctx context.Context, projectID, taskID, from, to string) ([]string, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !commitish(to) || from != "" && !commitish(from) {
		return nil, apperr.Invalid("project.errors.bad_commit")
	}
	if from == "" {
		base := s.WorktreeDefaultCommit(ctx, projectID, taskID)
		if base == "" {
			return nil, apperr.Precondition("project.errors.no_default_commit", "project", projectID)
		}
		mb, mbErr := w.Exec.Output(w.Dir, "merge-base", base, to)
		if mbErr != nil {
			return nil, apperr.Wrap(mbErr, "project.errors.worktree_diff", "project", projectID)
		}
		from = strings.TrimSpace(mb)
	}
	out, err := w.Exec.Output(w.Dir, "diff", "--name-only", "--no-renames", "-z", from, to, "--")
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.worktree_diff", "project", projectID)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// The git blob of each path at the commit; a path missing there maps to "".
func (s *Store) WorktreeBlobs(ctx context.Context, projectID, taskID, commit string, paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = ""
	}
	if len(paths) == 0 {
		return out, nil
	}
	found, err := s.treeBlobs(ctx, projectID, taskID, commit, paths)
	if err != nil {
		return nil, err
	}
	for p, sha := range found {
		if _, want := out[p]; want {
			out[p] = sha
		}
	}
	return out, nil
}

// Every file at the commit that is one of the paths or sits under one of them, with its blob. A
// directory in paths stands for the files it holds at that commit.
func (s *Store) WorktreeFilesUnder(ctx context.Context, projectID, taskID, commit string, paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	return s.treeBlobs(ctx, projectID, taskID, commit, paths)
}

func (s *Store) treeBlobs(ctx context.Context, projectID, taskID, commit string, paths []string) (map[string]string, error) {
	if !commitish(commit) {
		return nil, apperr.Invalid("project.errors.bad_commit")
	}
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	// --full-tree: paths are relative to the repository root whatever the cwd; -r so a nested path
	// matches its blob, not the tree that holds it, and a directory lists the files under it.
	// "--literal-pathspecs" keeps glob characters in a file name from matching other files.
	args := append([]string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree", commit, "--"}, paths...)
	raw, err := w.Exec.Output(w.Dir, args...)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.worktree_blobs", "project", projectID)
	}
	out := map[string]string{}
	for _, entry := range strings.Split(raw, "\x00") {
		// "<mode> <type> <sha>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		out[path] = fields[2]
	}
	return out, nil
}

// The merge base of two commits in the worktree's repo; "" when either does not resolve or they
// share no history.
func (s *Store) WorktreeMergeBase(ctx context.Context, projectID, taskID, a, b string) string {
	if !commitish(a) || !commitish(b) {
		return ""
	}
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return ""
	}
	out, err := w.Exec.Output(w.Dir, "merge-base", a, b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Where the task branch left the default branch: the merge base of the worktree's HEAD and
// origin/<default>. "" without a worktree or a default branch.
func (s *Store) WorktreeBase(ctx context.Context, projectID, taskID string) string {
	def := s.WorktreeDefaultCommit(ctx, projectID, taskID)
	if def == "" {
		return ""
	}
	head, err := s.WorktreeHead(ctx, projectID, taskID)
	if err != nil {
		return ""
	}
	return s.WorktreeMergeBase(ctx, projectID, taskID, def, head)
}

func commitish(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
