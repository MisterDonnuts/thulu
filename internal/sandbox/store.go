package sandbox

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusEvicted  Status = "evicted"
	StatusFailed   Status = "failed"
)

type Sandbox struct {
	TaskID         string
	WorkspaceID    string
	ContainerID    string
	Image          string
	Status         Status
	HostPorts      map[int]int
	CreatedAt      time.Time
	LastActivityAt time.Time
	ReleasedAt     *time.Time
}

type QueueEntry struct {
	TaskID     string
	Stage      string
	Priority   int
	EnqueuedAt time.Time
}

var ErrNotFound error = apperr.NotFound("sandbox.errors.not_found")

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: schema_migrations records history under this name, and a rename reruns every
// migration.
const MigrationComponent = "sandbox"

func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS task_sandboxes (
				task_id          TEXT PRIMARY KEY,
				container_id     TEXT NOT NULL,
				image            TEXT NOT NULL,
				status           TEXT NOT NULL,
				host_ports_json  TEXT NOT NULL DEFAULT '{}',
				created_at       TIMESTAMP NOT NULL,
				last_activity_at TIMESTAMP NOT NULL,
				released_at      TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_sandboxes_status ON task_sandboxes(status);
			CREATE INDEX IF NOT EXISTS idx_sandboxes_activity ON task_sandboxes(last_activity_at);

			CREATE TABLE IF NOT EXISTS sandbox_queue (
				task_id     TEXT PRIMARY KEY,
				stage       TEXT NOT NULL,
				priority    INTEGER NOT NULL,
				enqueued_at TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_queue_priority ON sandbox_queue(priority DESC, enqueued_at ASC);`},
		{ID: 2, SQL: `ALTER TABLE task_sandboxes ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_sandboxes_ws ON task_sandboxes(workspace_id, status);`},
		setupRunsMigration,
		cacheUsageMigration,
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		db.Close()
		return nil, apperr.Wrap(err, "sandbox.errors.create_schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Upsert(ctx context.Context, sb *Sandbox) error {
	hp, err := encodePorts(sb.HostPorts)
	if err != nil {
		return err
	}
	// workspace_id is deliberately not in the update set: later Upserts don't carry it and would
	// wipe the tenant tag.
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO task_sandboxes(
			task_id, workspace_id, container_id, image, status, host_ports_json,
			created_at, last_activity_at, released_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			container_id     = excluded.container_id,
			image            = excluded.image,
			status           = excluded.status,
			host_ports_json  = excluded.host_ports_json,
			created_at       = excluded.created_at,
			last_activity_at = excluded.last_activity_at,
			released_at      = excluded.released_at`),
		sb.TaskID, sb.WorkspaceID, sb.ContainerID, sb.Image, string(sb.Status), hp,
		sb.CreatedAt.UTC(), sb.LastActivityAt.UTC(), nullableTime(sb.ReleasedAt),
	)
	return err
}

func (s *Store) CountLiveForWorkspace(ctx context.Context, workspaceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM task_sandboxes WHERE workspace_id = ? AND status NOT IN (?, ?)`),
		workspaceID, string(StatusEvicted), string(StatusFailed),
	).Scan(&n)
	return n, err
}

func (s *Store) Get(ctx context.Context, taskID string) (*Sandbox, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`
		SELECT task_id, workspace_id, container_id, image, status, host_ports_json,
		       created_at, last_activity_at, released_at
		  FROM task_sandboxes WHERE task_id = ?`), taskID)
	return scanSandbox(row.Scan)
}

func (s *Store) Touch(ctx context.Context, taskID string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_sandboxes SET last_activity_at = ? WHERE task_id = ?`),
		time.Now().UTC(), taskID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetStatus(ctx context.Context, taskID string, st Status) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_sandboxes SET status = ? WHERE task_id = ?`),
		string(st), taskID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkReleased(ctx context.Context, taskID string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_sandboxes SET status = ?, released_at = ? WHERE task_id = ?`),
		string(StatusEvicted), time.Now().UTC(), taskID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM sandbox_queue WHERE task_id = ?`,
		`DELETE FROM task_sandboxes WHERE task_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, s.rb(q), taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) OwnersInWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT task_id FROM task_sandboxes WHERE workspace_id = ?`, workspaceID)
}

func (s *Store) ListLive(ctx context.Context) ([]*Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT task_id, workspace_id, container_id, image, status, host_ports_json,
		       created_at, last_activity_at, released_at
		  FROM task_sandboxes
		 WHERE status NOT IN (?, ?)
		 ORDER BY created_at ASC`),
		string(StatusEvicted), string(StatusFailed),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSandboxes(rows)
}

func (s *Store) ListAll(ctx context.Context) ([]*Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT task_id, workspace_id, container_id, image, status, host_ports_json,
		       created_at, last_activity_at, released_at
		  FROM task_sandboxes
		 ORDER BY created_at DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSandboxes(rows)
}

func (s *Store) CountLive(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM task_sandboxes WHERE status NOT IN (?, ?)`),
		string(StatusEvicted), string(StatusFailed),
	).Scan(&n)
	return n, err
}

// Evidence apps are never candidates: the runner tests them over the preview URL, so no exec marks
// them active.
func (s *Store) IdleEvictionCandidate(ctx context.Context, cutoff time.Time) (*Sandbox, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`
		SELECT task_id, workspace_id, container_id, image, status, host_ports_json,
		       created_at, last_activity_at, released_at
		  FROM task_sandboxes
		 WHERE status NOT IN (?, ?) AND last_activity_at < ? AND task_id NOT LIKE ?
		 ORDER BY last_activity_at ASC
		 LIMIT 1`),
		string(StatusEvicted), string(StatusFailed), cutoff.UTC(), evidenceOwnerPrefix+"%",
	)
	sb, err := scanSandbox(row.Scan)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return sb, err
}

// enqueued_at is deliberately not updated on conflict, so FIFO within a priority survives an agent
// re-pinging.
func (s *Store) Enqueue(ctx context.Context, taskID, stage string, priority int) error {
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO sandbox_queue(task_id, stage, priority, enqueued_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			stage    = excluded.stage,
			priority = excluded.priority`),
		taskID, stage, priority, time.Now().UTC(),
	)
	return err
}

func (s *Store) Reprioritize(ctx context.Context, prios map[string]int) (int, error) {
	queued, err := s.ListQueue(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, e := range queued {
		want := prios[e.Stage]
		if want == e.Priority {
			continue
		}
		if _, err := s.db.ExecContext(ctx, s.rb(
			`UPDATE sandbox_queue SET priority = ? WHERE task_id = ? AND stage = ?`),
			want, e.TaskID, e.Stage); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func (s *Store) Dequeue(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM sandbox_queue WHERE task_id = ?`), taskID)
	return err
}

func (s *Store) Next(ctx context.Context) (*QueueEntry, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`
		SELECT task_id, stage, priority, enqueued_at
		  FROM sandbox_queue
		 ORDER BY priority DESC, enqueued_at ASC
		 LIMIT 1`))
	var e QueueEntry
	err := row.Scan(&e.TaskID, &e.Stage, &e.Priority, &e.EnqueuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) Position(ctx context.Context, taskID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT task_id FROM sandbox_queue
		 ORDER BY priority DESC, enqueued_at ASC`))
	if err != nil {
		return -1, err
	}
	defer rows.Close()
	pos := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return -1, err
		}
		if id == taskID {
			return pos, nil
		}
		pos++
	}
	return -1, rows.Err()
}

func (s *Store) ListQueue(ctx context.Context) ([]*QueueEntry, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT task_id, stage, priority, enqueued_at
		  FROM sandbox_queue
		 ORDER BY priority DESC, enqueued_at ASC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueueEntry
	for rows.Next() {
		var e QueueEntry
		if err := rows.Scan(&e.TaskID, &e.Stage, &e.Priority, &e.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
