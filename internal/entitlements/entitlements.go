package entitlements

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Plan string

const (
	PlanFree       Plan = "free"
	PlanTeam       Plan = "team"
	PlanBusiness   Plan = "business"
	PlanEnterprise Plan = "enterprise"
	PlanSelfHosted Plan = "self-hosted"
)

const GiB int64 = 1 << 30

// A limit of 0 means unlimited.
type Entitlement struct {
	WorkspaceID       string    `json:"workspace_id"`
	Plan              Plan      `json:"plan"`
	SeatLimit         int       `json:"seat_limit"`
	ConcurrencyLimit  int       `json:"concurrency_limit"`
	RunQuotaMonthly   int       `json:"run_quota_monthly"`
	StorageQuotaBytes int64     `json:"storage_quota_bytes"`
	Features          []string  `json:"features,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitzero"`
}

// DefaultFor returns the limits of a plan.
func DefaultFor(plan Plan) Entitlement {
	switch plan {
	case PlanFree:
		return Entitlement{Plan: PlanFree, SeatLimit: 4, ConcurrencyLimit: 1, RunQuotaMonthly: 100,
			StorageQuotaBytes: 2 * GiB}
	case PlanTeam:
		return Entitlement{Plan: PlanTeam, SeatLimit: 20, ConcurrencyLimit: 5, RunQuotaMonthly: 1000,
			StorageQuotaBytes: 20 * GiB, Features: []string{"audit_export"}}
	case PlanBusiness:
		return Entitlement{Plan: PlanBusiness, SeatLimit: 0, ConcurrencyLimit: 15, RunQuotaMonthly: 5000,
			StorageQuotaBytes: 100 * GiB,
			Features:          []string{"audit_export"}}
	case PlanEnterprise:
		return Entitlement{Plan: PlanEnterprise, SeatLimit: 0, ConcurrencyLimit: 0, RunQuotaMonthly: 0,
			Features: []string{"audit_export"}}
	default:
		return Entitlement{Plan: PlanSelfHosted}
	}
}

type Store struct {
	db          *sql.DB
	defaultPlan Plan
	exempt      string
	now         func() time.Time
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: databases that ran these migrations under this name would run them all again.
const MigrationComponent = "entitlements"

// Append only; a published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS workspace_entitlements (
			workspace_id      TEXT PRIMARY KEY,
			plan              TEXT NOT NULL,
			seat_limit        INTEGER NOT NULL DEFAULT 0,
			concurrency_limit INTEGER NOT NULL DEFAULT 0,
			run_quota_monthly INTEGER NOT NULL DEFAULT 0,
			features          TEXT NOT NULL DEFAULT '[]',
			updated_at        TIMESTAMP NOT NULL
		)`},
		// -1 means "the plan's default", so rows written before this column pick up their plan's
		// quota.
		{ID: 2, SQL: `ALTER TABLE workspace_entitlements
			ADD COLUMN IF NOT EXISTS storage_quota_bytes BIGINT NOT NULL DEFAULT -1`},
		// Counted at launch so deleting tasks or pruning usage never hands runs back.
		{ID: 3, SQL: `CREATE TABLE IF NOT EXISTS workspace_run_usage (
			workspace_id TEXT NOT NULL,
			period       TEXT NOT NULL,
			runs         INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (workspace_id, period)
		)`},
	}, nil
}

func NewStore(db *sql.DB, defaultPlan Plan) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "entitlements.errors.schema")
	}
	if defaultPlan == "" {
		defaultPlan = PlanSelfHosted
	}
	return &Store{db: db, defaultPlan: defaultPlan, now: time.Now}, nil
}

func (s *Store) ExemptWorkspace(workspaceID string) *Store {
	s.exempt = strings.TrimSpace(workspaceID)
	return s
}

func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) Get(workspaceID string) (Entitlement, error) {
	var (
		e            Entitlement
		featuresJSON string
	)
	row := s.db.QueryRowContext(context.Background(), s.rb(
		`SELECT workspace_id, plan, seat_limit, concurrency_limit, run_quota_monthly,
		        storage_quota_bytes, features, updated_at
		 FROM workspace_entitlements WHERE workspace_id = ?`), workspaceID)
	err := row.Scan(&e.WorkspaceID, &e.Plan, &e.SeatLimit, &e.ConcurrencyLimit,
		&e.RunQuotaMonthly, &e.StorageQuotaBytes, &featuresJSON, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		plan := s.defaultPlan
		if s.exempt != "" && workspaceID == s.exempt {
			plan = PlanSelfHosted
		}
		def := DefaultFor(plan)
		def.WorkspaceID = workspaceID
		return def, nil
	}
	if err != nil {
		return Entitlement{}, err
	}
	if e.StorageQuotaBytes < 0 {
		e.StorageQuotaBytes = DefaultFor(e.Plan).StorageQuotaBytes
	}
	if jsonErr := json.Unmarshal([]byte(featuresJSON), &e.Features); jsonErr != nil {
		e.Features = nil
	}
	return e, nil
}

func (s *Store) SetPlan(workspaceID string, plan Plan) error {
	e := DefaultFor(plan)
	e.WorkspaceID = workspaceID
	return s.Set(e)
}

func (s *Store) DeleteByWorkspace(workspaceID string) error {
	if _, err := s.db.ExecContext(context.Background(),
		s.rb(`DELETE FROM workspace_entitlements WHERE workspace_id = ?`), workspaceID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(context.Background(),
		s.rb(`DELETE FROM workspace_run_usage WHERE workspace_id = ?`), workspaceID)
	return err
}

func (s *Store) Set(e Entitlement) error {
	if e.WorkspaceID == "" {
		return apperr.Invalid("entitlements.errors.workspace_id_required")
	}
	featuresJSON, err := json.Marshal(e.Features)
	if err != nil {
		return apperr.Wrap(err, "entitlements.errors.marshal_features")
	}
	_, err = s.db.ExecContext(context.Background(), s.rb(
		`INSERT INTO workspace_entitlements
		   (workspace_id, plan, seat_limit, concurrency_limit, run_quota_monthly,
		    storage_quota_bytes, features, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET
		   plan = excluded.plan, seat_limit = excluded.seat_limit,
		   concurrency_limit = excluded.concurrency_limit,
		   run_quota_monthly = excluded.run_quota_monthly,
		   storage_quota_bytes = excluded.storage_quota_bytes,
		   features = excluded.features, updated_at = excluded.updated_at`),
		e.WorkspaceID, e.Plan, e.SeatLimit, e.ConcurrencyLimit, e.RunQuotaMonthly,
		max(e.StorageQuotaBytes, 0), string(featuresJSON), time.Now().UTC(),
	)
	return err
}
