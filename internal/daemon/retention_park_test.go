package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestParkStuckWorktrees(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	ctx := t.Context()
	db := testpg.New(t)
	dir := t.TempDir()
	projects, err := project.NewStore(db, filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	tasks, err := task.NewStore(db, filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("task store: %v", err)
	}
	proj, err := projects.Create(ctx, "Park", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tk := &task.Task{Title: "stuck", WorkspaceID: "ws-1", ProjectIDs: []string{proj.ID}}
	if err := tasks.Create(ctx, tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	path, _, err := projects.CreateTaskWorktree(ctx, proj.ID, tk.ID)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("unsaved"), 0o600); err != nil {
		t.Fatal(err)
	}
	failed := task.StatusFailed
	if _, err := tasks.Update(ctx, tk.ID, task.Patch{
		Status: &failed, WorktreePaths: map[string]string{proj.ID: path},
	}); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = $1 WHERE id = $2`,
		time.Now().UTC().AddDate(0, 0, -20), tk.ID); err != nil {
		t.Fatalf("backdate task: %v", err)
	}

	r := &retention{tasks: tasks, projects: projects, now: time.Now}
	r.parkStuckWorktrees(ctx, time.Now().AddDate(0, 0, -14))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still on disk: %v", err)
	}
	got, err := tasks.Get(ctx, tk.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if len(got.WorktreePaths) != 0 {
		t.Errorf("task still points at the parked worktree: %v", got.WorktreePaths)
	}
	comments, err := tasks.ListComments(ctx, tk.ID)
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0].Body, "committed to the task branch") {
		t.Errorf("no parking note on the task: %+v, %v", comments, err)
	}

	again, _, err := projects.CreateTaskWorktree(ctx, proj.ID, tk.ID)
	if err != nil {
		t.Fatalf("recreate worktree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(again, "wip.txt")); err != nil || string(b) != "unsaved" {
		t.Errorf("work lost: %q, %v", b, err)
	}
}
