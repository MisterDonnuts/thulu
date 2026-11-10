package stage

import (
	"context"
	"database/sql"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func legacyDB(t *testing.T, boards []string, tasks map[string]string, types map[string]string) *sql.DB {
	t.Helper()
	db := testpg.New(t)
	if _, err := db.Exec(`CREATE TABLE tasks (
		id           TEXT PRIMARY KEY,
		board_id     TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	if _, err := NewStore(db); err != nil {
		t.Fatalf("NewStore (first boot): %v", err)
	}
	for _, id := range boards {
		if _, err := db.Exec(
			`INSERT INTO boards (id, label, description, workspace_id, position, is_default)
			 VALUES ($1, $1, '', '', 20, 0)`, id); err != nil {
			t.Fatalf("insert legacy board %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`UPDATE boards SET workspace_id = ''`); err != nil {
		t.Fatalf("unstamp boards: %v", err)
	}
	for boardID, wid := range tasks {
		if _, err := db.Exec(
			`INSERT INTO tasks (id, board_id, workspace_id) VALUES ($1, $2, $3)`,
			boardID+"-task-", boardID, wid); err != nil {
			t.Fatalf("insert task on %s: %v", boardID, err)
		}
	}
	for typeID, boardID := range types {
		if _, err := db.Exec(
			`INSERT INTO task_types (id, label, workspace_id, position) VALUES ($1, $1, '', 10)`,
			typeID); err != nil {
			t.Fatalf("insert legacy type %s: %v", typeID, err)
		}
		if _, err := db.Exec(
			`INSERT INTO board_task_types (board_id, type_id, position, is_default)
			 VALUES ($1, $2, 10, 0)`, boardID, typeID); err != nil {
			t.Fatalf("insert type membership %s: %v", typeID, err)
		}
	}
	if _, err := db.Exec(
		`DELETE FROM schema_migrations WHERE component = 'stages' AND id = 4`); err != nil {
		t.Fatalf("rewind migration 4: %v", err)
	}
	return db
}

func boardWorkspace(t *testing.T, store *Store, id string) string {
	t.Helper()
	for _, b := range store.BoardList() {
		if b.ID == id {
			return b.WorkspaceID
		}
	}
	t.Fatalf("board %q is gone", id)
	return ""
}

func TestAdoptionClaimsLegacyBoards(t *testing.T) {
	db := legacyDB(t,
		[]string{"acme", "ideas"},
		map[string]string{"acme": "wsa", "default": "wsb"},
		map[string]string{"bugfix": "acme"})
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore (upgrade boot): %v", err)
	}

	if got := boardWorkspace(t, store, "acme"); got != "wsa" {
		t.Errorf("acme adopted by %q, want wsa — its tasks say who owns it", got)
	}
	if got := boardWorkspace(t, store, DefaultBoardID); got != "wsb" {
		t.Errorf("default board adopted by %q, want wsb", got)
	}
	if got := boardWorkspace(t, store, "ideas"); got == "" {
		t.Error("an empty board stayed global — every tenant can still read it")
	}

	for _, b := range store.BoardList() {
		if b.WorkspaceID == "" {
			t.Errorf("board %q survived adoption unowned", b.ID)
		}
	}
	var typeWID string
	if err := db.QueryRow(`SELECT workspace_id FROM task_types WHERE id = 'bugfix'`).Scan(&typeWID); err != nil {
		t.Fatalf("read task type: %v", err)
	}
	if typeWID != "wsa" {
		t.Errorf("task type bugfix adopted by %q, want wsa — the board offering it is wsa's", typeWID)
	}
}

func TestAdoptionFallsBackToTheOnlyWorkspace(t *testing.T) {
	db := legacyDB(t, []string{"acme"}, map[string]string{"acme": ""}, nil)
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create workspaces: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('the-only-one')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, id := range []string{"acme", DefaultBoardID} {
		if got := boardWorkspace(t, store, id); got != "the-only-one" {
			t.Errorf("board %s adopted by %q, want the install's only workspace", id, got)
		}
	}
}

func TestAdoptionResolvesASharedBoard(t *testing.T) {
	db := legacyDB(t, []string{"shared"},
		map[string]string{"shared": "wsa", "default": "wsb"}, nil)
	for _, id := range []string{"s2", "s3"} {
		if _, err := db.Exec(
			`INSERT INTO tasks (id, board_id, workspace_id) VALUES ($1, 'shared', 'wsb')`, id); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := boardWorkspace(t, store, "shared"); got != "wsb" {
		t.Errorf("shared board adopted by %q, want wsb (2 tasks against 1)", got)
	}
}

func TestAdoptionGivesEachWorkspaceADefault(t *testing.T) {
	db := legacyDB(t, []string{"acme"},
		map[string]string{"acme": "wsa", "default": "wsb"}, nil)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seen := map[string]int{}
	for _, b := range store.BoardList() {
		if b.Default {
			seen[b.WorkspaceID]++
		}
	}
	for _, wid := range []string{"wsa", "wsb"} {
		if seen[wid] != 1 {
			t.Errorf("workspace %s has %d default boards, want exactly 1", wid, seen[wid])
		}
	}
	if got := store.DefaultBoardIDFor("wsa"); got != "acme" {
		t.Errorf("DefaultBoardIDFor(wsa) = %q, want acme", got)
	}
	if got := store.DefaultBoardIDFor("wsb"); got != DefaultBoardID {
		t.Errorf("DefaultBoardIDFor(wsb) = %q, want %q", got, DefaultBoardID)
	}
}

func TestEnsureDefaultBoardSeedsOncePerWorkspace(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first, err := store.EnsureDefaultBoard(ctx, "a1b2c3d4e5f60718")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if first.ID == DefaultBoardID {
		t.Error("the seed handed out the board that already belongs to another workspace")
	}
	if !first.Default || first.WorkspaceID != "a1b2c3d4e5f60718" {
		t.Errorf("seeded board = %+v, want the workspace's own default", first)
	}
	if got := len(store.Board(first.ID).List()); got != len(Defaults()) {
		t.Errorf("seeded board has %d stages, want the built-in %d", got, len(Defaults()))
	}

	again, err := store.EnsureDefaultBoard(ctx, "a1b2c3d4e5f60718")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard (second call): %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("second call seeded %q, want the existing %q", again.ID, first.ID)
	}
	if n := len(store.BoardList()); n != 2 {
		t.Errorf("%d boards after two calls, want 2", n)
	}
	if got := store.DefaultBoardIDFor(FallbackWorkspaceID); got != DefaultBoardID {
		t.Errorf("DefaultBoardIDFor(%q) = %q, want %q", FallbackWorkspaceID, got, DefaultBoardID)
	}
}

func TestEnsureDefaultBoardKeepsEditedStageLabels(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	edited := store.DefaultBoard().List()
	edited[0].Label = "Inbox"
	if err := store.Save(ctx, edited); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.EnsureDefaultBoard(ctx, "newcomer"); err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if got := store.DefaultBoard().List()[0].Label; got != "Inbox" {
		t.Errorf("first column of the existing board = %q, want the edited \"Inbox\"", got)
	}
	if got, want := store.Board(store.DefaultBoardIDFor("newcomer")).List()[0].Label, Defaults()[0].Label; got != want {
		t.Errorf("seeded board's first column = %q, want the built-in %q", got, want)
	}
}

func TestSetDefaultBoardLeavesOtherWorkspacesAlone(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	other, err := store.EnsureDefaultBoard(ctx, "wsa")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if err := store.CreateBoard(ctx,
		BoardInfo{ID: "alt", Label: "Alt", WorkspaceID: FallbackWorkspaceID},
		twoStageBoard("alt")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.SetDefaultBoard(ctx, "alt"); err != nil {
		t.Fatalf("SetDefaultBoard: %v", err)
	}
	if got := store.DefaultBoardIDFor("wsa"); got != other.ID {
		t.Errorf("wsa's default became %q, want %q", got, other.ID)
	}
	if got := store.DefaultBoardIDFor(FallbackWorkspaceID); got != "alt" {
		t.Errorf("default workspace's default = %q, want alt", got)
	}
}

func TestBoardCapIsPerWorkspace(t *testing.T) {
	store := newTestStore(t)
	if got := store.workspaceBoardCountLocked(FallbackWorkspaceID); got != 1 {
		t.Fatalf("seeded install has %d boards in %q, want 1", got, FallbackWorkspaceID)
	}
	if got := store.workspaceBoardCountLocked("wsa"); got != 0 {
		t.Errorf("workspace wsa starts with %d boards, want 0", got)
	}
}

func TestSeedStampsTheOnlyWorkspace(t *testing.T) {
	db := testpg.New(t)
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create workspaces: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('the-only-one')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := boardWorkspace(t, store, DefaultBoardID); got != "the-only-one" {
		t.Errorf("seeded board stamped %q, want the install's only workspace", got)
	}
}

func TestEnsureDefaultBoardAdoptsTheSeededBoard(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := boardWorkspace(t, store, DefaultBoardID); got != FallbackWorkspaceID {
		t.Fatalf("seeded board stamped %q, want the fallback with no workspace to name", got)
	}
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create workspaces: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('signed-up-later')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	b, err := store.EnsureDefaultBoard(context.Background(), "signed-up-later")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if b.ID != DefaultBoardID {
		t.Errorf("minted board %q instead of adopting the shipped one", b.ID)
	}
	if n := len(store.BoardList()); n != 1 {
		t.Errorf("%d boards after adoption, want the single shipped one", n)
	}
	if got := boardWorkspace(t, store, DefaultBoardID); got != "signed-up-later" {
		t.Errorf("shipped board owned by %q after adoption, want signed-up-later", got)
	}
}

func TestEnsureDefaultBoardDoesNotAdoptOnAMultiTenantInstall(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create workspaces: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('wsa'), ('wsb')`); err != nil {
		t.Fatalf("insert workspaces: %v", err)
	}

	b, err := store.EnsureDefaultBoard(context.Background(), "wsa")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if b.ID == DefaultBoardID {
		t.Error("wsa took the shipped board on an install where it is not the only tenant")
	}
	if got := boardWorkspace(t, store, DefaultBoardID); got != FallbackWorkspaceID {
		t.Errorf("shipped board moved to %q on a multi-tenant install", got)
	}
}
