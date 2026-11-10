package chat_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

func newStore(t *testing.T) *chat.Store {
	t.Helper()
	store, err := chat.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustCreate(t *testing.T, s *chat.Store, c *chat.Chat) *chat.Chat {
	t.Helper()
	if err := s.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

func TestChat_CreateGetList(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{
		WorkspaceID: "ws-1", AgentType: "manager", Title: "Plan", CreatedBy: "user@x.com",
	})
	if c.ID == "" {
		t.Fatal("empty chat ID")
	}

	got, err := store.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.AgentType != "manager" || got.WorkspaceID != "ws-1" || got.Title != "Plan" {
		t.Errorf("GetChat mismatch: %+v", got)
	}

	if _, err := store.GetChat(t.Context(), "nope"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-2", AgentType: "developer"})

	all, err := store.ListChats(t.Context(), "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(all))
	}

	ws1, err := store.ListChats(t.Context(), "ws-1", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws1) != 1 || ws1[0].ID != c.ID {
		t.Errorf("workspace filter failed: %+v", ws1)
	}

	byAgent, err := store.ListChats(t.Context(), "", "", "developer", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].AgentType != "developer" {
		t.Errorf("agent filter failed: %+v", byAgent)
	}
}

func TestChat_TaskLinkFilter(t *testing.T) {
	store := newStore(t)

	linked := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "developer", TaskID: "t-9"})
	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "developer"})

	got, err := store.ListChats(t.Context(), "", "t-9", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != linked.ID {
		t.Errorf("task filter failed: %+v", got)
	}
}

func TestChat_ArchiveRenameDelete(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager", Title: "old"})

	if err := store.RenameChat(t.Context(), c.ID, "new title"); err != nil {
		t.Fatalf("RenameChat: %v", err)
	}
	if err := store.SetArchived(t.Context(), c.ID, true); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}

	got, _ := store.GetChat(t.Context(), c.ID)
	if got.Title != "new title" || !got.Archived() {
		t.Errorf("after rename+archive: %+v", got)
	}

	if l, _ := store.ListChats(t.Context(), "ws-1", "", "", false); len(l) != 0 {
		t.Errorf("archived chat should be hidden, got %d", len(l))
	}
	if l, _ := store.ListChats(t.Context(), "ws-1", "", "", true); len(l) != 1 {
		t.Errorf("includeArchived should show it, got %d", len(l))
	}

	if err := store.SetArchived(t.Context(), c.ID, false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if got, _ := store.GetChat(t.Context(), c.ID); got.Archived() {
		t.Error("chat should be unarchived")
	}

	if err := store.DeleteChat(t.Context(), c.ID); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if _, err := store.GetChat(t.Context(), c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := store.DeleteChat(t.Context(), c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("double delete should be ErrNotFound, got %v", err)
	}
}

func TestChat_SessionID(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager"})
	if err := store.UpdateSessionID(t.Context(), c.ID, "sess-abc"); err != nil {
		t.Fatalf("UpdateSessionID: %v", err)
	}
	got, _ := store.GetChat(t.Context(), c.ID)
	if got.SessionID != "sess-abc" {
		t.Errorf("SessionID: got %q", got.SessionID)
	}
}

func TestChat_FindOrCreateDefault(t *testing.T) {
	store := newStore(t)

	a, err := store.FindOrCreateDefault(t.Context(), "ws-1", "manager", "Manager", "system")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.FindOrCreateDefault(t.Context(), "ws-1", "manager", "Manager", "system")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Errorf("FindOrCreateDefault should reuse: %s vs %s", a.ID, b.ID)
	}

	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-2", AgentType: "manager", TaskID: "t-1"})
	c, err := store.FindOrCreateDefault(t.Context(), "ws-2", "manager", "Manager", "system")
	if err != nil {
		t.Fatal(err)
	}
	if c.TaskID != "" {
		t.Errorf("default chat must not be task-linked: %+v", c)
	}
}

func TestChat_Messages(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager"})

	u, err := store.AddMessage(t.Context(), &chat.Message{ChatID: c.ID, Role: chat.RoleUser, Author: "user@x.com", Text: "Hola"})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if u.ID == "" {
		t.Fatal("empty message ID")
	}

	a, err := store.AddMessage(t.Context(), &chat.Message{
		ChatID: c.ID, Role: chat.RoleAssistant, Author: "manager", Text: "Hola!",
		DurationMs: 1234, InputTokens: 5678,
		Metadata: map[string]string{"raw_log_path": "/x/y.jsonl"},
	})
	if err != nil {
		t.Fatalf("AddMessage assistant: %v", err)
	}

	msgs, err := store.ListMessages(t.Context(), c.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != chat.RoleUser || msgs[1].Role != chat.RoleAssistant {
		t.Errorf("order wrong: %q then %q", msgs[0].Role, msgs[1].Role)
	}
	if msgs[1].DurationMs != 1234 || msgs[1].InputTokens != 5678 {
		t.Errorf("assistant counters wrong: %+v", msgs[1])
	}
	if msgs[1].Metadata["raw_log_path"] != "/x/y.jsonl" {
		t.Errorf("metadata roundtrip failed: %+v", msgs[1].Metadata)
	}

	last, err := store.LastMessage(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.ID != a.ID {
		t.Errorf("LastMessage wrong: %+v", last)
	}

	if err := store.ClearMessages(t.Context(), c.ID); err != nil {
		t.Fatalf("ClearMessages: %v", err)
	}
	if msgs, _ := store.ListMessages(t.Context(), c.ID, 0, ""); len(msgs) != 0 {
		t.Errorf("expected 0 after clear, got %d", len(msgs))
	}
}

func TestChat_MessagesPagination(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager"})
	base := time.Now().Add(-time.Hour)
	ids := make([]string, 5)
	for i := 0; i < 5; i++ {
		m, err := store.AddMessage(t.Context(), &chat.Message{
			ChatID: c.ID, Role: chat.RoleUser, Text: "m",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = m.ID
	}

	tail, err := store.ListMessages(t.Context(), c.ID, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0].ID != ids[3] || tail[1].ID != ids[4] {
		t.Fatalf("tail wrong: %+v", tail)
	}

	older, err := store.ListMessages(t.Context(), c.ID, 2, ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 2 || older[0].ID != ids[1] || older[1].ID != ids[2] {
		t.Fatalf("older page wrong: %+v", older)
	}

	if _, err := store.ListMessages(t.Context(), c.ID, 2, "missing-id"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("expected ErrNotFound for bad beforeID, got %v", err)
	}
}

func TestChat_MessagesCascadeOnDelete(t *testing.T) {
	store := newStore(t)

	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager"})
	store.AddMessage(t.Context(), &chat.Message{ChatID: c.ID, Role: chat.RoleUser, Text: "x"})

	if err := store.DeleteChat(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	row := store.DB().QueryRow(`SELECT COUNT(*) FROM chat_messages_v2`)
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("messages should cascade on chat delete, %d left", n)
	}
}

func TestChat_DeleteWorkspaceChats(t *testing.T) {
	store := newStore(t)

	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager"})
	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "developer"})
	keep := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-2", AgentType: "manager"})

	if err := store.DeleteWorkspaceChats(t.Context(), "ws-1"); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListChats(t.Context(), "", "", "", true)
	if len(all) != 1 || all[0].ID != keep.ID {
		t.Errorf("purge wrong: %+v", all)
	}
}

func TestChat_LegacyImport(t *testing.T) {
	db := tempDB(t)
	_, err := db.Exec(`
		CREATE TABLE chat_messages (
			id            TEXT PRIMARY KEY,
			role          TEXT NOT NULL,
			text          TEXT NOT NULL,
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			duration_ms   INTEGER NOT NULL DEFAULT 0,
			raw_log_path  TEXT NOT NULL DEFAULT '',
			input_tokens  INTEGER NOT NULL DEFAULT 0
		)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO chat_messages (id, role, text, created_at, duration_ms, input_tokens) VALUES
		 ('m1', 'user', 'hola', NOW() - INTERVAL '2 minutes', 0, 0),
		 ('m2', 'assistant', 'buenas', NOW() - INTERVAL '1 minute', 900, 1500)`); err != nil {
		t.Fatal(err)
	}

	store, err := chat.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	legacy, err := store.GetChat(t.Context(), "chat-manager-legacy")
	if err != nil {
		t.Fatalf("legacy chat not created: %v", err)
	}
	if legacy.AgentType != "manager" || legacy.Title != "Manager" {
		t.Errorf("legacy chat shape: %+v", legacy)
	}

	msgs, err := store.ListMessages(t.Context(), legacy.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 imported messages, got %d", len(msgs))
	}
	if msgs[1].Role != chat.RoleAssistant || msgs[1].InputTokens != 1500 {
		t.Errorf("imported assistant message wrong: %+v", msgs[1])
	}

	if _, err := db.Exec(`SELECT 1 FROM chat_messages`); err == nil {
		t.Error("legacy chat_messages table should be dropped")
	}
}

func TestChat_LegacyImport_EmptyTable(t *testing.T) {
	store := newStore(t)

	if _, err := store.GetChat(t.Context(), "chat-manager-legacy"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("no legacy chat expected on fresh DB, got %v", err)
	}
}

func TestChat_DBAccessor(t *testing.T) {
	store := newStore(t)

	if store.DB() == nil {
		t.Error("DB() should not return nil")
	}
	if err := store.DB().Ping(); err != nil {
		t.Errorf("DB().Ping(): %v", err)
	}
}

func TestBus_PublishSubscribe(t *testing.T) {
	bus := chat.NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()

	bus.Publish(chat.Event{Type: chat.EventTurnStarted, ChatID: "c-1", WorkspaceID: "ws-1"})
	select {
	case ev := <-ch:
		if ev.Type != chat.EventTurnStarted || ev.ChatID != "c-1" {
			t.Errorf("event mismatch: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
}

func TestBus_DropOnFull(t *testing.T) {
	bus := chat.NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			bus.Publish(chat.Event{Type: chat.EventMessageAdded, ChatID: "c-1"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	if n := len(ch); n != 64 {
		t.Errorf("expected full buffer of 64, got %d", n)
	}
}

func TestBus_Cancel(t *testing.T) {
	bus := chat.NewBus()
	_, cancel := bus.Subscribe()
	cancel()
	bus.Publish(chat.Event{Type: chat.EventChatDeleted, ChatID: "c-1"})
}

func TestChat_FindOrCreatePerTask(t *testing.T) {
	store := newStore(t)

	standing, err := store.FindOrCreateDefault(t.Context(), "ws-1", "reviewer", "Reviewer", "system")
	if err != nil {
		t.Fatalf("FindOrCreateDefault: %v", err)
	}
	onTask, err := store.FindOrCreate(t.Context(), &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "task-7", Title: "Reviewer · parser", CreatedBy: chat.SystemCreator})
	if err != nil {
		t.Fatalf("FindOrCreate: %v", err)
	}
	if onTask.ID == standing.ID {
		t.Fatal("the task chat reused the agent's standing chat")
	}
	if onTask.TaskID != "task-7" {
		t.Errorf("task_id = %q, want task-7", onTask.TaskID)
	}

	again, err := store.FindOrCreate(t.Context(), &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "task-7", Title: "ignored", CreatedBy: chat.SystemCreator})
	if err != nil {
		t.Fatalf("FindOrCreate again: %v", err)
	}
	if again.ID != onTask.ID {
		t.Errorf("second mention opened a new chat (%s vs %s)", again.ID, onTask.ID)
	}
	if again.Title != onTask.Title {
		t.Errorf("title changed on reuse: %q", again.Title)
	}

	other, err := store.FindOrCreate(t.Context(), &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "task-8", Title: "Reviewer · otra", CreatedBy: chat.SystemCreator})
	if err != nil {
		t.Fatalf("FindOrCreate other task: %v", err)
	}
	if other.ID == onTask.ID {
		t.Error("two tasks share one chat")
	}
}

func TestChat_FindOrCreatePerThread(t *testing.T) {
	store := newStore(t)
	find := func(thread string) *chat.Chat {
		t.Helper()
		c, err := store.FindOrCreate(t.Context(), &chat.Chat{WorkspaceID: "ws-1", AgentType: "manager", TaskID: "task-7", ThreadID: thread, CreatedBy: chat.SystemCreator})
		if err != nil {
			t.Fatalf("FindOrCreate(%q): %v", thread, err)
		}
		return c
	}
	onTask, a, b := find(""), find("c1"), find("c2")
	if a.ID == onTask.ID || a.ID == b.ID {
		t.Fatalf("threads share a chat: task %s, c1 %s, c2 %s", onTask.ID, a.ID, b.ID)
	}
	if again := find("c1"); again.ID != a.ID || again.ThreadID != "c1" {
		t.Errorf("c1 reopened as %s (thread %q), want %s", again.ID, again.ThreadID, a.ID)
	}
}

func TestChat_FindLive(t *testing.T) {
	store := newStore(t)
	want := &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", CreatedBy: chat.SystemCreator}

	if _, err := store.FindLive(t.Context(), want); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("FindLive on an empty store = %v, want ErrNotFound", err)
	}
	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", ThreadID: "th-1", CreatedBy: chat.SystemCreator})
	if _, err := store.FindLive(t.Context(), want); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("a thread's chat answered for the task's: %v", err)
	}
	c := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", Title: "Reviewer · Fix", CreatedBy: chat.SystemCreator})
	got, err := store.FindLive(t.Context(), want)
	if err != nil || got.ID != c.ID {
		t.Fatalf("FindLive = %+v, %v; want %s", got, err, c.ID)
	}
	if err := store.SetArchived(t.Context(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindLive(t.Context(), want); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("an archived chat answered: %v", err)
	}
}

func TestEncodeTools(t *testing.T) {
	meta := map[string]string{}
	chat.EncodeTools(meta, nil, 5)
	if len(meta) != 0 {
		t.Fatalf("a turn with no tools wrote %v", meta)
	}

	calls := []chat.ToolCall{{Name: "Read", Hint: "a.ts"}, {Name: "Bash", Hint: "npm test"}, {Name: "Grep"}}
	chat.EncodeTools(meta, calls, 2)
	if meta[chat.MetaTools] != `[{"name":"Read","hint":"a.ts"},{"name":"Bash","hint":"npm test"}]` {
		t.Errorf("tools = %s", meta[chat.MetaTools])
	}
	if meta[chat.MetaToolsOmitted] != "1" {
		t.Errorf("omitted = %q, want 1", meta[chat.MetaToolsOmitted])
	}
}

func TestChat_FindLiveKeepsPersonsAndTheDaemonApart(t *testing.T) {
	store := newStore(t)
	ana := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", CreatedBy: "ana@x.com", Owner: "user:ana"})
	daemon := mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", CreatedBy: chat.SystemCreator})
	mustCreate(t, store, &chat.Chat{WorkspaceID: "ws-1", AgentType: "reviewer", TaskID: "t-1", CreatedBy: "legacy@x.com"})

	for _, tc := range []struct {
		name string
		want chat.Chat
		id   string
	}{
		{"its owner", chat.Chat{CreatedBy: "ana@x.com", Owner: "user:ana"}, ana.ID},
		{"another person", chat.Chat{CreatedBy: "bob@x.com", Owner: "user:bob"}, ""},
		{"a person with no owner id", chat.Chat{CreatedBy: "legacy@x.com"}, ""},
		{"the daemon", chat.Chat{CreatedBy: chat.SystemCreator}, daemon.ID},
	} {
		w := tc.want
		w.WorkspaceID, w.AgentType, w.TaskID = "ws-1", "reviewer", "t-1"
		got, err := store.FindLive(t.Context(), &w)
		switch {
		case tc.id == "" && !errors.Is(err, chat.ErrNotFound):
			t.Errorf("%s: FindLive = %+v, %v; want ErrNotFound", tc.name, got, err)
		case tc.id != "" && (err != nil || got.ID != tc.id):
			t.Errorf("%s: FindLive = %+v, %v; want %s", tc.name, got, err, tc.id)
		}
	}
}
