package schematest

import (
	"context"
	"database/sql"
	"maps"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/schema"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func TestBootOrderCoversTheRegistry(t *testing.T) {
	if err := checkBootOrder(); err != nil {
		t.Fatal(err)
	}
}

func TestStoresFindTheTemplateFullyMigrated(t *testing.T) {
	db := NewDB(t)
	ctx := context.Background()
	history := func() int {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	recorded := recordedComponents(t, db)
	for _, c := range schema.Components() {
		if !recorded[c.Name] {
			t.Errorf("the template has no history for %s", c.Name)
		}
	}

	before := history()
	dir := t.TempDir()
	opens := []func() error{
		func() error { _, err := usage.NewStore(db, dir); return err },
		func() error { _, err := project.NewStore(db, filepath.Join(dir, "projects")); return err },
		func() error { _, err := task.NewStore(db, filepath.Join(dir, "attachments")); return err },
		func() error { _, err := settings.NewStore(db); return err },
		func() error { _, err := sandbox.NewStore(db); return err },
		func() error { _, err := secrets.NewStore(db); return err },
		func() error { _, err := credentials.NewStore(db); return err },
		func() error { _, err := chat.NewStore(db); return err },
		func() error { _, err := chatruntime.NewStore(db); return err },
		func() error { _, err := auth.NewUserStore(db); return err },
		func() error { _, err := audit.NewStore(db); return err },
		func() error { _, err := webhook.NewStore(db); return err },
		func() error { _, err := entitlements.NewStore(db, entitlements.PlanSelfHosted); return err },
		func() error { _, err := mcp.NewRegistry(db); return err },
		func() error { _, err := agentstore.New(db); return err },
		func() error { _, err := githubapp.NewStore(db); return err },
		func() error { _, err := stage.NewStore(db); return err },
		func() error { _, err := envmetrics.NewStore(db); return err },
		func() error { _, err := ingest.NewStore(db); return err },
		func() error { _, err := automation.NewStore(db); return err },
		func() error { _, err := registry.NewStore(db); return err },
	}
	for i, open := range opens {
		if err := open(); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if after := history(); after != before {
		t.Errorf("opening the stores applied %d more migrations; the template should already hold them", after-before)
	}
}

func recordedComponents(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT DISTINCT component FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	recorded := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		recorded[c] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return recorded
}

func TestBootedTemplateSeedsOnce(t *testing.T) {
	board := stagetest.Flow()
	db := NewBootedDB(t, board)
	ctx := context.Background()
	counts := func() map[string]int {
		out := map[string]int{}
		for _, table := range []string{"settings", "model_pricing", "stages", "boards", "board_stages"} {
			var n int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[table] = n
		}
		return out
	}
	var stamp string
	if err := db.QueryRowContext(ctx, `SELECT MAX(updated_at)::text FROM settings`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}

	before := counts()
	if before["settings"] < len(settings.DefaultValues())+1 {
		t.Errorf("template has %d settings rows, want every default plus the sandbox priorities", before["settings"])
	}
	if before["boards"] != 1 || before["board_stages"] != len(board) {
		t.Errorf("template has %d boards with %d columns, want 1 with %d", before["boards"], before["board_stages"], len(board))
	}
	if want := len(usage.DefaultPricing()); before["model_pricing"] != want {
		t.Errorf("template has %d pricing rows, want %d", before["model_pricing"], want)
	}

	if err := seed(ctx, db, nil); err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if after := counts(); !maps.Equal(after, before) {
		t.Errorf("second boot changed the seeded rows: %v, was %v", after, before)
	}
	var stampAfter string
	if err := db.QueryRowContext(ctx, `SELECT MAX(updated_at)::text FROM settings`).Scan(&stampAfter); err != nil {
		t.Fatal(err)
	}
	if stampAfter != stamp {
		t.Errorf("second boot rewrote settings (updated_at %s, was %s)", stampAfter, stamp)
	}
	st, err := stage.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(st.Catalog()); got != len(board) {
		t.Errorf("default board has %d stages after a second boot, want %d", got, len(board))
	}
}

func TestBootedFingerprintFollowsTheBoard(t *testing.T) {
	plain, err := bootedFingerprint(nil)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := bootedFingerprint(stagetest.Flow())
	if err != nil {
		t.Fatal(err)
	}
	again, err := bootedFingerprint(stagetest.Flow())
	if err != nil {
		t.Fatal(err)
	}
	if plain == flow {
		t.Error("the board does not change the fingerprint")
	}
	if flow != again {
		t.Error("the same board gave two fingerprints")
	}
}

func TestCopiesAreIndependent(t *testing.T) {
	a, b := NewDB(t), NewDB(t)
	if _, err := a.Exec(`CREATE TABLE only_in_a (x int)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM pg_tables WHERE tablename = 'only_in_a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a table created in one copy showed up in another")
	}
}
