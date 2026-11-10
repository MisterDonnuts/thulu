package daemon

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestWorkspaceIDBackfillStampsChatsAndSkipsMissingTables(t *testing.T) {
	ctx := context.Background()
	db := testpg.New(t)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		INSERT INTO workspaces VALUES ('ws-solo');
		CREATE TABLE projects (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '');
		CREATE TABLE tasks    (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '');
		CREATE TABLE chats    (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '');
		CREATE TABLE automations (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '');
		INSERT INTO chats VALUES ('c1', ''), ('c2', 'ws-other');
		INSERT INTO automations VALUES ('a1', '');`); err != nil {
		t.Fatal(err)
	}

	if err := workspaceIDBackfill(ctx, db); err != nil {
		t.Fatalf("workspaceIDBackfill: %v", err)
	}

	for table, rows := range map[string]map[string]string{
		"chats":       {"c1": "ws-solo", "c2": "ws-other"},
		"automations": {"a1": "ws-solo"},
	} {
		for id, want := range rows {
			var got string
			if err := db.QueryRowContext(ctx,
				`SELECT workspace_id FROM `+table+` WHERE id = $1`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s %s workspace_id = %q, want %q", table, id, got, want)
			}
		}
	}
}
