package testpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

const selfTemplateSQL = `CREATE TABLE tpl_marker (v TEXT NOT NULL); INSERT INTO tpl_marker VALUES ('from template')`

func selfTemplate() Template {
	sum := sha256.Sum256([]byte(selfTemplateSQL))
	return Template{
		Name: "testpgself",
		Key:  hex.EncodeToString(sum[:]),
		Build: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, selfTemplateSQL)
			return err
		},
	}
}

func TestNewFromTemplateCopiesTheSchemaAndData(t *testing.T) {
	a := NewFromTemplate(t, selfTemplate())
	b := NewFromTemplate(t, selfTemplate())

	for name, db := range map[string]*sql.DB{"a": a, "b": b} {
		var v string
		if err := db.QueryRow(`SELECT v FROM tpl_marker`).Scan(&v); err != nil {
			t.Fatalf("%s: read marker: %v", name, err)
		}
		if v != "from template" {
			t.Errorf("%s: marker = %q", name, v)
		}
	}
	if _, err := a.Exec(`INSERT INTO tpl_marker VALUES ('only in a')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM tpl_marker`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("b sees %d rows; a write to one copy leaked into another", n)
	}
}

func TestEnsureTemplatePrunesOnlyStaleLeftovers(t *testing.T) {
	ctx := context.Background()
	admin, err := sql.Open("pgx", baseDSN())
	if err != nil {
		t.Fatal(err)
	}
	// Closed by a Cleanup, not a defer: the later Cleanup that drops the databases needs admin
	// open.
	t.Cleanup(func() { admin.Close() })
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}

	suffix := randHex(t)
	stale := templatePrefix + "prunetest_" + suffix + "_old"
	fresh := templatePrefix + "prunetest_" + suffix + "_new"
	orphan := buildPrefix + suffix
	// Set up under the template lock: until the COMMENT lands these look stale, and another
	// package's ensureTemplate would drop them.
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(fmt.Sprintf("SELECT pg_advisory_lock(%d)", templateLockKey))
	for _, name := range []string{stale, fresh, orphan} {
		exec("CREATE DATABASE " + name)
	}
	t.Cleanup(func() {
		for _, name := range []string{stale, fresh, orphan} {
			_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("ALTER DATABASE %s WITH IS_TEMPLATE false", name))
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name)
		}
	})
	exec(fmt.Sprintf("ALTER DATABASE %s WITH IS_TEMPLATE true", stale))
	exec(fmt.Sprintf("COMMENT ON DATABASE %s IS '%s'", stale, time.Now().Add(-2*staleAfter).UTC().Format(time.RFC3339)))
	exec(fmt.Sprintf("COMMENT ON DATABASE %s IS '%s'", fresh, time.Now().UTC().Format(time.RFC3339)))
	exec(fmt.Sprintf("SELECT pg_advisory_unlock(%d)", templateLockKey))
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	tpl := selfTemplate()
	if err := ensureTemplateOnServer(t, baseDSN(), admin, &tpl, tpl.dbName()); err != nil {
		t.Fatal(err)
	}

	exists := func(name string) bool {
		var ok bool
		if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists(stale) {
		t.Errorf("%s was last used %s ago and survived the prune", stale, 2*staleAfter)
	}
	if exists(orphan) {
		t.Errorf("orphaned build database %s survived the prune", orphan)
	}
	if !exists(fresh) {
		t.Errorf("%s was used just now and got pruned", fresh)
	}
	if !exists(tpl.dbName()) {
		t.Errorf("the current template %s is gone", tpl.dbName())
	}
}
