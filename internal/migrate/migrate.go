package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type Migration struct {
	ID  int
	SQL string
	// Set for destructive expand/contract migrations: the release running it must follow the one
	// whose code stopped needing what it removes.
	// The cmd/migrations lint refuses an unreleased destructive statement without it. See
	// docs/migrations.md.
	Contract *Contract
}

type Contract struct {
	StoppedUsingIn string
	Reason         string
}

type Inspector interface {
	ColumnExists(table, column string) (bool, error)
}

func DBInspector(ctx context.Context, db *sql.DB) Inspector {
	return dbInspector{ctx: ctx, db: db}
}

type dbInspector struct {
	ctx context.Context
	db  *sql.DB
}

func (i dbInspector) ColumnExists(table, column string) (bool, error) {
	return ColumnExists(i.ctx, i.db, table, column)
}

var Everything Inspector = everything{}

type everything struct{}

func (everything) ColumnExists(string, string) (bool, error) { return true, nil }

func Run(ctx context.Context, db *sql.DB, component string, migrations []Migration) error {
	if err := checkOrder(migrations); err != nil {
		return apperr.Wrap(err, "migrate.errors.component", "component", component)
	}
	conn, connErr := db.Conn(ctx)
	if connErr != nil {
		return apperr.Wrap(connErr, "migrate.errors.lock", "component", component)
	}
	defer func() { logx.Warn(conn.Close(), "migrate.log.release_conn") }()
	if lockErr := acquireLock(ctx, conn); lockErr != nil {
		return apperr.Wrap(lockErr, "migrate.errors.lock", "component", component)
	}
	defer releaseLock(conn)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component  TEXT      NOT NULL,
		id         INTEGER   NOT NULL,
		applied_at TIMESTAMP NOT NULL,
		PRIMARY KEY (component, id)
	)`); err != nil {
		return apperr.Wrap(err, "migrate.errors.create_history", "component", component)
	}

	applied, err := appliedIDs(ctx, conn, component)
	if err != nil {
		return apperr.Wrap(err, "migrate.errors.load_history", "component", component)
	}

	for _, m := range migrations {
		if applied[m.ID] {
			continue
		}
		if err := apply(ctx, conn, component, m); err != nil {
			return err
		}
	}
	return nil
}

const lockKey int64 = 0x6f63746f6d696772

// Polled with try-lock, not waited on in Postgres: the pool's lock_timeout (internal/db) ends lock
// waits after 10 s, and migrations can take longer.
var (
	lockPoll = 250 * time.Millisecond
	lockWait = 10 * time.Minute
)

func acquireLock(ctx context.Context, conn *sql.Conn) error {
	deadline := time.Now().Add(lockWait)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
			return err
		}
		if got {
			return nil
		}
		if time.Now().After(deadline) {
			return apperr.Unavailable("migrate.errors.lock_busy", "wait", lockWait.String())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// On unlock failure drop the connection: a pooled connection still holding the lock would block
// every other migrator.
func releaseLock(conn *sql.Conn) {
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey); err == nil {
		return
	}
	// Raw returns the callback's error; ErrBadConn is what tells database/sql to discard the
	// connection.
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		logx.Warn(err, "migrate.log.drop_conn")
	}
}

type queryer interface {
	execer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Merged branches can duplicate an ID; Run would then silently skip the second migration wherever
// the first is recorded.
func checkOrder(migrations []Migration) error {
	for i := 1; i < len(migrations); i++ {
		if prev, cur := migrations[i-1].ID, migrations[i].ID; cur <= prev {
			return apperr.Internal("migrate.errors.out_of_order", "id", cur, "prev", prev)
		}
	}
	return nil
}

func appliedIDs(ctx context.Context, db queryer, component string) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, dbpkg.Rebind(`SELECT id FROM schema_migrations WHERE component = ?`), component)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		applied[id] = true
	}
	return applied, rows.Err()
}

// The migration and its history row share one transaction so recorded and applied cannot drift.
// The baseline path records in a fresh transaction: the first is unusable after the error.
func apply(ctx context.Context, db queryer, component string, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "migrate.errors.begin", "component", component, "id", m.ID)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, m.SQL); execErr != nil {
		_ = tx.Rollback()
		if !isBaselineable(m, execErr) {
			return apperr.Wrap(execErr, "migrate.errors.apply", "component", component, "id", m.ID)
		}
		return record(ctx, db, component, m)
	}
	if err := record(ctx, tx, component, m); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "migrate.errors.commit", "component", component, "id", m.ID)
	}
	return nil
}

func record(ctx context.Context, ex execer, component string, m Migration) error {
	const q = `INSERT INTO schema_migrations (component, id, applied_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`
	if _, err := ex.ExecContext(ctx, dbpkg.Rebind(q), component, m.ID, time.Now().UTC()); err != nil {
		return apperr.Wrap(err, "migrate.errors.record", "component", component, "id", m.ID)
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Scoped to current_schema(): another schema's table of the same name would otherwise answer and
// run a backfill against missing columns.
func ColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	q := `SELECT column_name FROM information_schema.columns
	       WHERE table_schema = current_schema() AND table_name = ?`
	rows, err := db.QueryContext(ctx, dbpkg.Rebind(q), table)
	if err != nil {
		return false, apperr.Wrap(err, "migrate.errors.inspect_table", "table", table)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// Matches exactly one ADD COLUMN statement: Postgres runs a multi-statement migration in one
// implicit transaction,
// so a duplicate rolls back its siblings and baselining it would record statements that never ran.
var addColumnRe = regexp.MustCompile(`(?is)^\s*alter\s+table\s+\S+\s+add\s+column\s[^;]*;?\s*$`)

func isBaselineable(m Migration, err error) bool {
	if err == nil || !addColumnRe.MatchString(m.SQL) {
		return false
	}
	//i18n:ignore Postgres error text matched, not a message
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
