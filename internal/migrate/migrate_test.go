package migrate

import (
	"context"
	"database/sql"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

func countApplied(t *testing.T, db *sql.DB, component string) int {
	t.Helper()
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE component = $1`, component).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestRunAppliesAndRecords(t *testing.T) {
	db := openDB(t)
	migs := []Migration{
		{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`},
		{ID: 2, SQL: `ALTER TABLE t ADD COLUMN name TEXT NOT NULL DEFAULT ''`},
	}
	if err := Run(context.Background(), db, "demo", migs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 2 {
		t.Fatalf("applied = %d, want 2", got)
	}
	if _, err := db.Exec(`INSERT INTO t (id, name) VALUES (1, 'x')`); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	db := openDB(t)
	migs := []Migration{{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`}}
	for i := 0; i < 3; i++ {
		if err := Run(context.Background(), db, "demo", migs); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if got := countApplied(t, db, "demo"); got != 1 {
		t.Fatalf("applied = %d, want 1", got)
	}
}

func TestRunRefusesIDsOutOfOrder(t *testing.T) {
	db := openDB(t)
	for name, migs := range map[string][]Migration{
		"duplicate": {
			{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`},
			{ID: 1, SQL: `CREATE TABLE u (id INTEGER PRIMARY KEY)`},
		},
		"descending": {
			{ID: 2, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`},
			{ID: 1, SQL: `CREATE TABLE u (id INTEGER PRIMARY KEY)`},
		},
	} {
		if err := Run(context.Background(), db, name, migs); err == nil {
			t.Errorf("%s: Run accepted IDs out of order", name)
		}
		var created bool
		if err := db.QueryRow(`SELECT to_regclass('t') IS NOT NULL`).Scan(&created); err != nil {
			t.Fatal(err)
		}
		if created {
			t.Errorf("%s: Run applied a migration from a list it refused", name)
		}
	}
}

func TestRunBaselinesExistingColumn(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	migs := []Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY)`},
		{ID: 2, SQL: `ALTER TABLE t ADD COLUMN name TEXT`},
	}
	if err := Run(context.Background(), db, "demo", migs); err != nil {
		t.Fatalf("Run on existing db: %v", err)
	}
	if got := countApplied(t, db, "demo"); got != 2 {
		t.Fatalf("applied = %d, want 2 (benign duplicate recorded)", got)
	}
}

func TestColumnExists(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		column string
		want   bool
	}{
		{"name", true},
		{"id", true},
		{"missing", false},
	} {
		got, err := ColumnExists(ctx, db, "t", tc.column)
		if err != nil {
			t.Fatalf("ColumnExists(%q): %v", tc.column, err)
		}
		if got != tc.want {
			t.Errorf("ColumnExists(%q) = %v, want %v", tc.column, got, tc.want)
		}
	}
}

func TestRunSurfacesRealErrors(t *testing.T) {
	db := openDB(t)
	migs := []Migration{{ID: 1, SQL: `THIS IS NOT VALID SQL`}}
	if err := Run(context.Background(), db, "demo", migs); err == nil {
		t.Fatal("expected error on invalid SQL, got nil")
	}
	if got := countApplied(t, db, "demo"); got != 0 {
		t.Fatalf("applied = %d, want 0 after failure", got)
	}
}
