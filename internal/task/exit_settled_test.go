package task

import (
	"fmt"
	"testing"
)

func TestExitSettled(t *testing.T) {
	store := newTestStore(t)
	d := NewDispatcher(store, nil, nil, nil, nil)
	ctx := t.Context()

	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	stageRun := fmt.Sprintf("dev-task-%s-in_progress-1700000000000", tk.ID)
	mentionRun := fmt.Sprintf("dev-mention-%s-abc123-1700000000000", tk.ID)
	goneRun := "dev-task-0000000000000000-in_progress-1700000000000"

	if d.ExitSettled(ctx, stageRun) {
		t.Error("a stage run with no finish event is settled; its pod would go before Reconcile replays it")
	}
	if d.ExitSettled(ctx, mentionRun) {
		t.Error("a mention run with no finish event is settled")
	}
	if err := store.RecordEvent(ctx, tk.ID, EventAgentRunFinished, stageRun, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, tk.ID, EventMentionFinished, mentionRun, "", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{stageRun, mentionRun, goneRun, "chat-0123456789abcdef"} {
		if !d.ExitSettled(ctx, id) {
			t.Errorf("ExitSettled(%q) = false, want true", id)
		}
	}
}
