package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "registry"

// A published migration never changes; internal/schema reads this for the migrations lock and lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS agent_registry (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL DEFAULT '',
			task_id    TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '',
			payload    TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_agent_registry_task ON agent_registry(task_id);
		CREATE INDEX IF NOT EXISTS idx_agent_registry_status ON agent_registry(status)`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "registry.errors.schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) Upsert(a *agent.Agent) error {
	payload, err := json.Marshal(a)
	if err != nil {
		return apperr.Wrap(err, "registry.errors.marshal_agent", "id", a.ID)
	}
	_, err = s.db.ExecContext(context.Background(), s.rb(
		`INSERT INTO agent_registry (id, name, task_id, status, payload, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name, task_id = excluded.task_id,
		   status = excluded.status, payload = excluded.payload,
		   updated_at = excluded.updated_at`),
		a.ID, a.Name, a.TaskID, string(a.Status), string(payload), time.Now().UTC(),
	)
	return err
}

func (s *Store) Remove(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		s.rb(`DELETE FROM agent_registry WHERE id = ?`), id)
	return err
}

func (r *Registry) AttachStore(s *Store) {
	r.mu.Lock()
	r.store = s
	r.mu.Unlock()
}

// Called with r.mu held so the mirror sees mutations in the same order as memory.
func (r *Registry) mirror(fn func(*Store) error, op, id string) {
	if r.store == nil {
		return
	}
	logx.Warn(fn(r.store), "registry.log.mirror_failed", "op", op, "id", id)
}
