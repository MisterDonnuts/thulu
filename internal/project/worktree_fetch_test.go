package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestCreateTaskWorktreeAuth_FetchesLatestDefault(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "FetchAuth", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	advanceOriginMain(t, bare, "landed on main after the clone")

	wt, err := store.CreateTaskWorktreeAuth(t.Context(), proj.ID, "fa-task", "tok-secret-123")
	if err != nil {
		t.Fatalf("CreateTaskWorktreeAuth: %v", err)
	}
	if wt.StaleBase != nil {
		t.Errorf("StaleBase = %v, want nil after a successful fetch", wt.StaleBase)
	}
	if wt.Branch != taskBranchName("fa-task") {
		t.Errorf("Branch = %q", wt.Branch)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "advance.txt")); err != nil {
		t.Errorf("worktree did not branch off the fetched main: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(proj.Path, ".git", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(cfg), "tok-secret-123") {
		t.Error("token persisted to .git/config")
	}
}

func TestCreateTaskWorktreeAuth_StaleBaseWhenFetchFails(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "FetchStale", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base, err := gitOutput(proj.Path, "rev-parse", "origin/"+proj.DefaultBranch)
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if err := os.RemoveAll(bare); err != nil {
		t.Fatalf("remove remote: %v", err)
	}

	wt, err := store.CreateTaskWorktreeAuth(t.Context(), proj.ID, "st-task", "tok-secret-456")
	if err != nil {
		t.Fatalf("CreateTaskWorktreeAuth: %v", err)
	}
	if wt.StaleBase == nil {
		t.Fatal("StaleBase = nil, want the fetch error")
	}
	if strings.Contains(wt.StaleBase.Error(), "tok-secret-456") {
		t.Errorf("token leaked into the error: %v", wt.StaleBase)
	}
	if wt.BaseCommit == "" || !strings.HasPrefix(strings.TrimSpace(base), wt.BaseCommit) {
		t.Errorf("BaseCommit = %q, want a prefix of %q", wt.BaseCommit, strings.TrimSpace(base))
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Errorf("worktree not created: %v", err)
	}

	tokenless, err := store.CreateTaskWorktreeAuth(t.Context(), proj.ID, "st-task2", "")
	if err != nil {
		t.Fatalf("CreateTaskWorktreeAuth tokenless: %v", err)
	}
	if tokenless.StaleBase != nil {
		t.Errorf("tokenless StaleBase = %v, want nil (log only, as before)", tokenless.StaleBase)
	}
}
