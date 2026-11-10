package task

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newPushTestDispatcher(t *testing.T) (*Dispatcher, *Store) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewDispatcher(store, nil, nil, nil, nil), store
}

func newPushTestTask(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "push me"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

func TestAutoPushUsesInjectedPusher(t *testing.T) {
	d, store := newPushTestDispatcher(t)
	tk := newPushTestTask(t, store)

	var gotTask, gotProject string
	d.WithBranchPusher(func(_ context.Context, task *Task, projectID string) (bool, error) {
		gotTask, gotProject = task.ID, projectID
		return true, nil
	})
	d.pushTaskBranchForProject(t.Context(), tk, "proj-1")

	if gotTask != tk.ID || gotProject != "proj-1" {
		t.Errorf("pusher called with (%q, %q), want (%q, proj-1)", gotTask, gotProject, tk.ID)
	}
	events, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var pushed bool
	for _, e := range events {
		if e.Kind == EventBranchCreated {
			pushed = true
		}
	}
	if !pushed {
		t.Error("a successful push must record a branch event")
	}
}

func TestAutoPushNothingAheadIsQuiet(t *testing.T) {
	d, store := newPushTestDispatcher(t)
	tk := newPushTestTask(t, store)
	d.WithBranchPusher(func(context.Context, *Task, string) (bool, error) { return false, nil })

	d.pushTaskBranchForProject(t.Context(), tk, "proj-1")

	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(full.Comments) != 0 {
		t.Errorf("no-op push commented on the task: %q", full.Comments[0].Body)
	}
	events, _ := store.ListEvents(t.Context(), tk.ID)
	for _, e := range events {
		if e.Kind == EventBranchCreated {
			t.Error("no-op push recorded a branch event")
		}
	}
}

func TestAutoPushFailureCommentsOnTask(t *testing.T) {
	d, store := newPushTestDispatcher(t)
	tk := newPushTestTask(t, store)
	d.WithBranchPusher(func(context.Context, *Task, string) (bool, error) {
		return false, errors.New("could not read Username for 'https://github.com'")
	})

	d.pushTaskBranchForProject(t.Context(), tk, "proj-1")

	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(full.Comments) == 0 {
		t.Fatal("a failed auto-push must comment on the task")
	}
	body := full.Comments[len(full.Comments)-1].Body
	if !strings.Contains(body, "Auto-push of branch failed") || !strings.Contains(body, "could not read Username") {
		t.Errorf("comment doesn't carry the failure: %q", body)
	}
}
