package envmetrics

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type Signal string

const (
	SignalLatencyP50 Signal = "latency_p50"
	SignalLatencyP95 Signal = "latency_p95"
	SignalErrorRate  Signal = "error_rate"
	SignalThroughput Signal = "throughput"
	SignalCPU        Signal = "cpu"
	SignalMemory     Signal = "memory"
	SignalRestarts   Signal = "restarts"
	SignalCostDaily  Signal = "cost_daily"
	SignalSaturation Signal = "saturation"
)

var ValidSignals = []Signal{
	SignalLatencyP50, SignalLatencyP95, SignalErrorRate, SignalThroughput,
	SignalCPU, SignalMemory, SignalSaturation, SignalRestarts, SignalCostDaily,
}

func (s Signal) Valid() bool {
	for _, v := range ValidSignals {
		if v == s {
			return true
		}
	}
	return false
}

var counterSignals = map[Signal]bool{
	SignalThroughput: true,
	SignalErrorRate:  true,
	SignalSaturation: true,
	SignalRestarts:   true,
}

func (s Signal) IsCounter() bool { return counterSignals[s] }

type MappingStatus string

const (
	StatusProposed      MappingStatus = "proposed"
	StatusActive        MappingStatus = "active"
	StatusDisabled      MappingStatus = "disabled"
	StatusNotApplicable MappingStatus = "not_applicable"
)

type Mapping struct {
	ID            string         `json:"id"`
	EnvironmentID string         `json:"environment_id"`
	Service       string         `json:"service"`
	ProjectID     string         `json:"project_id,omitempty"`
	Signal        Signal         `json:"signal"`
	Source        map[string]any `json:"source"`

	Status        MappingStatus `json:"status"`
	NotApplicable bool          `json:"not_applicable,omitempty"`
	Note          string        `json:"note,omitempty"`
	TaskID        string        `json:"task_id,omitempty"`
	FlagID        string        `json:"flag_id,omitempty"`
	CreatedBy     string        `json:"created_by"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Point struct {
	EnvironmentID string    `json:"environment_id"`
	Service       string    `json:"service"`
	Signal        Signal    `json:"signal"`
	Bucket        time.Time `json:"bucket"`
	Value         float64   `json:"value"`
}

const BucketWidth = 5 * time.Minute

func TruncateBucket(t time.Time) time.Time {
	return t.UTC().Truncate(BucketWidth)
}

var ErrNotFound error = apperr.NotFound("envmetrics.errors.mapping_not_found")

type Store struct{ db *sql.DB }

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }
func (s *Store) DB() *sql.DB        { return s.db }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "envmetrics"

// A published migration never changes; internal/schema reads this for the migrations lock and lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	migrations := make([]migrate.Migration, 0, 16)
	migrations = append(migrations, []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS environment_metric_mappings (
			id             TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL,
			service        TEXT NOT NULL DEFAULT '',
			project_id     TEXT NOT NULL DEFAULT '',
			signal         TEXT NOT NULL,
			source         TEXT NOT NULL DEFAULT '{}',
			status         TEXT NOT NULL DEFAULT 'proposed',
			task_id        TEXT NOT NULL DEFAULT '',
			flag_id        TEXT NOT NULL DEFAULT '',
			created_by     TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL,
			updated_at     TIMESTAMP NOT NULL
		)`},
		{ID: 2, SQL: `CREATE INDEX IF NOT EXISTS idx_env_mappings_env
			ON environment_metric_mappings(environment_id)`},
		{ID: 3, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_env_mappings_active
			ON environment_metric_mappings(environment_id, service, signal)
			WHERE status = 'active'`},
		{ID: 4, SQL: `CREATE INDEX IF NOT EXISTS idx_env_mappings_status
			ON environment_metric_mappings(status)`},

		{ID: 5, SQL: `CREATE TABLE IF NOT EXISTS environment_metrics (
			environment_id TEXT NOT NULL,
			service        TEXT NOT NULL DEFAULT '',
			signal         TEXT NOT NULL,
			bucket         TIMESTAMP NOT NULL,
			value          DOUBLE PRECISION NOT NULL DEFAULT 0,
			PRIMARY KEY (environment_id, service, signal, bucket)
		)`},
		{ID: 6, SQL: `CREATE INDEX IF NOT EXISTS idx_env_metrics_bucket
			ON environment_metrics(bucket)`},

		{ID: 7, SQL: `ALTER TABLE environment_metric_mappings
			ADD COLUMN IF NOT EXISTS not_applicable BOOLEAN NOT NULL DEFAULT FALSE`},
		{ID: 8, SQL: `ALTER TABLE environment_metric_mappings
			ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT ''`},
	}...)
	// customMigrations and dashboardMigrations continue this component's id sequence; new ids go
	// after theirs.
	migrations = append(migrations, customMigrations()...)
	migrations = append(migrations, dashboardMigrations()...)
	return migrations, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.schema")
	}
	return &Store{db: db}, nil
}

const mappingCols = `id, environment_id, service, project_id, signal, source,
	status, not_applicable, note, task_id, flag_id, created_by, created_at, updated_at`

type MappingInput struct {
	Service       string         `json:"service"`
	ProjectID     string         `json:"project_id,omitempty"`
	Signal        Signal         `json:"signal"`
	Source        map[string]any `json:"source"`
	NotApplicable bool           `json:"not_applicable,omitempty"`
	Note          string         `json:"note,omitempty"`
}

func (in MappingInput) Validate() error {
	if strings.TrimSpace(in.Service) == "" {
		return apperr.Invalid("envmetrics.errors.service_required")
	}
	if !in.Signal.Valid() {
		return apperr.Invalid("envmetrics.errors.unknown_signal", "signal", in.Signal)
	}
	if in.NotApplicable {
		if strings.TrimSpace(in.Note) == "" {
			return apperr.Invalid("envmetrics.errors.not_applicable_needs_reason", "signal", in.Signal)
		}
		return nil
	}
	if len(in.Source) == 0 {
		return apperr.Invalid("envmetrics.errors.source_required", "signal", in.Signal)
	}
	return nil
}

func (s *Store) ProposeMappings(ctx context.Context, envID string, in []MappingInput, taskID, flagID, createdBy string) ([]Mapping, error) {
	for _, m := range in {
		if err := m.Validate(); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	out := make([]Mapping, 0, len(in))
	for _, m := range in {
		src, marshalErr := json.Marshal(m.Source)
		if marshalErr != nil {
			return nil, apperr.Wrap(marshalErr, "envmetrics.errors.marshal_source")
		}
		row := Mapping{
			ID: generateID(), EnvironmentID: envID, Service: m.Service,
			ProjectID: m.ProjectID, Signal: m.Signal, Source: m.Source,
			Status: StatusProposed, NotApplicable: m.NotApplicable, Note: m.Note,
			TaskID: taskID, FlagID: flagID,
			CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
		}
		if _, insErr := tx.ExecContext(ctx, s.rb(
			`INSERT INTO environment_metric_mappings (`+mappingCols+`)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			row.ID, row.EnvironmentID, row.Service, row.ProjectID, row.Signal,
			string(src), row.Status, row.NotApplicable, row.Note,
			row.TaskID, row.FlagID, row.CreatedBy,
			row.CreatedAt, row.UpdatedAt); insErr != nil {
			return nil, insErr
		}
		out = append(out, row)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListMappings(ctx context.Context, envID string) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+mappingCols+` FROM environment_metric_mappings
		  WHERE environment_id = ?
		  ORDER BY CASE status WHEN 'proposed' THEN 0 WHEN 'active' THEN 1 ELSE 2 END,
		           service, signal`), envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMappings(rows)
}

func (s *Store) ActiveMappings(ctx context.Context) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+mappingCols+` FROM environment_metric_mappings
		  WHERE status = ? ORDER BY environment_id, service, signal`), StatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMappings(rows)
}

// Retire must run before promote, or the promote violates uq_env_mappings_active.
func (s *Store) ApproveMappings(ctx context.Context, envID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if _, retireErr := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_metric_mappings SET status = ?, updated_at = ?
		  WHERE environment_id = ? AND status = ?
		    AND (service, signal) IN (
		      SELECT service, signal FROM environment_metric_mappings
		       WHERE environment_id = ? AND status = ?)`),
		StatusDisabled, now, envID, StatusActive, envID, StatusProposed); retireErr != nil {
		return 0, retireErr
	}
	// Not-applicable rows must never become active: the collector would get a mapping with no
	// source.
	if _, naErr := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_metric_mappings SET status = ?, updated_at = ?
		  WHERE environment_id = ? AND status = ? AND not_applicable = ?`),
		StatusNotApplicable, now, envID, StatusProposed, true); naErr != nil {
		return 0, naErr
	}
	res, err := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_metric_mappings SET status = ?, updated_at = ?
		  WHERE environment_id = ? AND status = ? AND not_applicable = ?`),
		StatusActive, now, envID, StatusProposed, false)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// Approving a flag must activate the same proposals the Environments panel does, or the flag goes
// green and nothing gets collected.
func (s *Store) EnvironmentForFlag(ctx context.Context, flagID string) (string, error) {
	if strings.TrimSpace(flagID) == "" {
		return "", nil
	}
	var envID string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT environment_id FROM environment_metric_mappings
		  WHERE flag_id = ? AND status = ? LIMIT 1`),
		flagID, StatusProposed).Scan(&envID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return envID, nil
}

func (s *Store) PendingFlagIDs(ctx context.Context, envID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT DISTINCT flag_id FROM environment_metric_mappings
		  WHERE environment_id = ? AND status = ? AND flag_id != ''`,
		envID, StatusProposed)
}

func (s *Store) DeleteMapping(ctx context.Context, envID, mapID string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_metric_mappings WHERE id = ? AND environment_id = ?`),
		mapID, envID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteForEnvironment(ctx context.Context, envID string) error {
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_metric_mappings WHERE environment_id = ?`), envID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_metrics WHERE environment_id = ?`), envID); err != nil {
		return err
	}
	if err := s.deleteCustomForEnvironment(ctx, envID); err != nil {
		return err
	}
	return deleteDashboardsForEnvironment(ctx, s, envID)
}

// Last value wins: summing a p95 is meaningless. This is where it differs from usage_daily, which
// accumulates.
func (s *Store) WritePoints(ctx context.Context, pts []Point) error {
	if len(pts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range pts {
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO environment_metrics (environment_id, service, signal, bucket, value)
			 VALUES (?,?,?,?,?)
			 ON CONFLICT (environment_id, service, signal, bucket)
			 DO UPDATE SET value = excluded.value`),
			p.EnvironmentID, p.Service, p.Signal,
			TruncateBucket(p.Bucket), p.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type SignalKey struct {
	Service string
	Signal  Signal
}

func (s *Store) CollectedSignals(ctx context.Context, envID string) (map[SignalKey]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT DISTINCT service, signal FROM environment_metrics WHERE environment_id = ?`), envID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[SignalKey]bool{}
	for rows.Next() {
		var k SignalKey
		if err := rows.Scan(&k.Service, &k.Signal); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func (s *Store) Series(ctx context.Context, envID string, signal Signal, service string, from, to time.Time) ([]Point, error) {
	conds := []string{"environment_id = ?", "bucket >= ?", "bucket <= ?"}
	args := []any{envID, from.UTC(), to.UTC()}
	if signal != "" {
		conds = append(conds, "signal = ?")
		args = append(args, string(signal))
	}
	if service != "" {
		conds = append(conds, "service = ?")
		args = append(args, service)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT environment_id, service, signal, bucket, value
		   FROM environment_metrics WHERE `+strings.Join(conds, " AND ")+`
		  ORDER BY signal, service, bucket`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.EnvironmentID, &p.Service, &p.Signal, &p.Bucket, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_metrics WHERE bucket < ?`), cutoff.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type mappingScanner interface{ Scan(dest ...any) error }

func scanMappings(rows *sql.Rows) ([]Mapping, error) {
	out := []Mapping{}
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanMapping(r mappingScanner) (*Mapping, error) {
	var m Mapping
	var src string
	if err := r.Scan(&m.ID, &m.EnvironmentID, &m.Service, &m.ProjectID, &m.Signal,
		&src, &m.Status, &m.NotApplicable, &m.Note, &m.TaskID, &m.FlagID, &m.CreatedBy,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	if src != "" && src != "{}" {
		if err := json.Unmarshal([]byte(src), &m.Source); err != nil {
			m.Source = nil
		}
	}
	return &m, nil
}
