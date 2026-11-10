package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWalkDataDir(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, filepath.Join(dir, "projects", "shop", "main.go"), 100)
	writeSized(t, filepath.Join(dir, "projects", "shop", ".git", "HEAD"), 20)
	writeSized(t, filepath.Join(dir, "projects", "blog", "index.md"), 30)
	writeSized(t, filepath.Join(dir, "projects", ".worktrees", "shop--task-1", "main.go"), 50)
	writeSized(t, filepath.Join(dir, "task-attachments", "a.png"), 400)
	writeSized(t, filepath.Join(dir, "logs", "run.log"), 7)
	writeSized(t, filepath.Join(dir, "octopus.db.bak"), 3)
	if err := os.Symlink(filepath.Join(dir, "task-attachments", "a.png"), filepath.Join(dir, "logs", "link")); err != nil {
		t.Fatal(err)
	}

	w := walkDataDir(context.Background(), dir)
	got := map[string]int64{}
	for _, d := range w.storage.Dirs {
		got[d.Name] = d.Bytes
	}
	want := map[string]int64{"projects": 150, "worktrees": 50, "task-attachments": 400, "logs": 7, "other": 3}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (symlinks count as nothing)", k, got[k], v)
		}
	}
	if w.storage.Total != 610 || w.storage.Partial {
		t.Errorf("total = %d partial = %v", w.storage.Total, w.storage.Partial)
	}
	if w.storage.Dirs[0].Name != "task-attachments" {
		t.Errorf("dirs not largest first: %+v", w.storage.Dirs)
	}
	if w.byProject["shop"] != 170 || w.byProject["blog"] != 30 {
		t.Errorf("by project = %+v, want the worktree folded into its project", w.byProject)
	}

	ws := workspaceStorage(w.byProject, []projectRow{
		{id: "p1", workspaceID: "ws1", path: filepath.Join(dir, "projects", "shop")},
		{id: "p2", workspaceID: "ws2", path: "/data/projects/blog"},
		{id: "p3", workspaceID: "ws2", path: ""},
	})
	if ws["ws1"] != 170 || ws["ws2"] != 30 {
		t.Errorf("workspace storage = %+v", ws)
	}
}

func TestWalkDataDirStopsAtDeadline(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, filepath.Join(dir, "logs", "a"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w := walkDataDir(ctx, dir); !w.storage.Partial {
		t.Error("a cancelled walk was not marked partial")
	}
	if w := walkDataDir(context.Background(), filepath.Join(dir, "missing")); w.storage.Total != 0 || len(w.storage.Dirs) != 0 {
		t.Errorf("missing data dir = %+v", w.storage)
	}
}

func TestStartOfDayIsUTC(t *testing.T) {
	art := time.FixedZone("ART", -3*3600)
	got := startOfDay(time.Date(2026, 10, 3, 22, 30, 0, 0, art))
	if !got.Equal(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("startOfDay = %v", got)
	}
}

func TestReadDB(t *testing.T) {
	db := testpg.New(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, path TEXT NOT NULL)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, finished_at TIMESTAMP NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read INTEGER NOT NULL DEFAULT 0, cache_creation INTEGER NOT NULL DEFAULT 0,
			cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0)`,
		`INSERT INTO workspaces VALUES ('ws2', 'beta'), ('ws1', 'Acme')`,
		`INSERT INTO tasks VALUES ('t1', 'ws1'), ('t2', 'ws1'), ('t3', 'ws2')`,
		`INSERT INTO projects VALUES ('p1', 'ws1', '/data/projects/shop')`,
		`INSERT INTO sessions VALUES
			('s1', 'ws1', '2026-10-03 10:00', 100, 50, 10, 5, 0.25),
			('s2', 'ws1', '2026-10-03 11:00', 1, 1, 1, 1, 0.75),
			('s3', 'ws1', '2026-10-02 23:00', 999, 0, 0, 0, 9)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	st, probs := readDB(ctx, db, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if len(probs) != 0 {
		t.Fatalf("problems = %+v", probs)
	}
	if st.postgres == nil || st.postgres.DatabaseSize <= 0 || st.postgres.MaxConnections <= 0 || st.postgres.Version == "" {
		t.Errorf("postgres = %+v", st.postgres)
	}
	if len(st.postgres.LargestTables) != 4 {
		t.Errorf("largest tables = %+v", st.postgres.LargestTables)
	}
	if st.counts != (Counts{Workspaces: 2, Users: 0, Tasks: 3, Projects: 1}) {
		t.Errorf("counts = %+v", st.counts)
	}
	if st.names["ws1"] != "Acme" || len(st.workspaces) != 2 || st.workspaces[0] != "ws1" {
		t.Errorf("workspaces = %v %v, want name order", st.names, st.workspaces)
	}
	u := st.usage["ws1"]
	if u.runs != 2 || u.tokens != 169 || !near(u.cost, 1) {
		t.Errorf("usage today = %+v, want yesterday's session left out", u)
	}
	if len(st.projects) != 1 || st.projects[0].path != "/data/projects/shop" {
		t.Errorf("projects = %+v", st.projects)
	}
}
