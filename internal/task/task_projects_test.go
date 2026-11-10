package task

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func projectsOf(t *testing.T, s *Store, id string) []string {
	t.Helper()
	got, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return got.ProjectIDs
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestProjectLinks_RoundTripPreservesOrder(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "spans three repos", ProjectIDs: []string{"web", "api", "infra"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := projectsOf(t, s, tk.ID); !equalSlices(got, []string{"web", "api", "infra"}) {
		t.Errorf("project ids = %v, want [web api infra]", got)
	}
}

func TestProjectLinks_EmptyIsEmptyNotNil(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "no repos"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ProjectIDs == nil {
		t.Error("ProjectIDs is nil — it serializes as null instead of []")
	}
	if len(got.ProjectIDs) != 0 {
		t.Errorf("ProjectIDs = %v, want empty", got.ProjectIDs)
	}
}

func TestProjectLinks_UpdateReplacesTheWholeSet(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"a", "b"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	next := []string{"b", "c"}
	if _, err := s.Update(t.Context(), tk.ID, Patch{ProjectIDs: &next}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := projectsOf(t, s, tk.ID); !equalSlices(got, []string{"b", "c"}) {
		t.Errorf("project ids = %v, want [b c]", got)
	}
}

func TestProjectLinks_UpdateToEmptyUnlinksAll(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"a", "b"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	none := []string{}
	if _, err := s.Update(t.Context(), tk.ID, Patch{ProjectIDs: &none}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := projectsOf(t, s, tk.ID); len(got) != 0 {
		t.Errorf("project ids = %v, want none", got)
	}
}

func TestProjectLinks_UnrelatedPatchKeepsLinks(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"a", "b"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	running := StatusRunning
	if _, err := s.Update(t.Context(), tk.ID, Patch{Status: &running}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := projectsOf(t, s, tk.ID); !equalSlices(got, []string{"a", "b"}) {
		t.Errorf("project ids = %v, want [a b]", got)
	}
}

func TestProjectLinks_DuplicatesAreCollapsed(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"a", "a", "b"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := projectsOf(t, s, tk.ID); !equalSlices(got, []string{"a", "b"}) {
		t.Errorf("project ids = %v, want [a b] — a link is a set membership", got)
	}
}

func TestProjectLinks_DeletingATaskRemovesItsLinks(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"a"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(t.Context(), tk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var n int
	if err := s.DB().QueryRowContext(context.Background(),
		dbpkg.Rebind(`SELECT count(*) FROM task_projects WHERE task_id = ?`), tk.ID).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if n != 0 {
		t.Errorf("%d orphaned link rows after deleting the task", n)
	}
}

func TestProjectLinks_UnlinkProjectClearsEveryTask(t *testing.T) {
	s := newTestStore(t)
	a := &Task{WorkspaceID: testWS, Title: "a", ProjectIDs: []string{"web", "api"}}
	b := &Task{WorkspaceID: testWS, Title: "b", ProjectIDs: []string{"api"}}
	c := &Task{WorkspaceID: testWS, Title: "c", ProjectIDs: []string{"infra"}}
	for _, tk := range []*Task{a, b, c} {
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("Create %s: %v", tk.Title, err)
		}
	}

	n, err := s.UnlinkProject(t.Context(), "api")
	if err != nil {
		t.Fatalf("UnlinkProject: %v", err)
	}
	if n != 2 {
		t.Errorf("unlinked %d tasks, want 2", n)
	}
	if got := projectsOf(t, s, a.ID); !equalSlices(got, []string{"web"}) {
		t.Errorf("task a = %v, want [web]", got)
	}
	if got := projectsOf(t, s, b.ID); len(got) != 0 {
		t.Errorf("task b = %v, want none", got)
	}
	if got := projectsOf(t, s, c.ID); !equalSlices(got, []string{"infra"}) {
		t.Errorf("task c = %v, want [infra] — untouched", got)
	}
}

func TestProjectLinks_UnlinkUnknownProjectIsANoOp(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"web"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	n, err := s.UnlinkProject(t.Context(), "nope")
	if err != nil {
		t.Fatalf("UnlinkProject: %v", err)
	}
	if n != 0 {
		t.Errorf("unlinked %d, want 0", n)
	}
	if got := projectsOf(t, s, tk.ID); !equalSlices(got, []string{"web"}) {
		t.Errorf("project ids = %v", got)
	}
}

func TestProjectLinks_ListDecoratesEachRowIndependently(t *testing.T) {
	s := newTestStore(t)
	a := &Task{WorkspaceID: testWS, Title: "a", ProjectIDs: []string{"web"}}
	b := &Task{WorkspaceID: testWS, Title: "b", ProjectIDs: []string{"api", "infra"}}
	c := &Task{WorkspaceID: testWS, Title: "c"}
	for _, tk := range []*Task{a, b, c} {
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	all, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string][]string{}
	for i := range all {
		byID[all[i].ID] = all[i].ProjectIDs
	}
	if !equalSlices(byID[a.ID], []string{"web"}) {
		t.Errorf("a = %v", byID[a.ID])
	}
	if !equalSlices(byID[b.ID], []string{"api", "infra"}) {
		t.Errorf("b = %v", byID[b.ID])
	}
	if len(byID[c.ID]) != 0 {
		t.Errorf("c = %v, want none", byID[c.ID])
	}
}

func TestProjectLinks_ForkGetsItsOwnLinks(t *testing.T) {
	s := newTestStore(t)
	src := &Task{WorkspaceID: testWS, Title: "src", ProjectIDs: []string{"web", "api"}}
	if err := s.Create(t.Context(), src); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fork := &Task{WorkspaceID: testWS, Title: "fork", ProjectIDs: append([]string(nil), src.ProjectIDs...)}
	if err := s.Create(t.Context(), fork); err != nil {
		t.Fatalf("Create fork: %v", err)
	}
	only := []string{"web"}
	if _, err := s.Update(t.Context(), fork.ID, Patch{ProjectIDs: &only}); err != nil {
		t.Fatalf("Update fork: %v", err)
	}
	if got := projectsOf(t, s, src.ID); !equalSlices(got, []string{"web", "api"}) {
		t.Errorf("source changed with the fork: %v", got)
	}
}

func TestProjectLinks_BackfilledFromLegacyColumn(t *testing.T) {
	db := testpg.New(t)
	seedLegacyTasksTable(t, db)

	rows := []struct{ id, projects string }{
		{"aaaa000000000001", `["web","api"]`},
		{"aaaa000000000002", `["infra"]`},
		{"aaaa000000000003", `[]`},
		{"aaaa000000000004", `["web"]`},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(context.Background(), dbpkg.Rebind(
			`INSERT INTO tasks (id, project_ids, title, body, stage, stage_assignments,
			  branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
			  tags, priority, created_at, updated_at)
			 VALUES (?,?,?,'','backlog','{}','','{}','{}','','','[]','',now(),now())`),
			r.id, r.projects, "legacy "+r.id); err != nil {
			t.Fatalf("seed row %s: %v", r.id, err)
		}
	}

	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore (runs the migration): %v", err)
	}

	for _, want := range []struct {
		id       string
		projects []string
	}{
		{"aaaa000000000001", []string{"web", "api"}},
		{"aaaa000000000002", []string{"infra"}},
		{"aaaa000000000003", nil},
		{"aaaa000000000004", []string{"web"}},
	} {
		got := projectsOf(t, store, want.id)
		if len(want.projects) == 0 {
			if len(got) != 0 {
				t.Errorf("%s = %v, want none", want.id, got)
			}
			continue
		}
		if !equalSlices(got, want.projects) {
			t.Errorf("%s = %v, want %v", want.id, got, want.projects)
		}
	}
}

func TestProjectLinks_BackfillIsIdempotent(t *testing.T) {
	db := testpg.New(t)
	seedLegacyTasksTable(t, db)
	if _, err := db.ExecContext(context.Background(), dbpkg.Rebind(
		`INSERT INTO tasks (id, project_ids, title, body, stage, stage_assignments,
		  branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
		  tags, priority, created_at, updated_at)
		 VALUES (?,?,?,'','backlog','{}','','{}','{}','','','[]','',now(),now())`),
		"bbbb000000000001", `["web","api"]`, "legacy"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dir := t.TempDir()
	for i := range 3 {
		if _, err := NewStore(db, dir); err != nil {
			t.Fatalf("NewStore #%d: %v", i, err)
		}
	}
	store, err := NewStore(db, dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := projectsOf(t, store, "bbbb000000000001"); !equalSlices(got, []string{"web", "api"}) {
		t.Errorf("project ids = %v, want [web api] exactly once", got)
	}
}

func TestProjectLinks_BackfillFromCurrentProductionShape(t *testing.T) {
	db := testpg.New(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE tasks (
		id                 TEXT PRIMARY KEY,
		project_ids        TEXT NOT NULL DEFAULT '[]',
		title              TEXT NOT NULL,
		body               TEXT NOT NULL DEFAULT '',
		stage              TEXT NOT NULL,
		stage_assignments  TEXT NOT NULL DEFAULT '{}',
		branch_name        TEXT NOT NULL DEFAULT '',
		worktree_paths     TEXT NOT NULL DEFAULT '{}',
		pr_urls            TEXT NOT NULL DEFAULT '{}',
		current_run_id     TEXT NOT NULL DEFAULT '',
		current_instance   TEXT NOT NULL DEFAULT '',
		status             TEXT NOT NULL DEFAULT 'idle',
		tags               TEXT NOT NULL DEFAULT '[]',
		priority           TEXT NOT NULL DEFAULT '',
		workspace_id       TEXT NOT NULL DEFAULT '',
		board_id           TEXT NOT NULL DEFAULT 'default',
		type_id            TEXT NOT NULL DEFAULT '',
		parent_id          TEXT NOT NULL DEFAULT '',
		created_at         TIMESTAMP NOT NULL,
		updated_at         TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatalf("seed production-shaped tasks table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		component TEXT NOT NULL, id INTEGER NOT NULL, applied_at TIMESTAMP NOT NULL,
		PRIMARY KEY (component, id))`); err != nil {
		t.Fatalf("seed migration history: %v", err)
	}
	for id := 1; id <= 17; id++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (component, id, applied_at)
			VALUES ('task', $1, now())`, id); err != nil {
			t.Fatalf("record migration %d: %v", id, err)
		}
	}

	rows := []struct{ id, projects, board, typeID, parent string }{
		{"cccc000000000001", `["web","api"]`, "default", "bugfix", ""},
		{"cccc000000000002", `[]`, "default", "", ""},
		{"cccc000000000003", `["infra"]`, "hotfix", "", "cccc000000000001"},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, dbpkg.Rebind(
			`INSERT INTO tasks (id, project_ids, title, stage, board_id, type_id, parent_id, created_at, updated_at)
			 VALUES (?,?,?, 'backlog', ?,?,?, now(), now())`),
			r.id, r.projects, "prod "+r.id, r.board, r.typeID, r.parent); err != nil {
			t.Fatalf("seed row %s: %v", r.id, err)
		}
	}

	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore on a production-shaped database: %v", err)
	}

	if got := projectsOf(t, store, "cccc000000000001"); !equalSlices(got, []string{"web", "api"}) {
		t.Errorf("row 1 = %v, want [web api]", got)
	}
	if got := projectsOf(t, store, "cccc000000000002"); len(got) != 0 {
		t.Errorf("row 2 = %v, want none", got)
	}
	if got := projectsOf(t, store, "cccc000000000003"); !equalSlices(got, []string{"infra"}) {
		t.Errorf("row 3 = %v, want [infra]", got)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = 'tasks' AND column_name = 'project_ids'`).
		Scan(&n); err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if n != 0 {
		t.Error("project_ids survived the migration — two sources of truth for the same relation")
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List after migration: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List returned %d rows, want 3", len(all))
	}

	got, err := store.Get(ctx, "cccc000000000003")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BoardID != "hotfix" || got.ParentID != "cccc000000000001" {
		t.Errorf("row 3 lost fields in the migration: board=%q parent=%q", got.BoardID, got.ParentID)
	}
}

func seedLegacyTasksTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE tasks (
		id                 TEXT PRIMARY KEY,
		project_ids        TEXT NOT NULL DEFAULT '[]',
		title              TEXT NOT NULL,
		body               TEXT NOT NULL DEFAULT '',
		stage              TEXT NOT NULL,
		stage_assignments  TEXT NOT NULL DEFAULT '{}',
		branch_name        TEXT NOT NULL DEFAULT '',
		worktree_paths     TEXT NOT NULL DEFAULT '{}',
		pr_urls            TEXT NOT NULL DEFAULT '{}',
		current_run_id     TEXT NOT NULL DEFAULT '',
		current_instance   TEXT NOT NULL DEFAULT '',
		tags               TEXT NOT NULL DEFAULT '[]',
		priority           TEXT NOT NULL DEFAULT '',
		created_at         TIMESTAMP NOT NULL,
		updated_at         TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy tasks table: %v", err)
	}
}

func TestProjectLinks_LookupIsExactNotSubstring(t *testing.T) {
	s := newTestStore(t)
	exact := &Task{WorkspaceID: testWS, Title: "exact", ProjectIDs: []string{"api"}}
	longer := &Task{WorkspaceID: testWS, Title: "longer", ProjectIDs: []string{"api-gateway"}}
	for _, tk := range []*Task{exact, longer} {
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	got, err := s.ListByProject(t.Context(), "api")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 1 || got[0].ID != exact.ID {
		t.Fatalf("ListByProject(api) = %d rows, want just the exact match", len(got))
	}

	if got, err = s.ListByProject(t.Context(), "api-gateway"); err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 1 || got[0].ID != longer.ID {
		t.Errorf("ListByProject(api-gateway) = %+v", got)
	}
}

func TestProjectLinks_LookupWithNoMatches(t *testing.T) {
	s := newTestStore(t)
	if err := s.Create(t.Context(), &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"web"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.ListByProject(t.Context(), "nothing-here")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d rows, want none", len(got))
	}
}

func TestProjectLinks_LookupHandlesAwkwardIDs(t *testing.T) {
	s := newTestStore(t)
	for _, pid := range []string{`100%_real`, `with"quote`, `back\slash`} {
		tk := &Task{WorkspaceID: testWS, Title: "t " + pid, ProjectIDs: []string{pid}}
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("Create %q: %v", pid, err)
		}
		got, err := s.ListByProject(t.Context(), pid)
		if err != nil {
			t.Fatalf("ListByProject(%q): %v", pid, err)
		}
		if len(got) != 1 || got[0].ID != tk.ID {
			t.Errorf("ListByProject(%q) = %d rows, want exactly the one task", pid, len(got))
		}
	}
}

func TestProjectLinks_LookupReturnsEveryLinkedTask(t *testing.T) {
	s := newTestStore(t)
	var want []string
	for i := range 5 {
		tk := &Task{WorkspaceID: testWS, Title: fmt.Sprintf("t%d", i), ProjectIDs: []string{"web", "api"}}
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("Create: %v", err)
		}
		want = append(want, tk.ID)
	}
	got, err := s.ListByProject(t.Context(), "web")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
}
