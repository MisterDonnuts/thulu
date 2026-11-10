package task

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestMessagesRoundTripAndStayOffTheCommentThread(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := store.AddMessage(t.Context(), &Message{
		TaskID: tk.ID, Role: RoleUser, Author: "you", Body: "change the parser",
	})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if first.Stage != types.StageInProgress {
		t.Errorf("stage = %q, want the task's stage", first.Stage)
	}
	if _, err := store.AddMessage(t.Context(), &Message{
		TaskID: tk.ID, Role: RoleSystem, Author: "octopus", Body: "retomando",
	}); err != nil {
		t.Fatalf("AddMessage system: %v", err)
	}

	got, err := store.ListMessages(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("messages = %d, want 2", len(got))
	}
	if got[0].Body != "change the parser" || got[1].Role != RoleSystem {
		t.Errorf("wrong order or roles: %+v", got)
	}

	comments, err := store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %d, want the thread untouched", len(comments))
	}
}

func TestUpdateMessageDeliveryStampsTheRow(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := store.AddMessage(t.Context(), &Message{TaskID: tk.ID, Role: RoleUser, Body: "hola"})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := store.UpdateMessageDelivery(t.Context(), m.ID, "chat", "reviewer", "", "chat-9"); err != nil {
		t.Fatalf("UpdateMessageDelivery: %v", err)
	}
	got, err := store.GetMessage(t.Context(), tk.ID, m.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Delivery != "chat" || got.Agent != "reviewer" || got.ChatID != "chat-9" {
		t.Errorf("row = %+v, want the chat delivery stamped", got)
	}
}

func TestGetMessageIsScopedToItsTask(t *testing.T) {
	store := newTestStore(t)
	mine := &Task{WorkspaceID: testWS, Title: "mine"}
	theirs := &Task{WorkspaceID: testWS, Title: "theirs"}
	for _, tk := range []*Task{mine, theirs} {
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	m, err := store.AddMessage(t.Context(), &Message{TaskID: theirs.ID, Role: RoleUser, Body: "secreto"})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := store.GetMessage(t.Context(), mine.ID, m.ID); err == nil {
		t.Fatal("read another task's message")
	}
}
