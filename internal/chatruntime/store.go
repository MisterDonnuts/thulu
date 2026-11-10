package chatruntime

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
	StatusReady   Status = "ready"
	StatusEvicted Status = "evicted"
	StatusFailed  Status = "failed"
)

type Runtime struct {
	ChatID         string
	WorkspaceID    string
	AgentType      string
	ContainerID    string
	Status         Status
	CreatedAt      time.Time
	LastActivityAt time.Time
	ReleasedAt     *time.Time
	// Persisted, not in memory, so after a daemon restart we still know which containers hold a
	// delegated key.
	KeyDelegatedUntil *time.Time
}

var ErrNotFound error = apperr.NotFound("chatruntime.errors.not_found")

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: schema_migrations records history under this name, and a rename reruns every
// migration.
const MigrationComponent = "chatruntime"

// internal/schema reads this for the migrations lock and lint. Never edit a published migration;
// append a new one.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS chat_runtimes (
				chat_id          TEXT PRIMARY KEY,
				workspace_id     TEXT NOT NULL DEFAULT '',
				agent_type       TEXT NOT NULL,
				container_id     TEXT NOT NULL,
				status           TEXT NOT NULL,
				created_at       TIMESTAMP NOT NULL,
				last_activity_at TIMESTAMP NOT NULL,
				released_at      TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_chatrt_status   ON chat_runtimes(status);
			CREATE INDEX IF NOT EXISTS idx_chatrt_activity ON chat_runtimes(last_activity_at);
			CREATE INDEX IF NOT EXISTS idx_chatrt_ws       ON chat_runtimes(workspace_id, status);`},
		{ID: 2, SQL: `ALTER TABLE chat_runtimes ADD COLUMN IF NOT EXISTS key_delegated_until TIMESTAMPTZ`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "chatruntime.errors.init_schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

const rtCols = `chat_id, workspace_id, agent_type, container_id, status, created_at, last_activity_at, released_at, key_delegated_until`

func (s *Store) Upsert(rt *Runtime) error {
	var released, delegated any
	if rt.ReleasedAt != nil {
		released = rt.ReleasedAt.UTC()
	}
	if rt.KeyDelegatedUntil != nil {
		delegated = rt.KeyDelegatedUntil.UTC()
	}
	_, err := s.db.ExecContext(context.Background(), s.rb(`
		INSERT INTO chat_runtimes(`+rtCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			workspace_id        = excluded.workspace_id,
			agent_type          = excluded.agent_type,
			container_id        = excluded.container_id,
			status              = excluded.status,
			created_at          = excluded.created_at,
			last_activity_at    = excluded.last_activity_at,
			released_at         = excluded.released_at,
			key_delegated_until = excluded.key_delegated_until`),
		rt.ChatID, rt.WorkspaceID, rt.AgentType, rt.ContainerID, string(rt.Status),
		rt.CreatedAt.UTC(), rt.LastActivityAt.UTC(), released, delegated,
	)
	return err
}

func scanRuntime(scan func(...any) error) (*Runtime, error) {
	var rt Runtime
	var status string
	var released, delegated sql.NullTime
	err := scan(&rt.ChatID, &rt.WorkspaceID, &rt.AgentType, &rt.ContainerID, &status,
		&rt.CreatedAt, &rt.LastActivityAt, &released, &delegated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rt.Status = Status(status)
	if released.Valid {
		t := released.Time
		rt.ReleasedAt = &t
	}
	if delegated.Valid {
		t := delegated.Time
		rt.KeyDelegatedUntil = &t
	}
	return &rt, nil
}

func (s *Store) Get(chatID string) (*Runtime, error) {
	row := s.db.QueryRowContext(context.Background(), s.rb(
		`SELECT `+rtCols+` FROM chat_runtimes WHERE chat_id = ?`), chatID)
	return scanRuntime(row.Scan)
}

func (s *Store) Touch(chatID string) error {
	res, err := s.db.ExecContext(context.Background(), s.rb(
		`UPDATE chat_runtimes SET last_activity_at = ? WHERE chat_id = ?`),
		time.Now().UTC(), chatID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkReleased(chatID string) error {
	res, err := s.db.ExecContext(context.Background(), s.rb(
		`UPDATE chat_runtimes SET status = ?, released_at = ? WHERE chat_id = ?`),
		string(StatusEvicted), time.Now().UTC(), chatID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(chatID string) error {
	_, err := s.db.ExecContext(context.Background(), s.rb(
		`DELETE FROM chat_runtimes WHERE chat_id = ?`), chatID)
	return err
}

func (s *Store) ListLive() ([]*Runtime, error) {
	rows, err := s.db.QueryContext(context.Background(), s.rb(`
		SELECT `+rtCols+` FROM chat_runtimes
		 WHERE status NOT IN (?, ?)
		 ORDER BY created_at ASC`),
		string(StatusEvicted), string(StatusFailed),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runtime
	for rows.Next() {
		rt, err := scanRuntime(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

func (s *Store) LiveForWorkspace(workspaceID string) ([]*Runtime, error) {
	rows, err := s.db.QueryContext(context.Background(), s.rb(`
		SELECT `+rtCols+` FROM chat_runtimes
		 WHERE workspace_id = ? AND status NOT IN (?, ?)
		 ORDER BY last_activity_at ASC`),
		workspaceID, string(StatusEvicted), string(StatusFailed),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runtime
	for rows.Next() {
		rt, err := scanRuntime(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

func (s *Store) CountLive() (int, error) {
	var n int
	err := s.db.QueryRowContext(context.Background(), s.rb(
		`SELECT COUNT(*) FROM chat_runtimes WHERE status NOT IN (?, ?)`),
		string(StatusEvicted), string(StatusFailed),
	).Scan(&n)
	return n, err
}

func (s *Store) CountLiveForWorkspace(workspaceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(context.Background(), s.rb(
		`SELECT COUNT(*) FROM chat_runtimes WHERE workspace_id = ? AND status NOT IN (?, ?)`),
		workspaceID, string(StatusEvicted), string(StatusFailed),
	).Scan(&n)
	return n, err
}

func (s *Store) IdleEvictionCandidate(cutoff time.Time, workspaceID string) (*Runtime, error) {
	return s.oldestLiveWhere(workspaceID, " AND last_activity_at < ?", cutoff.UTC())
}

func (s *Store) oldestLiveWhere(workspaceID, extra string, extraArgs ...any) (*Runtime, error) {
	where := `status NOT IN (?, ?)`
	args := []any{string(StatusEvicted), string(StatusFailed)}
	if workspaceID != "" {
		where = `workspace_id = ? AND ` + where
		args = append([]any{workspaceID}, args...)
	}
	where += extra
	args = append(args, extraArgs...)
	row := s.db.QueryRowContext(context.Background(), s.rb(`
		SELECT `+rtCols+` FROM chat_runtimes
		 WHERE `+where+`
		 ORDER BY last_activity_at ASC
		 LIMIT 1`),
		args...,
	)
	rt, err := scanRuntime(row.Scan)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return rt, err
}
