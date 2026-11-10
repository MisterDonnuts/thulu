package project

import (
	"context"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The agent's clone is the source of truth: a rebased result resets the worktree onto it.
// Rewritten (branch no longer contains its old tip) is what tells Publish to force-with-lease.
func (s *Store) IntegrateAgentResult(ctx context.Context, projectID, taskID, resultRef string) (bool, error) {
	if err := validateTaskID(taskID); err != nil {
		return false, err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return false, err
	}
	return integrateResultIntoWorktree(proj.Path, s.taskWorktreeDir(proj, taskID), resultRef)
}

func integrateResultIntoWorktree(projPath, worktreeDir, resultRef string) (bool, error) {
	if _, err := os.Stat(worktreeDir); err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_worktree_missing", "dir", worktreeDir)
	}
	if err := ValidateBranchName(resultRef); err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	g, err := worktreeExec(projPath, worktreeDir)
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	// A full ref name is never read as an option.
	ref := "refs/heads/" + resultRef
	if g.Run(worktreeDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}") != nil {
		return false, nil
	}
	return moveWorktreeOnto(g, worktreeDir, ref, resultRef, "")
}

func moveWorktreeOnto(g GitExec, worktreeDir, ref, label, upstream string) (bool, error) {
	if g.Run(worktreeDir, "merge", "--ff-only", "--no-verify", "--end-of-options", ref) == nil {
		return false, nil
	}
	missing, err := ownCommits(g, worktreeDir, ref, upstream)
	if err != nil {
		return false, err
	}
	if len(missing) > 0 {
		return false, apperr.Conflict("project.errors.integrate_diverged", "ref", label, "commits", strings.Join(missing, ", "))
	}
	// --keep refuses rather than discard uncommitted changes in the worktree.
	if err := g.Run(worktreeDir, "reset", "--keep", ref); err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_reset_failed", "ref", label)
	}
	return true, nil
}

// git cherry skips commits ref carries under another id. Commits reachable from upstream are the
// base,
// not the worktree's own work: ref may have branched from an older point of it.
func ownCommits(g GitExec, worktreeDir, ref, upstream string) ([]string, error) {
	out, err := g.Output(worktreeDir, "cherry", ref, "HEAD")
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	own, err := beyond(g, worktreeDir, ref, upstream)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(out, "\n") {
		sha, ok := strings.CutPrefix(strings.TrimSpace(line), "+ ")
		if ok && (own == nil || own[sha]) {
			missing = append(missing, shortSHA(sha))
		}
	}
	return missing, nil
}

func beyond(g GitExec, dir, ref, upstream string) (map[string]bool, error) {
	if upstream == "" || g.Run(dir, "rev-parse", "--verify", "--quiet", upstream+"^{commit}") != nil {
		return nil, nil
	}
	out, err := g.Output(dir, "rev-list", "HEAD", "--not", ref, upstream, "--")
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	own := map[string]bool{}
	for _, sha := range strings.Fields(out) {
		own[sha] = true
	}
	return own, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
