package schematest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/schema"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

// Must match internal/daemon openStores: some migrations inspect another component's columns
// (usage's backfill reads task tables, stage migration 4 reads tasks). Every component must be
// listed.
var bootOrder = []string{
	usage.MigrationComponent,
	project.MigrationComponent,
	task.MigrationComponent,
	settings.MigrationComponent,
	sandbox.MigrationComponent,
	secrets.MigrationComponent,
	credentials.MigrationComponent,
	mcpoauth.MigrationComponent,
	chat.MigrationComponent,
	chatruntime.MigrationComponent,
	auth.MigrationComponent,
	audit.MigrationComponent,
	webhook.MigrationComponent,
	entitlements.MigrationComponent,
	mcp.MigrationComponent,
	agentstore.MigrationComponent,
	githubapp.MigrationComponent,
	stage.MigrationComponent,
	envmetrics.MigrationComponent,
	ingest.MigrationComponent,
	automation.MigrationComponent,
	evidence.MigrationComponent,
	runner.MigrationComponent,
	tokenopt.MigrationComponent,
	registry.MigrationComponent,
}

// Bump when build changes how it applies the schema, so old templates stop being used.
const builderVersion = 1

func NewDB(t *testing.T) *sql.DB {
	t.Helper()
	key, err := fingerprint()
	if err != nil {
		//i18n:ignore test helper output
		t.Fatalf("schematest: %v", err)
	}
	return testpg.NewFromTemplate(t, testpg.Template{Name: "full", Key: key, Build: build})
}

func NewBootedDB(t *testing.T, board []types.StageInfo) *sql.DB {
	t.Helper()
	key, err := bootedFingerprint(board)
	if err != nil {
		//i18n:ignore test helper output
		t.Fatalf("schematest: %v", err)
	}
	return testpg.NewFromTemplate(t, testpg.Template{Name: "booted", Key: key, Build: func(ctx context.Context, db *sql.DB) error {
		if buildErr := build(ctx, db); buildErr != nil {
			return buildErr
		}
		return seed(ctx, db, board)
	}})
}

// Bump when seed changes which constructors it runs or what it writes besides their seeds.
const seedVersion = 1

func bootedFingerprint(board []types.StageInfo) (string, error) {
	schemaKey, err := fingerprint()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	//i18n:ignore hash input, not a message
	fmt.Fprintf(h, "schema %s\nseed %d\nlocale %s\n", schemaKey, seedVersion, i18n.DaemonLocale())
	parts := []struct {
		name string
		v    any
	}{
		{"settings", settings.DefaultValues()},
		{"pricing", usage.DefaultPricing()},
		{"stages", stage.Defaults()},
		{"board", board},
		{"default_board", []string{i18n.Text("stage.labels.default_board"), i18n.Text("stage.descriptions.default_board")}},
	}
	for _, p := range parts {
		b, mErr := json.Marshal(p.v)
		if mErr != nil {
			//i18n:ignore test helper output
			return "", fmt.Errorf("fingerprint %s: %w", p.name, mErr)
		}
		fmt.Fprintf(h, "%s %s\n", p.name, b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func seed(ctx context.Context, db *sql.DB, board []types.StageInfo) error {
	// usage.NewStore imports a usage.json from its data dir; an empty scratch dir keeps the
	// working directory's out.
	dataDir, err := os.MkdirTemp("", "schematest-usage-")
	if err != nil {
		return err
	}
	_, err = usage.NewStore(db, dataDir)
	rmErr := os.RemoveAll(dataDir)
	if err != nil {
		return err
	}
	if rmErr != nil {
		return rmErr
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		return err
	}
	st, err := stage.NewStore(db)
	if err != nil {
		return err
	}
	if board != nil {
		if err := st.Save(ctx, board); err != nil {
			return err
		}
	}
	sett.SetStageBoard(st.Catalog)
	return sett.SeedSandboxPriorities(ctx)
}

func fingerprint() (string, error) {
	if err := checkBootOrder(); err != nil {
		return "", err
	}
	all, err := schema.All()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	//i18n:ignore hash input, not a message
	fmt.Fprintf(h, "builder %d\n", builderVersion)
	for _, name := range bootOrder {
		//i18n:ignore hash input, not a message
		fmt.Fprintf(h, "order %s\n", name)
	}
	for _, stmt := range task.BaseSchema() {
		sum := sha256.Sum256([]byte(stmt))
		//i18n:ignore hash input, not a message
		fmt.Fprintf(h, "task base %x\n", sum)
	}
	for _, e := range all {
		fmt.Fprintf(h, "%s %d %s\n", e.Component, e.ID, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checkBootOrder() error {
	listed := map[string]bool{}
	for _, name := range bootOrder {
		listed[name] = true
	}
	for _, c := range schema.Components() {
		if !listed[c.Name] {
			//i18n:ignore test helper output
			return fmt.Errorf("component %q is missing from schematest.bootOrder", c.Name)
		}
	}
	if len(listed) != len(schema.Components()) {
		//i18n:ignore test helper output
		return fmt.Errorf("schematest.bootOrder lists %d components, the registry %d", len(listed), len(schema.Components()))
	}
	return nil
}

// Two passes, like a second boot: migrations guarded on other components' columns skipped the first
// time
// (usage's backfill runs before task tables exist) run now, so the template is a fixed point.
func build(ctx context.Context, db *sql.DB) error {
	lists := map[string]func(migrate.Inspector) ([]migrate.Migration, error){}
	for _, c := range schema.Components() {
		lists[c.Name] = c.List
	}
	for range 2 {
		for _, name := range bootOrder {
			if name == task.MigrationComponent {
				// The task store creates its base tables outside its migration list; only its
				// constructor does both.
				if _, err := task.NewStore(db, ""); err != nil {
					return err
				}
				continue
			}
			list, err := lists[name](migrate.DBInspector(ctx, db))
			if err != nil {
				return err
			}
			if err := migrate.Run(ctx, db, name, list); err != nil {
				return err
			}
		}
	}
	return nil
}
