package project

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The agent's clone is the source of truth: a rebased result resets the worktree onto it.
// Rewritten (branch no longer contains its old tip) is what tells Publish to force-with-lease.
// Without the result branch there is nothing to integrate.
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
	if err := ValidateBranchName(resultRef); err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	// A full ref name is never read as an option.
	ref := "refs/heads/" + resultRef
	tip, err := gitOutput(projPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return false, nil
	}
	tip = strings.TrimSpace(tip)
	if _, err = os.Stat(worktreeDir); err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_worktree_missing", "dir", worktreeDir)
	}
	g, err := worktreeExec(projPath, worktreeDir)
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	rewritten, err := moveWorktreeOnto(g, worktreeDir, tip, resultRef, "")
	if err != nil {
		return false, err
	}
	// Integrated, so a later run that pushes nothing cannot bring this result back over newer work.
	// Only this tip goes: a push that landed meanwhile stays for the next integration.
	logx.Warn(gitRun(projPath, "update-ref", "-d", ref, tip), "project.log.delete_result_branch", "branch", resultRef)
	return rewritten, nil
}

// Under refs/octopus/, so no branch listing or task-branch fetch picks a parked result up.
func parkedResultPrefix(resultRef string) string { return "refs/octopus/parked/" + resultRef + "/" }

// ParkAgentResult moves a result that does not fit the task branch out of the way, so the runs after
// it integrate and push again. The work stays at the returned ref until the task's worktree goes.
// Without the result branch there is nothing to park.
func (s *Store) ParkAgentResult(ctx context.Context, projectID, resultRef string) (string, error) {
	if err := ValidateBranchName(resultRef); err != nil {
		return "", apperr.Wrap(err, "project.errors.integrate_failed")
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return "", err
	}
	ref := "refs/heads/" + resultRef
	tip, err := gitOutput(proj.Path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", nil
	}
	tip = strings.TrimSpace(tip)
	parked := parkedResultPrefix(resultRef) + shortSHA(tip)
	if err := gitRun(proj.Path, "update-ref", parked, tip); err != nil {
		return "", apperr.Wrap(err, "project.errors.park_result", "ref", parked)
	}
	if err := gitRun(proj.Path, "update-ref", "-d", ref, tip); err != nil {
		return "", apperr.Wrap(err, "project.errors.park_result", "ref", parked)
	}
	return parked, nil
}

func dropParkedResults(projPath, resultRef string) {
	out, err := gitOutput(projPath, "for-each-ref", "--format=%(refname)", parkedResultPrefix(resultRef))
	for _, ref := range strings.Fields(out) {
		err = errors.Join(err, gitRun(projPath, "update-ref", "-d", ref))
	}
	logx.Warn(err, "project.log.delete_result_branch", "branch", resultRef)
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
