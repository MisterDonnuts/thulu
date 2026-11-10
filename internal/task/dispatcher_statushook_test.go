package task

import (
	"path/filepath"
	"testing"
)

func TestStatusHookSeesTheUpdatedTask(t *testing.T) {
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	d := NewDispatcher(store, nil, nil, nil, nil)

	tk := &Task{WorkspaceID: testWS, Title: "will fail"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}

	var seen []RunStatus
	var seenID, seenTitle string
	d.WithStatusHook(func(got *Task, status RunStatus) {
		seen = append(seen, status)
		seenID, seenTitle = got.ID, got.Title
		if got.Status != status {
			t.Errorf("hook got a task at status %s while being told %s — that is the pre-write copy",
				got.Status, status)
		}
	})

	d.setStatus(t.Context(), tk.ID, StatusFailed)

	if len(seen) != 1 || seen[0] != StatusFailed {
		t.Fatalf("hook calls = %v, want one %s", seen, StatusFailed)
	}
	if seenID != tk.ID || seenTitle != "will fail" {
		t.Errorf("hook got task %s/%q, want %s/%q", seenID, seenTitle, tk.ID, "will fail")
	}
}

func TestStatusWithoutAHookIsFine(t *testing.T) {
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tk := &Task{WorkspaceID: testWS, Title: "nobody listening"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	NewDispatcher(store, nil, nil, nil, nil).setStatus(t.Context(), tk.ID, StatusIdle)

	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusIdle {
		t.Errorf("status = %s, want %s", got.Status, StatusIdle)
	}
}
