package migrate

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = current_schema() AND table_name = $1`, name).Scan(&n)
	if err != nil {
		t.Fatalf("tableExists(%q): %v", name, err)
	}
	return n > 0
}

func TestRun_DuplicateInsideMultiStatementIsNotTolerated(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE already_here (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	migs := []Migration{{ID: 1, SQL: `
		CREATE TABLE already_here (id INTEGER PRIMARY KEY);
		CREATE TABLE brand_new (id INTEGER PRIMARY KEY);`}}

	err := Run(context.Background(), db, "demo", migs)
	if err == nil {
		t.Fatal("multi-statement migration whose first statement already exists was accepted")
	}
	if !strings.Contains(err.Error(), "demo#1") {
		t.Errorf("error should name the migration, got: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 0 {
		t.Errorf("applied = %d, want 0 — a rolled-back migration must not be recorded", got)
	}
	if tableExists(t, db, "brand_new") {
		t.Error("brand_new exists — test assumption wrong, the statements did not share a transaction")
	}
}

func TestRun_FailureLeavesNoTrace(t *testing.T) {
	db := openDB(t)
	migs := []Migration{{ID: 1, SQL: `
		CREATE TABLE half_done (id INTEGER PRIMARY KEY);
		THIS IS NOT VALID SQL;`}}

	if err := Run(context.Background(), db, "demo", migs); err == nil {
		t.Fatal("expected an error")
	}
	if got := countApplied(t, db, "demo"); got != 0 {
		t.Errorf("applied = %d, want 0", got)
	}
	if tableExists(t, db, "half_done") {
		t.Error("half_done survived a failed migration — the DDL was not rolled back")
	}
}

func TestRun_EarlierMigrationsSurviveALaterFailure(t *testing.T) {
	db := openDB(t)
	migs := []Migration{
		{ID: 1, SQL: `CREATE TABLE fine (id INTEGER PRIMARY KEY)`},
		{ID: 2, SQL: `NOT SQL AT ALL`},
	}
	if err := Run(context.Background(), db, "demo", migs); err == nil {
		t.Fatal("expected an error from migration 2")
	}
	if !tableExists(t, db, "fine") {
		t.Error("migration 1 was rolled back by migration 2's failure")
	}
	if got := countApplied(t, db, "demo"); got != 1 {
		t.Errorf("applied = %d, want 1", got)
	}
}

func TestRun_LegacyAddColumnIsStillBaselined(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	migs := []Migration{{ID: 1, SQL: `ALTER TABLE t ADD COLUMN name TEXT`}}
	if err := Run(context.Background(), db, "demo", migs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 1 {
		t.Errorf("applied = %d, want 1", got)
	}
}

func TestRun_DuplicateTableIsNotBaselined(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	migs := []Migration{{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`}}
	if err := Run(context.Background(), db, "demo", migs); err == nil {
		t.Fatal("a duplicate CREATE TABLE was silently baselined")
	}
	if got := countApplied(t, db, "demo"); got != 0 {
		t.Errorf("applied = %d, want 0", got)
	}
}

func TestRun_AddColumnIfNotExistsNeedsNoTolerance(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	migs := []Migration{
		{ID: 1, SQL: `ALTER TABLE t ADD COLUMN IF NOT EXISTS name TEXT`},
		{ID: 2, SQL: `ALTER TABLE t ADD COLUMN IF NOT EXISTS extra TEXT NOT NULL DEFAULT ''`},
	}
	if err := Run(context.Background(), db, "demo", migs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, name, extra) VALUES (1, 'a', 'b')`); err != nil {
		t.Fatalf("both columns should exist: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 2 {
		t.Errorf("applied = %d, want 2", got)
	}
}

func TestColumnExists_IgnoresOtherSchemas(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed public.t: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA elsewhere`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE elsewhere.t (id INTEGER PRIMARY KEY, legacy_col TEXT)`); err != nil {
		t.Fatalf("seed elsewhere.t: %v", err)
	}

	got, err := ColumnExists(ctx, db, "t", "legacy_col")
	if err != nil {
		t.Fatalf("ColumnExists: %v", err)
	}
	if got {
		t.Error("ColumnExists found a column that only exists in another schema")
	}
}

func TestRun_EmptyMigrationListIsANoOp(t *testing.T) {
	db := openDB(t)
	if err := Run(context.Background(), db, "demo", nil); err != nil {
		t.Fatalf("Run with no migrations: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 0 {
		t.Errorf("applied = %d, want 0", got)
	}
}

func TestRun_ComponentsAreIndependent(t *testing.T) {
	db := openDB(t)
	if err := Run(context.Background(), db, "alpha", []Migration{
		{ID: 1, SQL: `CREATE TABLE alpha_t (id INTEGER PRIMARY KEY)`},
	}); err != nil {
		t.Fatalf("alpha: %v", err)
	}
	if err := Run(context.Background(), db, "beta", []Migration{
		{ID: 1, SQL: `CREATE TABLE beta_t (id INTEGER PRIMARY KEY)`},
	}); err != nil {
		t.Fatalf("beta: %v", err)
	}
	if got := countApplied(t, db, "alpha"); got != 1 {
		t.Errorf("alpha applied = %d, want 1", got)
	}
	if got := countApplied(t, db, "beta"); got != 1 {
		t.Errorf("beta applied = %d, want 1", got)
	}
	if !tableExists(t, db, "alpha_t") || !tableExists(t, db, "beta_t") {
		t.Error("both components' tables should exist")
	}
}
