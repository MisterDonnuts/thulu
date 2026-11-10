package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// Regression: a project named ".worktrees" cloned into the store's own dir, so other projects'
// worktrees landed in that tenant's checkout and its delete removed them.
func TestCreateRefusesReservedProjectNames(t *testing.T) {
	store := newTestStore(t)
	for _, d := range []string{worktreesDirName, projectStageDirName} {
		if fi, err := os.Stat(filepath.Join(store.baseDir, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not created at boot: %v", d, err)
		}
	}

	repo := testgit.BareRepo(t)
	for _, name := range []string{".worktrees", ".project-stage", ".hidden", "..", " .worktrees"} {
		_, err := store.Create(t.Context(), name, repo, CreateOpts{WorkspaceID: testWS})
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Key != "project.errors.name_reserved" {
			t.Errorf("Create(%q) = %v, want project.errors.name_reserved", name, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(store.baseDir, worktreesDirName)); len(entries) != 0 {
		t.Errorf("a refused name still wrote into .worktrees: %d entries", len(entries))
	}

	if _, err := store.Create(t.Context(), "my.repo", repo, CreateOpts{WorkspaceID: testWS}); err != nil {
		t.Errorf("Create(\"my.repo\") = %v, want success", err)
	}
}

func TestImportRefusesReservedProjectPath(t *testing.T) {
	store := newTestStore(t)
	err := store.ImportProject(t.Context(), Project{
		ID: "p-reserved", Name: "x", Path: filepath.Join(store.baseDir, worktreesDirName),
		RepoURL: "https://example.com/x.git", DefaultBranch: "main", WorkspaceID: testWS,
	})
	if err == nil {
		t.Fatal("ImportProject accepted a path inside the store's .worktrees directory")
	}
}

// Regression: workspace B's "app--x" worktrees start with "app--", so deleting workspace A's "app"
// removed them.
func TestDeleteKeepsWorktreesOfAProjectSharingTheNamePrefix(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	mine, err := store.Create(t.Context(), "app", repo, CreateOpts{WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	theirs, err := store.Create(t.Context(), "app--x", repo, CreateOpts{WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatalf("Create app--x: %v", err)
	}
	minePath, _, err := store.CreateTaskWorktree(t.Context(), mine.ID, "aaa111")
	if err != nil {
		t.Fatalf("worktree for app: %v", err)
	}
	theirPath, _, err := store.CreateTaskWorktree(t.Context(), theirs.ID, "bbb222")
	if err != nil {
		t.Fatalf("worktree for app--x: %v", err)
	}

	if err := store.Delete(t.Context(), mine.ID); err != nil {
		t.Fatalf("Delete app: %v", err)
	}
	if _, err := os.Stat(minePath); !os.IsNotExist(err) {
		t.Errorf("app's own worktree survived its delete: %v", err)
	}
	if _, err := os.Stat(theirPath); err != nil {
		t.Errorf("deleting app removed app--x's worktree %s: %v", theirPath, err)
	}
}

func TestWorktreeOf(t *testing.T) {
	for entry, want := range map[string]string{
		"app--task-aaa111":           "app",
		"app--x--task-bbb222":        "app--x",
		"app--task-zzz--task-ccc333": "app--task-zzz",
		"app":                        "",
		"--task-aaa":                 "",
	} {
		if got := worktreeOf(entry); got != want {
			t.Errorf("worktreeOf(%q) = %q, want %q", entry, got, want)
		}
	}
}
