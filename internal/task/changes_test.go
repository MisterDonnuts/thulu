package task

import (
	"testing"
	"time"
)

func TestCommentsPublishATaskChange(t *testing.T) {
	store := newTestStore(t)
	bus := NewChangeBus()
	store.WithChangeBus(bus)
	tk := &Task{WorkspaceID: testWS, Title: "mention me"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ch, cancel := bus.Subscribe()
	defer cancel()

	next := func(what string) Change {
		t.Helper()
		select {
		case c := <-ch:
			return c
		case <-time.After(2 * time.Second):
			t.Fatalf("%s published no task change", what)
			return Change{}
		}
	}

	root, err := store.AddComment(t.Context(), tk.ID, SourceSystem, "", "@tasker was mentioned (instance `tasker-mention-x`).")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	c := next("a system comment")
	if c.Kind != ChangeUpdated || c.TaskID != tk.ID || c.WorkspaceID != testWS || c.BoardID != tk.BoardID || c.Stage != string(tk.Stage) {
		t.Errorf("change = %+v, want task_updated for %s in %s", c, tk.ID, testWS)
	}

	if _, err := store.AddReply(t.Context(), tk.ID, root.ID, SourceAgent, "tasker", "done"); err != nil {
		t.Fatalf("AddReply: %v", err)
	}
	if c := next("an agent's reply"); c.TaskID != tk.ID {
		t.Errorf("reply change = %+v", c)
	}

	store.touchTask(t.Context(), "no-such-task", time.Now().UTC())
	select {
	case c := <-ch:
		t.Errorf("a missing task published %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}
