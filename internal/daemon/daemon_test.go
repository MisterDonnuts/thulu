package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// An unreachable Postgres fails after st.db is set; the deferred close must not panic and hide this
// error.
func TestOpenStores_ReturnsTheStoreError(t *testing.T) {
	t.Setenv("OCTOPUS_DATABASE_URL", "postgres://octopus:x@127.0.0.1:1/octopus?sslmode=disable&connect_timeout=1")
	saved := dbWait
	dbWait = db.WaitPolicy{Initial: time.Millisecond, Max: time.Millisecond, Budget: 20 * time.Millisecond, PingTimeout: time.Second}
	t.Cleanup(func() { dbWait = saved })
	st, err := openStores(t.Context(), t.TempDir())
	if err == nil {
		st.closeAll()
		t.Fatal("openStores succeeded against a closed port")
	}
	if st != nil {
		t.Errorf("openStores returned stores along with %v", err)
	}
}

func TestOpenStores_SingleSharedDB(t *testing.T) {
	if os.Getenv("OCTOPUS_DATABASE_URL") == "" {
		t.Skip("OCTOPUS_DATABASE_URL not set; rewrite pending in Phase 2 (testpg helper)")
	}
	dataDir := t.TempDir()

	st, err := openStores(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("openStores: %v", err)
	}
	defer st.closeAll()

	rows, err := st.db.Query("SELECT DISTINCT component FROM schema_migrations ORDER BY component")
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []string{"audit", "auth", "chatruntime", "chats", "entitlements", "githubapp", "mcp_registrations", "project", "registry", "sandbox", "secrets", "settings", "task", "usage", "webhook"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("components = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("components = %v, want %v", got, want)
			break
		}
	}
}

func TestWorkspaceIDBackfill_SingleWorkspaceStampsLegacyRows(t *testing.T) {
	if os.Getenv("OCTOPUS_DATABASE_URL") == "" {
		t.Skip("OCTOPUS_DATABASE_URL not set; rewrite pending in Phase 2 (testpg helper)")
	}
	st, err := openStores(t.Context(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("openStores: %v", err)
	}
	t.Cleanup(st.closeAll)
	ctx := context.Background()

	u, err := st.auth.CreateUser(ctx, "op@test.com", "Op")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	ws, err := st.auth.CreateWorkspace(ctx, u.ID, "Solo", "solo")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	legacy := &task.Task{Title: "legacy"}
	if err := st.task.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy task: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO projects
		(id, name, path, repo_url, created_at) VALUES ('p1','Legacy','/tmp/x','none', ?)`,
		time.Now()); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}

	if err := workspaceIDBackfill(ctx, st.db); err != nil {
		t.Fatalf("workspaceIDBackfill: %v", err)
	}

	got, err := st.task.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.WorkspaceID != ws.ID {
		t.Errorf("task workspace_id = %q, want %q", got.WorkspaceID, ws.ID)
	}
	proj, err := st.project.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if proj.WorkspaceID != ws.ID {
		t.Errorf("project workspace_id = %q, want %q", proj.WorkspaceID, ws.ID)
	}

	if err := workspaceIDBackfill(ctx, st.db); err != nil {
		t.Fatalf("workspaceIDBackfill (2nd): %v", err)
	}
}

func TestWorkspaceIDBackfill_MultiWorkspaceIsNoOp(t *testing.T) {
	if os.Getenv("OCTOPUS_DATABASE_URL") == "" {
		t.Skip("OCTOPUS_DATABASE_URL not set; rewrite pending in Phase 2 (testpg helper)")
	}
	st, err := openStores(t.Context(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("openStores: %v", err)
	}
	t.Cleanup(st.closeAll)
	ctx := context.Background()

	u, _ := st.auth.CreateUser(ctx, "a@test.com", "A")
	u2, _ := st.auth.CreateUser(ctx, "b@test.com", "B")
	st.auth.CreateWorkspace(ctx, u.ID, "WA", "wa")
	st.auth.CreateWorkspace(ctx, u2.ID, "WB", "wb")

	legacy := &task.Task{Title: "ambiguous legacy"}
	if err := st.task.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy task: %v", err)
	}

	if err := workspaceIDBackfill(ctx, st.db); err != nil {
		t.Fatalf("workspaceIDBackfill: %v", err)
	}
	got, _ := st.task.Get(ctx, legacy.ID)
	if got.WorkspaceID != "" {
		t.Errorf("legacy task stamped to %q with two workspaces — backfill must stay no-op", got.WorkspaceID)
	}
}
