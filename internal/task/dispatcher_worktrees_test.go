package task

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func TestWorktreeFailureNamesTheProject(t *testing.T) {
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	if err := projects.ImportProject(t.Context(), project.Project{
		ID: "p1conduit0000001", Name: "conduit-api", Path: filepath.Join(t.TempDir(), "missing"),
		RepoURL: "https://github.com/acme/conduit-api", DefaultBranch: "main", WorkspaceID: testWS,
	}); err != nil {
		t.Fatalf("import project: %v", err)
	}
	d := NewDispatcher(store, nil, projects, nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "worktree", ProjectIDs: []string{"p1conduit0000001"}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}

	d.ensureWorktrees(t.Context(), tk)

	got, err := store.Get(i18n.WithLocale(t.Context(), "es"), tk.ID)
	if err != nil || len(got.Comments) == 0 {
		t.Fatalf("no note: %v", err)
	}
	body := got.Comments[len(got.Comments)-1].Body
	if !strings.Contains(body, "conduit-api") || strings.Contains(body, "p1conduit0000001") {
		t.Errorf("note = %q, want the project's name and not its id", body)
	}
	if !strings.Contains(body, i18n.T("es", "project.errors.clone_missing", "name", "conduit-api")) {
		t.Errorf("note = %q, want the missing clone in words", body)
	}
}
