package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A runner never hears that a task ended or was deleted; it removes a task's worktree once the
// daemon stops naming the branch here. A branch stays open while any task of its group is outside a
// terminal stage, and the answer covers only this runner's workspace.
func TestRunnerWorktreesListsOpenTaskBranches(t *testing.T) {
	db := testpg.New(t)
	dir := t.TempDir()
	ctx := context.Background()
	store, err := runner.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewStore(db, filepath.Join(dir, "att"))
	if err != nil {
		t.Fatal(err)
	}
	register := func(ws string) *runner.RegisterResponse {
		tok, _, err := store.CreateRegistration(ctx, ws, "laptop", "")
		if err != nil {
			t.Fatal(err)
		}
		reg, err := store.Register(ctx, runner.RegisterRequest{Token: tok})
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	mine, other, elsewhere := register("ws1"), register("ws1"), register("ws2")
	app, err := projects.CreateOnRunner(ctx, "app", "", mine.RunnerID, project.CreateOpts{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := projects.CreateOnRunner(ctx, "theirs", "", other.RunnerID, project.CreateOpts{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := projects.CreateOnRunner(ctx, "foreign", "", elsewhere.RunnerID, project.CreateOpts{WorkspaceID: "ws2"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(tk *task.Task, stage types.Stage) *task.Task {
		t.Helper()
		if err := tasks.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if stage != "" {
			if _, err := tasks.Update(ctx, tk.ID, task.Patch{Stage: &stage}); err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	open := create(&task.Task{WorkspaceID: "ws1", Title: "open", ProjectIDs: []string{app.ID}}, "")
	done := create(&task.Task{WorkspaceID: "ws1", Title: "done", ProjectIDs: []string{app.ID}}, types.StageDone)
	parent := create(&task.Task{WorkspaceID: "ws1", Title: "parent", ProjectIDs: []string{app.ID}}, types.StageDone)
	create(&task.Task{WorkspaceID: "ws1", Title: "child", ParentID: parent.ID, ProjectIDs: []string{app.ID}}, "")
	create(&task.Task{WorkspaceID: "ws1", Title: "theirs", ProjectIDs: []string{theirs.ID}}, "")
	create(&task.Task{WorkspaceID: "ws2", Title: "foreign", ProjectIDs: []string{foreign.ID}}, "")

	srv := &Server{runnerStore: store, projectStore: projects, taskStore: tasks}
	rn, err := store.Authenticate(ctx, mine.Credential, "test", runner.ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/runner/v1/worktrees", nil)
	r = r.WithContext(context.WithValue(r.Context(), runnerCtxKey{}, rn))
	w := httptest.NewRecorder()
	srv.handleRunnerWorktrees(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("worktrees = %d: %s", w.Code, w.Body.String())
	}
	var got runner.WorktreesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{project.TaskBranchName(open.ID), project.TaskBranchName(parent.ID)}
	slices.Sort(want)
	if !slices.Equal(got.Projects[app.ID], want) {
		t.Errorf("open branches of app = %v, want %v (not %s, whose task is done)", got.Projects[app.ID], want,
			project.TaskBranchName(done.ID))
	}
	if b, ok := got.Projects[theirs.ID]; !ok || len(b) != 0 {
		t.Errorf("another runner's project = %v, %v; want listed with no branch, so the clone stays", b, ok)
	}
	if _, ok := got.Projects[foreign.ID]; ok {
		t.Error("a project of another workspace is listed")
	}
}
