package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestWorktreeTouchesWorkflows(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "Workflows", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "wf1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	wt, err := store.HostWorktreeGit(t.Context(), proj.ID, "wf1")
	if err != nil {
		t.Fatalf("HostWorktreeGit: %v", err)
	}
	touches := func(step string, want bool) {
		t.Helper()
		got, err := wt.TouchesWorkflows()
		if err != nil {
			t.Fatalf("%s: TouchesWorkflows: %v", step, err)
		}
		if got != want {
			t.Errorf("%s: TouchesWorkflows = %v, want %v", step, got, want)
		}
	}
	commit := func(msg string) {
		t.Helper()
		gitRunT(t, wtPath, "add", "-A")
		gitRunT(t, wtPath, "commit", "-q", "-m", msg)
	}

	touches("fresh branch", false)

	writeF(t, filepath.Join(wtPath, "main.go"), "package main\n")
	commit("code only")
	touches("code-only commit", false)

	if err := os.MkdirAll(filepath.Join(wtPath, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wtPath, ".github", "workflows", "ci.yml"), "on: push\n")
	commit("add a workflow")
	touches("workflow added", true)

	if _, err := wt.Push(""); err != nil {
		t.Fatalf("Push: %v", err)
	}
	touches("after push", false)

	gitRunT(t, wtPath, "rm", "-q", ".github/workflows/ci.yml")
	commit("drop the workflow")
	touches("workflow deleted", true)
}
