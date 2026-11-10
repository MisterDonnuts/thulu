package testpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver
)

// Postgres of the container stack on :5433 (`make up` or `make dev`); the credential is a
// well-known dev placeholder, not a secret.
//
//nolint:gosec // G101: known dev/CI credential, documented above.
const DefaultDSN = "postgres://octopus:octopus@localhost:5433/octopus?sslmode=disable" //i18n:ignore DSN, not a message

func New(t *testing.T) *sql.DB {
	t.Helper()
	return provision(t, nil)
}

func provision(t *testing.T, tpl *Template) *sql.DB {
	t.Helper()
	base := baseDSN()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	admin, err := sql.Open("pgx", base)
	if err != nil {
		//i18n:ignore test helper output
		t.Skipf("testpg: open admin %q: %v", base, err)
	}
	if pingErr := admin.PingContext(ctx); pingErr != nil {
		if cerr := admin.Close(); cerr != nil {
			//i18n:ignore test helper output
			t.Logf("testpg: close after ping fail: %v", cerr)
		}
		//i18n:ignore test helper output
		t.Skipf("testpg: postgres unreachable at %s (start the pg compose service or set OCTOPUS_TEST_POSTGRES_DSN): %v", base, pingErr)
	}

	dbName := randDBName(t)
	// Identifiers cannot be bound: dbName is crypto/rand hex and the template name is validated in
	// NewFromTemplate.
	create := fmt.Sprintf("CREATE DATABASE %s", dbName)
	if tpl != nil {
		tplName, tplErr := ensureTemplate(t, base, admin, tpl)
		if tplErr != nil {
			if cerr := admin.Close(); cerr != nil {
				//i18n:ignore test helper output
				t.Logf("testpg: close after template fail: %v", cerr)
			}
			//i18n:ignore test helper output
			t.Fatalf("testpg: template: %v", tplErr)
		}
		create += " TEMPLATE " + tplName
		// The first caller may have spent minutes building the template; the copy gets its own
		// deadline.
		var copyCancel context.CancelFunc
		ctx, copyCancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer copyCancel()
	}
	if _, createErr := admin.ExecContext(ctx, create); createErr != nil {
		if cerr := admin.Close(); cerr != nil {
			//i18n:ignore test helper output
			t.Logf("testpg: close after create fail: %v", cerr)
		}
		//i18n:ignore test helper output
		t.Fatalf("testpg: create database %s: %v", dbName, createErr)
	}

	testDSN := withDB(t, base, dbName)
	pool, err := sql.Open("pgx", testDSN)
	if err != nil {
		teardown(ctx, t, admin, nil, dbName)
		//i18n:ignore test helper output
		t.Fatalf("testpg: open test db: %v", err)
	}
	// 25 is the daemon's pool limit (internal/db); unbounded, racing tests run the server out of
	// max_connections.
	maxOpen := 25
	if tpl != nil {
		maxOpen = templateMaxOpenConns
	}
	pool.SetMaxOpenConns(maxOpen)
	if pingErr := pool.PingContext(ctx); pingErr != nil {
		teardown(ctx, t, admin, pool, dbName)
		//i18n:ignore test helper output
		t.Fatalf("testpg: ping test db: %v", pingErr)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		teardown(cleanupCtx, t, admin, pool, dbName)
	})

	return pool
}

func baseDSN() string {
	if dsn := os.Getenv("OCTOPUS_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return DefaultDSN
}

func randDBName(t *testing.T) string {
	t.Helper()
	return "octopus_test_" + randHex(t)
}

func randHex(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		//i18n:ignore test helper output
		t.Fatalf("testpg: rand: %v", err)
	}
	return hex.EncodeToString(buf)
}

func withDB(t *testing.T, base, dbName string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		//i18n:ignore test helper output
		t.Fatalf("testpg: parse base DSN: %v", err)
	}
	u.Path = "/" + dbName
	return u.String()
}

func dropDB(ctx context.Context, admin *sql.DB, dbName string) error {
	_, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	return err
}

func teardown(ctx context.Context, t *testing.T, admin, pool *sql.DB, dbName string) {
	t.Helper()
	if pool != nil {
		if err := pool.Close(); err != nil {
			//i18n:ignore test helper output
			t.Logf("testpg teardown: pool close: %v", err)
		}
	}
	// A goroutine that outlived the test would otherwise make DROP DATABASE fail with "is being
	// accessed by other users".
	terminateQ := `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname=$1 AND pid <> pg_backend_pid()`
	if _, err := admin.ExecContext(ctx, terminateQ, dbName); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg teardown: terminate backends: %v", err)
	}
	if err := dropDB(ctx, admin, dbName); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg teardown: drop db %s: %v", dbName, err)
	}
	if err := admin.Close(); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg teardown: admin close: %v", err)
	}
}
