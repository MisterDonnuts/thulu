package project

import (
	"context"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (s *Store) PushBranch(ctx context.Context, projectID, taskID string) (PushResult, error) {
	return s.PushBranchAuth(ctx, projectID, taskID, "")
}

// ghToken travels only as a -c url.insteadOf argument; it must never reach the repo's git config or
// the rebase env.
func (s *Store) PushBranchAuth(ctx context.Context, projectID, taskID, ghToken string) (PushResult, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return PushResult{}, err
	}
	return w.Push(ghToken)
}

type PushResult struct {
	Rebased bool
}

// Returned after the rebase was aborted: the worktree is already back to its pre-rebase state.
type RebaseConflictError struct {
	Base  string
	Files []string
}

func (e *RebaseConflictError) Error() string {
	return i18n.Log("project.errors.rebase_conflict",
		"base", e.Base, "count", len(e.Files), "files", strings.Join(e.Files, ", "))
}

type BranchStatus struct {
	Branch      string `json:"branch"`
	Base        string `json:"base"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	UpToDate    bool   `json:"up_to_date"`
	NeedsRebase bool   `json:"needs_rebase"`
}

func (s *Store) GetBranchStatus(ctx context.Context, projectID, taskID string) (BranchStatus, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return BranchStatus{}, err
	}
	return w.Status("")
}

func (s *Store) BranchHasCommitsAhead(ctx context.Context, projectID, taskID string) (bool, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return false, err
	}
	return w.HasCommitsAhead()
}

func parseCount(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
