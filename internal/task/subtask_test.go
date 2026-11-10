package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Regression: on a pre-parent_id database the schema block runs before migrations, so it must not
// index parent_id.
// Migration 13 indexes it after migration 12 adds the column; a fresh DB can't catch this.
func TestNewStore_UpgradeFromPreParentID(t *testing.T) {
	db := testpg.New(t)
	if _, err := db.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		project_ids TEXT NOT NULL DEFAULT '[]',
		title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '',
		stage TEXT NOT NULL,
		stage_assignments TEXT NOT NULL DEFAULT '{}',
		branch_name TEXT NOT NULL DEFAULT '',
		worktree_paths TEXT NOT NULL DEFAULT '{}',
		pr_urls TEXT NOT NULL DEFAULT '{}',
		current_run_id TEXT NOT NULL DEFAULT '',
		current_instance TEXT NOT NULL DEFAULT '',
		tags TEXT NOT NULL DEFAULT '[]',
		priority TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatalf("seed pre-parent_id tasks table: %v", err)
	}

	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore upgrade from pre-parent_id DB: %v", err)
	}

	parent := &Task{WorkspaceID: testWS, Title: "p"}
	if err := store.Create(t.Context(), parent); err != nil {
		t.Fatalf("create after upgrade: %v", err)
	}
	sub := &Task{WorkspaceID: testWS, Title: "c", ParentID: parent.ID}
	if err := store.Create(t.Context(), sub); err != nil {
		t.Fatalf("create subtask after upgrade: %v", err)
	}
	kids, err := store.ListChildren(t.Context(), parent.ID)
	if err != nil || len(kids) != 1 {
		t.Fatalf("ListChildren after upgrade = (%v, %v), want 1 child", kids, err)
	}
}

type fakeReleaser struct{ released []string }

func (f *fakeReleaser) Release(_ context.Context, taskID string) error {
	f.released = append(f.released, taskID)
	return nil
}

func TestSandboxRootID(t *testing.T) {
	top := &Task{WorkspaceID: testWS, ID: "parent-1"}
	if top.IsSubtask() {
		t.Error("top-level task reported as subtask")
	}
	if got := top.SandboxRootID(); got != "parent-1" {
		t.Errorf("top-level SandboxRootID = %q want parent-1", got)
	}

	sub := &Task{WorkspaceID: testWS, ID: "child-1", ParentID: "parent-1"}
	if !sub.IsSubtask() {
		t.Error("subtask not reported as subtask")
	}
	if got := sub.SandboxRootID(); got != "parent-1" {
		t.Errorf("subtask SandboxRootID = %q want parent-1 (the root)", got)
	}
}

func TestParentIDRoundTrip(t *testing.T) {
	store := newTestStore(t)
	parent := &Task{WorkspaceID: testWS, Title: "feature"}
	if err := store.Create(t.Context(), parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	sub := &Task{WorkspaceID: testWS, Title: "dev slice", ParentID: parent.ID}
	if err := store.Create(t.Context(), sub); err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	got, err := store.Get(t.Context(), sub.ID)
	if err != nil {
		t.Fatalf("get subtask: %v", err)
	}
	if got.ParentID != parent.ID {
		t.Errorf("Get ParentID = %q want %q", got.ParentID, parent.ID)
	}

	all, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var seen bool
	for _, tk := range all {
		if tk.ID == sub.ID {
			seen = true
			if tk.ParentID != parent.ID {
				t.Errorf("List ParentID = %q want %q", tk.ParentID, parent.ID)
			}
		}
	}
	if !seen {
		t.Error("subtask missing from List()")
	}
}

func TestListChildren(t *testing.T) {
	store := newTestStore(t)
	parent := &Task{WorkspaceID: testWS, Title: "parent"}
	store.Create(t.Context(), parent)
	other := &Task{WorkspaceID: testWS, Title: "unrelated"}
	store.Create(t.Context(), other)

	a := &Task{WorkspaceID: testWS, Title: "a", ParentID: parent.ID}
	store.Create(t.Context(), a)
	b := &Task{WorkspaceID: testWS, Title: "b", ParentID: parent.ID}
	store.Create(t.Context(), b)

	children, err := store.ListChildren(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("ListChildren len = %d want 2: %+v", len(children), children)
	}
	if children[0].ID != a.ID || children[1].ID != b.ID {
		t.Errorf("ListChildren order = [%s,%s] want [%s,%s]", children[0].ID, children[1].ID, a.ID, b.ID)
	}

	none, err := store.ListChildren(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("ListChildren(other): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListChildren(other) = %d want 0", len(none))
	}
}

func TestAllInGroupDone(t *testing.T) {
	store := newTestStore(t)
	parent := &Task{WorkspaceID: testWS, Title: "parent"}
	store.Create(t.Context(), parent)
	a := &Task{WorkspaceID: testWS, Title: "a", ParentID: parent.ID}
	store.Create(t.Context(), a)
	b := &Task{WorkspaceID: testWS, Title: "b", ParentID: parent.ID}
	store.Create(t.Context(), b)

	mustDone := func(id string) {
		done := types.StageDone
		if _, err := store.Update(t.Context(), id, Patch{Stage: &done}); err != nil {
			t.Fatalf("mark done %s: %v", id, err)
		}
	}
	assertGroup := func(want bool) {
		t.Helper()
		got, err := store.AllInGroupDone(t.Context(), parent.ID)
		if err != nil {
			t.Fatalf("AllInGroupDone: %v", err)
		}
		if got != want {
			t.Errorf("AllInGroupDone = %v want %v", got, want)
		}
	}

	assertGroup(false)
	mustDone(a.ID)
	assertGroup(false)
	mustDone(b.ID)
	assertGroup(false)
	mustDone(parent.ID)
	assertGroup(true)
}

func TestAllInGroupDone_TopLevelNoChildren(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "solo"}
	store.Create(t.Context(), tk)

	if done, _ := store.AllInGroupDone(t.Context(), tk.ID); done {
		t.Error("AllInGroupDone true before task is Done")
	}
	doneStage := types.StageDone
	store.Update(t.Context(), tk.ID, Patch{Stage: &doneStage})
	if done, _ := store.AllInGroupDone(t.Context(), tk.ID); !done {
		t.Error("AllInGroupDone false after lone task reached Done")
	}
}

func TestCleanupDoneTask_RefCounted(t *testing.T) {
	store := newTestStore(t)
	rel := &fakeReleaser{}
	d := NewDispatcher(store, nil, nil, nil, rel)

	parent := &Task{WorkspaceID: testWS, Title: "feature"}
	store.Create(t.Context(), parent)
	a := &Task{WorkspaceID: testWS, Title: "dev", ParentID: parent.ID}
	store.Create(t.Context(), a)
	b := &Task{WorkspaceID: testWS, Title: "ui-test", ParentID: parent.ID}
	store.Create(t.Context(), b)

	ctx := context.Background()
	done := types.StageDone

	store.Update(ctx, a.ID, Patch{Stage: &done})
	aDone, _ := store.Get(ctx, a.ID)
	d.cleanupDoneTask(ctx, aDone)
	if len(rel.released) != 0 {
		t.Fatalf("released %v after only one subtask done; want none", rel.released)
	}

	store.Update(ctx, parent.ID, Patch{Stage: &done})
	pDone, _ := store.Get(ctx, parent.ID)
	d.cleanupDoneTask(ctx, pDone)
	if len(rel.released) != 0 {
		t.Fatalf("released %v while a subtask still open; want none", rel.released)
	}

	store.Update(ctx, b.ID, Patch{Stage: &done})
	bDone, _ := store.Get(ctx, b.ID)
	d.cleanupDoneTask(ctx, bDone)
	if len(rel.released) != 1 || rel.released[0] != parent.ID {
		t.Fatalf("released %v; want exactly [%s] (the root)", rel.released, parent.ID)
	}
}
