package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestParkTaskWorktree_KeepsWorkAndRecreates(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "Park", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path, branch, err := store.CreateTaskWorktree(t.Context(), proj.ID, "park1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("unsaved"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := store.ParkTaskWorktree(t.Context(), proj.ID, "park1"); err != nil {
		t.Fatalf("ParkTaskWorktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still there after parking: %v", err)
	}
	if !branchExists(proj.Path, branch) {
		t.Fatalf("branch %s deleted by parking", branch)
	}
	if wts, _ := store.ListWorktrees(t.Context(), proj.ID); len(wts) != 0 {
		t.Errorf("git still lists the parked worktree: %+v", wts)
	}

	again, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "park1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree after parking: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(again, "wip.txt"))
	if err != nil || string(got) != "unsaved" {
		t.Fatalf("work lost across park: %q, %v", got, err)
	}

	if err := store.ParkTaskWorktree(t.Context(), proj.ID, "never-created"); err != nil {
		t.Errorf("park of a missing worktree: %v", err)
	}
}
