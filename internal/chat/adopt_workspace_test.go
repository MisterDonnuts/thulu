package chat_test

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func TestMigrationAdoptsTheTasksWorkspace(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := chat.NewStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE tasks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '');
		INSERT INTO tasks VALUES ('t-owned', 'ws-a'), ('t-legacy', '');
		INSERT INTO chats (id, workspace_id, agent_type, task_id, title, created_by, created_at, updated_at) VALUES
		  ('c-task',   '',     'dev', 't-owned',  '', '', now(), now()),
		  ('c-legacy', '',     'dev', 't-legacy', '', '', now(), now()),
		  ('c-none',   '',     'dev', '',         '', '', now(), now()),
		  ('c-other',  'ws-b', 'dev', 't-owned',  '', '', now(), now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.NewStore(db); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c-task": "ws-a", "c-legacy": "", "c-none": "", "c-other": "ws-b"}
	for id, wid := range want {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM chats WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != wid {
			t.Errorf("chat %s workspace_id = %q, want %q", id, got, wid)
		}
	}
}
