package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func dsn(t *testing.T) string {
	t.Helper()
	base := os.Getenv("OCTOPUS_TEST_POSTGRES_DSN")
	if base == "" {
		base = testpg.DefaultDSN
	}
	return base
}

func TestOpen_AppliesAStatementTimeout(t *testing.T) {
	testpg.New(t)
	pool, err := Open(dsn(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var got string
	if err := pool.QueryRowContext(context.Background(), `SHOW statement_timeout`).Scan(&got); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if got == "0" || got == "" {
		t.Errorf("statement_timeout = %q — a query can still run forever", got)
	}
}

func TestOpen_AppliesALockTimeout(t *testing.T) {
	testpg.New(t)
	pool, err := Open(dsn(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var got string
	if err := pool.QueryRowContext(context.Background(), `SHOW lock_timeout`).Scan(&got); err != nil {
		t.Fatalf("SHOW lock_timeout: %v", err)
	}
	if got == "0" || got == "" {
		t.Errorf("lock_timeout = %q — a writer can wait on a row lock forever", got)
	}
}

func TestOpen_StatementTimeoutFires(t *testing.T) {
	db := testpg.New(t)
	if _, err := db.ExecContext(context.Background(), `SET statement_timeout = '250ms'`); err != nil {
		t.Fatalf("set: %v", err)
	}
	start := time.Now()
	_, err := db.ExecContext(context.Background(), `SELECT pg_sleep(5)`)
	if err == nil {
		t.Fatal("a 5s query completed under a 250ms statement timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("query ran for %v before being cut off", elapsed)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") &&
		!strings.Contains(strings.ToLower(err.Error()), "canceling") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpen_LockTimeoutFires(t *testing.T) {
	db := testpg.New(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE locked (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO locked (id) VALUES ('x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.ExecContext(ctx, `SELECT id FROM locked WHERE id = 'x' FOR UPDATE`); err != nil {
		t.Fatalf("lock: %v", err)
	}

	waiter, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin waiter: %v", err)
	}
	defer func() { _ = waiter.Rollback() }()
	if _, err := waiter.ExecContext(ctx, `SET LOCAL lock_timeout = '250ms'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}

	start := time.Now()
	_, err = waiter.ExecContext(ctx, `SELECT id FROM locked WHERE id = 'x' FOR UPDATE`)
	if err == nil {
		t.Fatal("the waiter acquired a lock the holder never released")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("waited %v before giving up", elapsed)
	}
}

func TestOpen_RejectsNonPostgresDSN(t *testing.T) {
	for _, bad := range []string{"", "sqlite:///tmp/x.db", "/tmp/octopus.db", "mysql://u:p@h/db"} {
		if _, err := Open(bad); err == nil {
			t.Errorf("Open(%q) accepted a non-postgres DSN", bad)
		}
	}
}

func TestOpen_KeepsExistingDSNParameters(t *testing.T) {
	testpg.New(t)
	base := dsn(t)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	pool, err := Open(base + sep + "application_name=octopus_test_probe")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var name string
	if err := pool.QueryRowContext(context.Background(), `SHOW application_name`).Scan(&name); err != nil {
		t.Fatalf("SHOW application_name: %v", err)
	}
	if name != "octopus_test_probe" {
		t.Errorf("application_name = %q — Open dropped a caller's DSN parameter", name)
	}
}

func TestOpen_DoesNotOverrideAnExplicitTimeout(t *testing.T) {
	testpg.New(t)
	base := dsn(t)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	pool, err := Open(base + sep + "statement_timeout=7000")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var got string
	if err := pool.QueryRowContext(context.Background(), `SHOW statement_timeout`).Scan(&got); err != nil {
		t.Fatalf("SHOW: %v", err)
	}
	if got != "7s" {
		t.Errorf("statement_timeout = %q, want the operator's 7s", got)
	}
}
