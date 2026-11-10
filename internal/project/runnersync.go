package project

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func remoteBranchRef(branch string) string { return "refs/remotes/origin/" + branch }

func fetchRemoteBranch(proj *Project, branch, ghToken string) (bool, error) {
	if err := ValidateBranchName(branch); err != nil {
		return false, err
	}
	// ls-remote first: telling a missing branch from a failed fetch would mean parsing git's
	// localized output.
	args := append(authRewriteArgs(ghToken), "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	out, err := gitOutput(proj.Path, args...)
	if err != nil {
		return false, apperr.Wrap(errors.New(redactToken(err.Error(), ghToken)), "project.errors.git_fetch")
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	args = append(authRewriteArgs(ghToken), "fetch", "--no-recurse-submodules", "--", "origin",
		"+refs/heads/"+branch+":"+remoteBranchRef(branch))
	if err := gitRun(proj.Path, args...); err != nil {
		return false, apperr.Wrap(errors.New(redactToken(err.Error(), ghToken)), "project.errors.git_fetch")
	}
	return true, nil
}

func (s *Store) SyncRunnerBranch(ctx context.Context, projectID, taskID, ghToken string) (bool, error) {
	if err := validateTaskID(taskID); err != nil {
		return false, err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return false, err
	}
	if rerr := proj.ready(); rerr != nil {
		return false, rerr
	}
	worktreeDir := s.taskWorktreeDir(proj, taskID)
	if !dirExists(worktreeDir) {
		return false, nil
	}
	branch := taskBranchName(taskID)
	found, err := fetchRemoteBranch(proj, branch, ghToken)
	if err != nil || !found {
		return false, err
	}
	g, err := worktreeExec(proj.Path, worktreeDir)
	if err != nil {
		return true, apperr.Wrap(err, "project.errors.integrate_failed")
	}
	upstream := ""
	if ValidateBranchName(proj.DefaultBranch) == nil {
		upstream = remoteBranchRef(proj.DefaultBranch)
	}
	_, err = moveWorktreeOnto(g, worktreeDir, remoteBranchRef(branch), "origin/"+branch, upstream)
	return true, err
}

func (s *Store) CreateRunnerTaskWorktree(ctx context.Context, projectID, taskID, ghToken string) (TaskWorktree, error) {
	if err := validateTaskID(taskID); err != nil {
		return TaskWorktree{}, err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return TaskWorktree{}, err
	}
	if err := proj.ready(); err != nil {
		return TaskWorktree{}, err
	}
	branch := taskBranchName(taskID)
	if !dirExists(s.taskWorktreeDir(proj, taskID)) && !branchExists(proj.Path, branch) {
		found, err := fetchRemoteBranch(proj, branch, ghToken)
		// Error ignored on purpose: we fall back to the default branch, whose own fetch reports the
		// remote problem.
		logx.Warn(err, "project.log.fetch_origin", "branch", branch, "path", proj.Path)
		if found {
			return s.addWorktree(proj, taskID, remoteBranchRef(branch), nil)
		}
	}
	return s.CreateTaskWorktreeAuth(ctx, projectID, taskID, ghToken)
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
