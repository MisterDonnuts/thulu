package testpg

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type Template struct {
	Name string
	// Key must change whenever what Build applies changes: the template database is named after it,
	// so a stale key reuses a stale schema.
	Key string
	// Build must close its connections (CREATE DATABASE ... TEMPLATE refuses a connected source)
	// and write nothing test-specific: every test gets the same copy.
	Build func(ctx context.Context, db *sql.DB) error
}

const (
	// Both prefixes start with "octopus_tpl" so listOwned's LIKE and ownedDBRe find every database
	// this file creates.
	templatePrefix = "octopus_tpl_"
	buildPrefix    = "octopus_tplbuild_"

	// Templates with another key are not dropped at once: two checkouts with different migrations
	// often share one Postgres, and dropping the other's template would fail its run.
	staleAfter = 24 * time.Hour

	// The build replays every migration; on a loaded laptop it has taken minutes.
	buildTimeout = 15 * time.Minute

	// Serializes template builds and pruning across the parallel test binaries of `go test ./...`.
	// Advisory locks are per database, so every process must take it on the admin DSN's database.
	templateLockKey int64 = 0x6f63746f70757331

	// api tests run in parallel with a pool each, next to other packages; at 25 per pool four tests
	// could take most of max_connections=100.
	templateMaxOpenConns = 10
)

var (
	templateNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,20}$`)
	templateKeyRe  = regexp.MustCompile(`^[0-9a-f]{16,}$`)
	// These names are interpolated into DDL; nothing that fails this regexp may reach
	// pruneTemplates' DROP.
	ownedDBRe = regexp.MustCompile(`^octopus_tpl[a-z0-9_]+$`)
)

// A failed build is cached too, so the suite fails fast instead of replaying every migration once
// per test.
var (
	templatesMu sync.Mutex
	templates   = map[string]error{}
)

func NewFromTemplate(t *testing.T, tpl Template) *sql.DB {
	t.Helper()
	if !templateNameRe.MatchString(tpl.Name) || !templateKeyRe.MatchString(tpl.Key) || tpl.Build == nil {
		//i18n:ignore test helper output
		t.Fatalf("testpg: invalid template %q (key %q)", tpl.Name, tpl.Key)
	}
	return provision(t, &tpl)
}

// 16 hex chars keep the name under Postgres's 63-byte identifier limit.
func (tpl *Template) dbName() string {
	return templatePrefix + tpl.Name + "_" + tpl.Key[:16]
}

func ensureTemplate(t *testing.T, base string, admin *sql.DB, tpl *Template) (string, error) {
	t.Helper()
	name := tpl.dbName()
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if err, done := templates[name]; done {
		return name, err
	}
	err := ensureTemplateOnServer(t, base, admin, tpl, name)
	templates[name] = err
	return name, err
}

func ensureTemplateOnServer(t *testing.T, base string, admin *sql.DB, tpl *Template, name string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	// A session-level advisory lock belongs to one connection, so take it on a dedicated one, not
	// the pool.
	conn, err := admin.Conn(ctx)
	if err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: admin conn: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			//i18n:ignore test helper output
			t.Logf("testpg: close template conn: %v", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, templateLockKey); err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: template lock: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, templateLockKey); err != nil {
			//i18n:ignore test helper output
			t.Logf("testpg: template unlock: %v", err)
		}
	}()

	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: look up template %s: %w", name, err)
	}
	if !exists {
		started := time.Now()
		if err := buildTemplate(ctx, t, base, conn, tpl, name); err != nil {
			return err
		}
		//i18n:ignore test helper output
		t.Logf("testpg: built template %s in %s", name, time.Since(started).Round(time.Millisecond))
	}

	stamp := fmt.Sprintf(`COMMENT ON DATABASE %s IS '%s'`, name, time.Now().UTC().Format(time.RFC3339))
	if _, err := conn.ExecContext(ctx, stamp); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg: stamp template %s: %v", name, err)
	}
	pruneTemplates(ctx, t, conn, name)
	return nil
}

// Build into a scratch database and rename only on success: a killed process leaves a scratch
// database pruneTemplates drops, never a half-built template.
func buildTemplate(ctx context.Context, t *testing.T, base string, conn *sql.Conn, tpl *Template, name string) error {
	t.Helper()
	scratch := buildPrefix + randHex(t)
	// Identifiers cannot be bound; scratch and name are built from validated [a-z0-9_] parts.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", scratch)); err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: create %s: %w", scratch, err)
	}
	if err := runBuild(ctx, withDB(t, base, scratch), tpl); err != nil {
		dropOwned(ctx, t, conn, scratch, false)
		return err
	}
	// CREATE DATABASE ... TEMPLATE and RENAME both refuse a database with connections, and closed
	// pool backends exit asynchronously, so terminate them.
	// ALLOW_CONNECTIONS false keeps a stray client from blocking every copy; IS_TEMPLATE lets a
	// CREATEDB-only role copy it.
	finish := func(target, stmt string, args ...any) error {
		if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
			dropOwned(ctx, t, conn, target, false)
			//i18n:ignore test helper output
			return fmt.Errorf("testpg: finish template %s: %q: %w", name, stmt, err)
		}
		return nil
	}
	if err := finish(scratch, fmt.Sprintf("ALTER DATABASE %s WITH ALLOW_CONNECTIONS false", scratch)); err != nil {
		return err
	}
	if err := finish(scratch, terminateSQL, scratch); err != nil {
		return err
	}
	if err := finish(scratch, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", scratch, name)); err != nil {
		return err
	}
	return finish(name, fmt.Sprintf("ALTER DATABASE %s WITH IS_TEMPLATE true", name))
}

const terminateSQL = `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`

func runBuild(ctx context.Context, dsn string, tpl *Template) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: open template build db: %w", err)
	}
	defer db.Close()
	if err := tpl.Build(ctx, db); err != nil {
		//i18n:ignore test helper output
		return fmt.Errorf("testpg: build template %s: %w", tpl.Name, err)
	}
	return nil
}

// Scratch databases only live while their builder holds the lock we now hold, so any left are
// orphans.
func pruneTemplates(ctx context.Context, t *testing.T, conn *sql.Conn, current string) {
	t.Helper()
	all, err := listOwned(ctx, conn, current)
	if err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg: list templates: %v", err)
		return
	}
	for _, f := range all {
		if !ownedDBRe.MatchString(f.name) {
			continue
		}
		if strings.HasPrefix(f.name, buildPrefix) {
			dropOwned(ctx, t, conn, f.name, false)
			continue
		}
		if used, err := time.Parse(time.RFC3339, f.stamp); err == nil && time.Since(used) < staleAfter {
			continue
		}
		dropOwned(ctx, t, conn, f.name, true)
	}
}

type ownedDB struct{ name, stamp string }

// Read every row before returning: the caller drops databases on the same connection, which cannot
// run a statement while rows are open.
func listOwned(ctx context.Context, conn *sql.Conn, current string) ([]ownedDB, error) {
	rows, err := conn.QueryContext(ctx, `SELECT datname, COALESCE(shobj_description(oid, 'pg_database'), '')
		FROM pg_database WHERE datname LIKE 'octopus\_tpl%' AND datname <> $1`, current)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []ownedDB
	for rows.Next() {
		var f ownedDB
		if err := rows.Scan(&f.name, &f.stamp); err != nil {
			return nil, err
		}
		all = append(all, f)
	}
	return all, rows.Err()
}

// Postgres refuses to drop a database still marked IS_TEMPLATE.
func dropOwned(ctx context.Context, t *testing.T, conn *sql.Conn, name string, isTemplate bool) {
	t.Helper()
	if isTemplate {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE %s WITH IS_TEMPLATE false", name)); err != nil {
			//i18n:ignore test helper output
			t.Logf("testpg: unmark template %s: %v", name, err)
			return
		}
	}
	if _, err := conn.ExecContext(ctx, terminateSQL, name); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg: terminate %s: %v", name, err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name)); err != nil {
		//i18n:ignore test helper output
		t.Logf("testpg: drop %s: %v", name, err)
	}
}
