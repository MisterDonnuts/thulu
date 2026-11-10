package chat_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func TestChat_VisibleTo(t *testing.T) {
	ana := chat.Viewer{Owner: "user:ana", Author: "ana@x.com"}
	bob := chat.Viewer{Owner: "user:bob", Author: "bob@x.com"}
	for _, tc := range []struct {
		name string
		c    chat.Chat
		v    chat.Viewer
		want bool
	}{
		{"owner", chat.Chat{ID: "c1", Owner: "user:ana", CreatedBy: "ana@x.com"}, ana, true},
		{"someone else", chat.Chat{ID: "c1", Owner: "user:ana", CreatedBy: "ana@x.com"}, bob, false},
		{"owner wins over a matching author", chat.Chat{ID: "c1", Owner: "user:ana", CreatedBy: "bob@x.com"}, bob, false},
		{"a person's chat on a task", chat.Chat{ID: "c1", TaskID: "t1", Owner: "user:ana", CreatedBy: "ana@x.com"}, bob, false},
		{"the daemon's mention chat", chat.Chat{ID: "c1", TaskID: "t1", ThreadID: "th1", CreatedBy: chat.SystemCreator}, bob, true},
		{"legacy row, its author", chat.Chat{ID: "c1", CreatedBy: "ana@x.com"}, ana, true},
		{"legacy row, someone else", chat.Chat{ID: "c1", CreatedBy: "ana@x.com"}, bob, false},
		{"row with no author", chat.Chat{ID: "c1"}, bob, true},
		{"a viewer with no identity", chat.Chat{ID: "c1", Owner: "user:ana"}, chat.Viewer{}, false},
		{"run key on its chat", chat.Chat{ID: "c1", Owner: "user:ana"}, chat.Viewer{RunChatID: "c1"}, true},
		{"run key on another chat", chat.Chat{ID: "c1", Owner: "user:ana"}, chat.Viewer{RunChatID: "c2"}, false},
	} {
		if got := tc.c.VisibleTo(tc.v); got != tc.want {
			t.Errorf("%s: VisibleTo = %v, want %v", tc.name, got, tc.want)
		}
		ev := chat.Event{Type: chat.EventMessageAdded}.For(&tc.c)
		if got := ev.VisibleTo(tc.v); got != tc.want {
			t.Errorf("%s: event VisibleTo = %v, want %v", tc.name, got, tc.want)
		}
		wantListed := tc.want && tc.c.CreatedBy != chat.SystemCreator
		if got := ev.Listed(tc.v); got != wantListed || tc.c.Listed(tc.v) != wantListed {
			t.Errorf("%s: Listed = %v (event %v), want %v", tc.name, tc.c.Listed(tc.v), got, wantListed)
		}
	}
	if (chat.Event{Type: chat.EventMessageAdded, ChatID: "c1", WorkspaceID: "ws"}).VisibleTo(ana) {
		t.Error("an event published without For reached a subscriber")
	}
}

func TestChat_ListListedMatchesListed(t *testing.T) {
	store := newStore(t)
	chats := []*chat.Chat{
		{WorkspaceID: "ws-1", AgentType: "m", Title: "ana", Owner: "user:ana", CreatedBy: "ana@x.com"},
		{WorkspaceID: "ws-1", AgentType: "m", Title: "bob", Owner: "user:bob", CreatedBy: "bob@x.com"},
		{WorkspaceID: "ws-1", AgentType: "m", Title: "bob on a task", TaskID: "t1", Owner: "user:bob", CreatedBy: "bob@x.com"},
		{WorkspaceID: "ws-1", AgentType: "m", Title: "mention", TaskID: "t1", CreatedBy: chat.SystemCreator},
		{WorkspaceID: "ws-1", AgentType: "m", Title: "legacy ana", CreatedBy: "ana@x.com"},
		{WorkspaceID: "ws-1", AgentType: "m", Title: "legacy none"},
		{WorkspaceID: "ws-2", AgentType: "m", Title: "elsewhere", Owner: "user:ana"},
	}
	for _, c := range chats {
		mustCreate(t, store, c)
	}
	for _, v := range []chat.Viewer{
		{Owner: "user:ana", Author: "ana@x.com"},
		{Owner: "user:bob", Author: "bob@x.com"},
		{Owner: "user:ana"},
		{},
	} {
		got, err := store.ListListed(t.Context(), "ws-1", v, false)
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, c := range got {
			listed[c.ID] = true
		}
		for _, c := range chats[:6] {
			if want := c.Listed(v); listed[c.ID] != want {
				t.Errorf("viewer %+v, chat %q: listed = %v, Listed = %v", v, c.Title, listed[c.ID], want)
			}
		}
		if listed[chats[6].ID] {
			t.Errorf("viewer %+v listed a chat from another workspace", v)
		}
	}
}
