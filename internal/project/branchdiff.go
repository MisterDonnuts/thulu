package project

import (
	"context"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// DiffFile is one path a task branch changes against the merge base with the default branch.
// Binary files count as a file with no lines.
type DiffFile struct {
	Path    string
	Added   int
	Deleted int
}

// BranchDiff is what the auditor's signals mode reads of a task branch on the daemon host.
type BranchDiff struct {
	Files []DiffFile
	// The paths whose diff adds or removes a line that silences a linter, a type checker, a
	// coverage tool or a test (SuppressionPattern).
	Suppressions []string
}

// SuppressionPattern matches a line that turns a check off: lint and type-check pragmas, coverage
// exclusions, scanner allowlists, and skipped or focused tests. git -G reads it as a POSIX extended
// regex, so it avoids \b and other GNU-only syntax.
const SuppressionPattern = `eslint-disable|@ts-ignore|@ts-expect-error|@ts-nocheck|nolint|noqa|type: *ignore|` + //i18n:ignore regex
	`istanbul ignore|c8 ignore|NOSONAR|nosemgrep|gitleaks:allow|@SuppressWarnings|swiftlint:disable|` + //i18n:ignore regex
	`rubocop:disable|pragma: *no cover|prettier-ignore|\.(skip|only)\(|(^|[^A-Za-z0-9_.])(xit|xdescribe|fit|fdescribe)\(` //i18n:ignore regex

// WorktreeBranchDiff reads what the task branch changes against its merge base with the default
// branch, from the worktree on the daemon host. A project whose code lives elsewhere (a runner) has
// no such worktree and returns the error hostWorktreeGit gives.
func (s *Store) WorktreeBranchDiff(ctx context.Context, projectID, taskID string) (BranchDiff, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return BranchDiff{}, err
	}
	head, err := w.Exec.Output(w.Dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return BranchDiff{}, apperr.Wrap(err, "project.errors.worktree_head", "project", projectID)
	}
	head = strings.TrimSpace(head)
	base := s.WorktreeDefaultCommit(ctx, projectID, taskID)
	if base == "" {
		return BranchDiff{}, apperr.Precondition("project.errors.no_default_commit", "project", projectID)
	}
	mb, err := w.Exec.Output(w.Dir, "merge-base", base, head)
	if err != nil {
		return BranchDiff{}, apperr.Wrap(err, "project.errors.worktree_diff", "project", projectID)
	}
	from := strings.TrimSpace(mb)
	stat, err := w.Exec.Output(w.Dir, "diff", "--numstat", "--no-renames", "-z", from, head, "--")
	if err != nil {
		return BranchDiff{}, apperr.Wrap(err, "project.errors.worktree_diff", "project", projectID)
	}
	out := BranchDiff{Files: parseNumstat(stat)}
	if len(out.Files) == 0 {
		return out, nil
	}
	names, err := w.Exec.Output(w.Dir, "diff", "--name-only", "--no-renames", "-z", "-G"+SuppressionPattern, from, head, "--")
	if err != nil {
		return BranchDiff{}, apperr.Wrap(err, "project.errors.worktree_diff", "project", projectID)
	}
	for _, p := range strings.Split(names, "\x00") {
		if p != "" {
			out.Suppressions = append(out.Suppressions, p)
		}
	}
	return out, nil
}

// parseNumstat reads `git diff --numstat -z --no-renames`: "<added>\t<deleted>\t<path>" per NUL,
// with "-" for both counts on a binary file.
func parseNumstat(raw string) []DiffFile {
	var out []DiffFile
	for _, rec := range strings.Split(raw, "\x00") {
		added, rest, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		deleted, path, ok := strings.Cut(rest, "\t")
		if !ok || path == "" {
			continue
		}
		out = append(out, DiffFile{Path: path, Added: numstatCount(added), Deleted: numstatCount(deleted)})
	}
	return out
}

// "-" (a binary file) and anything else that is not a count read as no lines.
func numstatCount(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
