package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func seedAttributionTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO tasks (id, workspace_id) VALUES ('task-a', 'ws-a') ON CONFLICT DO NOTHING`,
		`INSERT INTO tasks (id, workspace_id) VALUES ('task-nowhere', '') ON CONFLICT DO NOTHING`,
		`INSERT INTO projects (id, workspace_id) VALUES ('proj-b', 'ws-b') ON CONFLICT DO NOTHING`,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("seed attribution tables: %v", err)
		}
	}
}

// Migration ids 24, 25, 29 are usage's workspace backfill; keep in step with the usage migrations.
func rewindWorkspaceBackfill(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`UPDATE sessions SET workspace_id = ''`,
		`DELETE FROM schema_migrations WHERE component = 'usage' AND id IN (24, 25, 29)`,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("rewind backfill: %v", err)
		}
	}
}

func sessionAt(id string, now time.Time) Session {
	return Session{
		ID:           id,
		Agent:        "a-" + id,
		AgentType:    "developer",
		Engine:       "claude-code",
		Model:        "opus",
		StartedAt:    now.Add(-time.Minute),
		FinishedAt:   now,
		DurationMs:   60000,
		Turns:        3,
		InputTokens:  100,
		OutputTokens: 50,
		CostUSD:      0.25,
		StopReason:   "end_turn",
	}
}

func TestWorkspaceBackfill(t *testing.T) {
	dir := t.TempDir()
	db := testpg.New(t)
	seedAttributionTables(t, db)

	store, err := NewStore(db, dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()

	byTask := sessionAt("run-task", now)
	byTask.TaskID = "task-a"
	byProject := sessionAt("run-project", now)
	byProject.ProjectID = "proj-b"
	orphan := sessionAt("run-orphan", now)
	danglingTask := sessionAt("run-dangling", now)
	danglingTask.TaskID = "task-nowhere"
	for _, sess := range []Session{byTask, byProject, orphan, danglingTask} {
		if err := store.Add(t.Context(), sess); err != nil {
			t.Fatalf("add %s: %v", sess.ID, err)
		}
	}

	rewindWorkspaceBackfill(t, db)
	store, err = NewStore(db, dir)
	if err != nil {
		t.Fatalf("NewStore after rewind: %v", err)
	}

	for _, tc := range []struct{ id, want string }{
		{"run-task", "ws-a"},
		{"run-project", "ws-b"},
		{"run-orphan", ""},
		{"run-dangling", ""},
	} {
		got := store.Get(t.Context(), tc.id)
		if got == nil {
			t.Fatalf("session %s vanished", tc.id)
		}
		if got.WorkspaceID != tc.want {
			t.Errorf("%s workspace = %q, want %q", tc.id, got.WorkspaceID, tc.want)
		}
	}

	if got := store.Sessions(t.Context(), InWorkspace("ws-a")); len(got) != 1 || got[0].ID != "run-task" {
		t.Errorf("ws-a sees %+v, want only run-task", got)
	}
	if got := store.Sessions(t.Context(), InWorkspace("ws-b")); len(got) != 1 || got[0].ID != "run-project" {
		t.Errorf("ws-b sees %+v, want only run-project", got)
	}
	if got := store.Sessions(t.Context(), Everything()); len(got) != 4 {
		t.Errorf("unrestricted sees %d sessions, want 4", len(got))
	}
}

// An unattributed session (empty workspace_id) is visible to no scoped caller, unlike
// workspaceVisible's unstamped-row rule elsewhere.
func TestUnattributedSessionsAreInvisibleToScopedCallers(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()

	mine := sessionAt("run-mine", now)
	mine.WorkspaceID = "ws-mine"
	if err := store.Add(t.Context(), mine); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Add(t.Context(), sessionAt("run-unattributed", now)); err != nil {
		t.Fatalf("add: %v", err)
	}

	if got := store.Sessions(t.Context(), InWorkspace("ws-mine")); len(got) != 1 || got[0].ID != "run-mine" {
		t.Errorf("scoped list = %+v, want only run-mine", got)
	}
	if got := store.Sessions(t.Context(), InWorkspace("")); len(got) != 0 {
		t.Errorf("caller with no workspace sees %+v, want nothing", got)
	}
	if got := store.Totals(t.Context(), InWorkspace("ws-other")); got.Sessions != 0 || got.TotalCostUSD != 0 {
		t.Errorf("another workspace's totals = %+v, want zero", got)
	}
	if got := store.Totals(t.Context(), InWorkspace("ws-mine")); got.Sessions != 1 {
		t.Errorf("own totals = %d sessions, want 1", got.Sessions)
	}
	if got := store.ByAgent(t.Context(), InWorkspace("ws-other")); len(got) != 0 {
		t.Errorf("another workspace's by-agent = %+v, want nothing", got)
	}
}

func TestFilterWithoutScopeReportsNothing(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	sess := sessionAt("run-scoped", now)
	sess.WorkspaceID = "ws-mine"
	sess.TaskID = "task-a"
	if err := store.Add(t.Context(), sess); err != nil {
		t.Fatalf("add: %v", err)
	}

	ctx := context.Background()
	window := Filter{From: now.Add(-time.Hour), To: now.Add(time.Hour)}

	eff, err := store.Efficiency(ctx, window)
	if err != nil {
		t.Fatalf("efficiency: %v", err)
	}
	if eff.Sessions != 0 {
		t.Errorf("unscoped efficiency counted %d sessions, want 0", eff.Sessions)
	}
	rows, err := store.ByDimension(ctx, window, "model")
	if err != nil {
		t.Fatalf("by-model: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("unscoped by-model = %+v, want nothing", rows)
	}
	tasks, err := store.TopTasks(ctx, window, 10)
	if err != nil {
		t.Fatalf("top-tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("unscoped top-tasks = %+v, want nothing", tasks)
	}

	window.Scope = InWorkspace("ws-mine")
	eff, err = store.Efficiency(ctx, window)
	if err != nil {
		t.Fatalf("scoped efficiency: %v", err)
	}
	if eff.Sessions != 1 {
		t.Errorf("scoped efficiency counted %d sessions, want 1", eff.Sessions)
	}
}

func TestTimeSeriesRollupIsWorkspaceScoped(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Add(-time.Hour)

	mine := sessionAt("run-mine", now)
	mine.WorkspaceID = "ws-mine"
	theirs := sessionAt("run-theirs", now)
	theirs.WorkspaceID = "ws-theirs"
	theirs.CostUSD = 9.99
	for _, sess := range []Session{mine, theirs} {
		if err := store.Add(t.Context(), sess); err != nil {
			t.Fatalf("add %s: %v", sess.ID, err)
		}
	}

	ctx := context.Background()
	f := Filter{Scope: InWorkspace("ws-mine"), From: now.Add(-24 * time.Hour), To: now.Add(time.Hour)}
	pts, err := store.TimeSeries(ctx, f, BucketDay)
	if err != nil {
		t.Fatalf("timeseries: %v", err)
	}
	var cost float64
	var sessions int
	for _, p := range pts {
		cost += p.CostUSD
		sessions += p.Sessions
	}
	if sessions != 1 || !approxEqual(cost, mine.CostUSD) {
		t.Errorf("scoped day timeseries = %d sessions / $%.2f, want 1 / $%.2f", sessions, cost, mine.CostUSD)
	}

	hourly, err := store.TimeSeries(ctx, f, BucketHour)
	if err != nil {
		t.Fatalf("hourly timeseries: %v", err)
	}
	sessions = 0
	for _, p := range hourly {
		sessions += p.Sessions
	}
	if sessions != 1 {
		t.Errorf("scoped hour timeseries = %d sessions, want 1", sessions)
	}
}

func TestAddStampsWorkspaceFromResolver(t *testing.T) {
	store := newTestStore(t).WithWorkspaceResolver(func(_ context.Context, taskID, projectID string) string {
		switch {
		case taskID == "task-a":
			return "ws-a"
		case projectID == "proj-b":
			return "ws-b"
		}
		return ""
	})
	now := time.Now().UTC()

	byTask := sessionAt("run-task", now)
	byTask.TaskID = "task-a"
	byProject := sessionAt("run-project", now)
	byProject.ProjectID = "proj-b"
	adhoc := sessionAt("run-adhoc", now)
	for _, sess := range []Session{byTask, byProject, adhoc} {
		if err := store.Add(t.Context(), sess); err != nil {
			t.Fatalf("add %s: %v", sess.ID, err)
		}
	}

	for _, tc := range []struct{ id, want string }{
		{"run-task", "ws-a"},
		{"run-project", "ws-b"},
		{"run-adhoc", ""},
	} {
		if got := store.Get(t.Context(), tc.id); got == nil || got.WorkspaceID != tc.want {
			t.Errorf("%s workspace = %v, want %q", tc.id, got, tc.want)
		}
	}

	blind := &Store{db: store.db, dataDir: store.dataDir}
	again := sessionAt("run-task", now.Add(time.Minute))
	again.TaskID = "task-a"
	if err := blind.Add(t.Context(), again); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if got := store.Get(t.Context(), "run-task"); got == nil || got.WorkspaceID != "ws-a" {
		t.Errorf("re-insert dropped the workspace: %+v", got)
	}
}
