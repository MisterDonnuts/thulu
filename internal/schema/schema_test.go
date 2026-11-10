package schema

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
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
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

const modulePath = "github.com/MisterDonnuts/thulu"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

func TestEveryRunIsRegistered(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	running := map[string]bool{}

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == filepath.Join(root, "internal", "migrate") ||
				path == filepath.Join(root, "internal", "schema", "schematest") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "migrate" {
				return true
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			running[modulePath+"/"+filepath.ToSlash(rel)] = true
			if len(call.Args) < 3 {
				t.Errorf("%s: migrate.Run with %d arguments", fset.Position(call.Pos()), len(call.Args))
				return true
			}
			if id, ok := call.Args[2].(*ast.Ident); !ok || id.Name != "MigrationComponent" {
				t.Errorf("%s: migrate.Run must name its component with MigrationComponent, so the registry and the database agree on it",
					fset.Position(call.Pos()))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	registered := map[string]bool{}
	for _, c := range Components() {
		fn := runtime.FuncForPC(reflect.ValueOf(c.List).Pointer()).Name()
		registered[fn[:strings.LastIndex(fn, ".")]] = true
	}
	for pkg := range running {
		if !registered[pkg] {
			t.Errorf("%s runs migrations but is not in schema.Components()", pkg)
		}
	}
	for pkg := range registered {
		if !running[pkg] {
			t.Errorf("schema.Components() lists %s, which never calls migrate.Run", pkg)
		}
	}
}

func TestComponentsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Components() {
		if c.Name == "" {
			t.Error("component with an empty name")
		}
		if seen[c.Name] {
			t.Errorf("component %q registered twice", c.Name)
		}
		seen[c.Name] = true

		list, err := c.List(migrate.Everything)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if len(list) == 0 {
			t.Errorf("%s: empty migration list", c.Name)
		}
		for i, m := range list {
			if strings.TrimSpace(m.SQL) == "" {
				t.Errorf("%s#%d: empty SQL", c.Name, m.ID)
			}
			if i > 0 && m.ID <= list[i-1].ID {
				t.Errorf("%s: migration %d follows %d; IDs must be strictly increasing", c.Name, m.ID, list[i-1].ID)
			}
		}
	}
}

func TestAllHashesSQL(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no migrations registered")
	}
	for _, e := range all {
		if len(e.SHA256) != 64 {
			t.Errorf("%s#%d: SHA256 %q is not a hex sha256", e.Component, e.ID, e.SHA256)
		}
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		if all[i].Component != all[j].Component {
			return all[i].Component < all[j].Component
		}
		return all[i].ID < all[j].ID
	}) {
		t.Error("All() is not sorted by component, then ID")
	}
}

func TestBootRecordsOnlyRegisteredMigrations(t *testing.T) {
	db := testpg.New(t)
	dir := t.TempDir()

	opens := []func() error{
		func() error { _, err := usage.NewStore(db, dir); return err },
		func() error { _, err := project.NewStore(db, filepath.Join(dir, "projects")); return err },
		func() error { _, err := task.NewStore(db, filepath.Join(dir, "task-attachments")); return err },
		func() error { _, err := settings.NewStore(db); return err },
		func() error { _, err := sandbox.NewStore(db); return err },
		func() error { _, err := secrets.NewStore(db); return err },
		func() error { _, err := credentials.NewStore(db); return err },
		func() error { _, err := mcpoauth.New(db, nil); return err },
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
		func() error { _, err := evidence.NewStore(db); return err },
		func() error { _, err := runner.NewStore(db); return err },
		func() error { return tokenopt.Migrate(db) },
		func() error { _, err := registry.NewStore(db); return err },
	}
	for i, open := range opens {
		if err := open(); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}

	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		component string
		id        int
	}
	registered := map[key]bool{}
	for _, e := range all {
		registered[key{e.Component, e.ID}] = true
	}

	rows, err := db.QueryContext(context.Background(), `SELECT component, id FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	recorded := map[string]int{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.component, &k.id); err != nil {
			t.Fatal(err)
		}
		recorded[k.component]++
		if !registered[k] {
			t.Errorf("schema_migrations recorded %s#%d, which the registry does not list", k.component, k.id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, c := range Components() {
		if recorded[c.Name] == 0 {
			t.Errorf("component %s is registered but recorded no migration at boot", c.Name)
		}
	}
}
