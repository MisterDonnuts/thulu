package project

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// A dot-name, so no project directory can collide with it.
const appSourceDirName = ".apps"

// The explicit refspec updates the tracking ref even in a clone that only tracks its default
// branch.
func (s *Store) FetchBranch(ctx context.Context, projectID, branch, ghToken string) error {
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	args := append(authRewriteArgs(ghToken), "fetch", "--no-recurse-submodules", "--", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	err = gitRun(proj.Path, args...)
	if err == nil {
		return nil
	}
	return apperr.Wrap(errors.New(redactToken(err.Error(), ghToken)), "project.errors.git_fetch")
}

func (s *Store) BranchCommit(ctx context.Context, projectID, branch string) (string, error) {
	if err := ValidateBranchName(branch); err != nil {
		return "", err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return "", err
	}
	for _, ref := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if commit, rerr := resolveCommit(proj.Path, ref); rerr == nil {
			return commit, nil
		}
	}
	return "", apperr.NotFound("project.errors.branch_not_found", "branch", branch)
}

// commit must come from BranchCommit or DefaultBranchCommit.
func (s *Store) OpenCommitArchive(ctx context.Context, projectID, commit string) (io.ReadCloser, error) {
	return s.OpenDefaultBranchArchive(ctx, projectID, commit)
}

func (s *Store) ExtractCommit(ctx context.Context, projectID, commit, dest string) error {
	return s.ExtractDefaultBranch(ctx, projectID, commit, dest)
}

func (s *Store) AppSourceDir(name string) (string, error) {
	if err := validateTaskID(name); err != nil || s.baseDir == "" {
		return "", apperr.Invalid("project.errors.app_source_dir", "id", name)
	}
	return filepath.Join(s.baseDir, appSourceDirName, name), nil
}

func (s *Store) AppSourceNames() []string {
	if s.baseDir == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(s.baseDir, appSourceDirName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && validateTaskID(e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	return out
}
