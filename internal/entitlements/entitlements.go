package entitlements

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Plan string

// Cloud plans: the license is the same as self-hosted and the plan sells capacity, measured in agents
// at a time, with agent hours included per agent.
const (
	PlanTrial      Plan = "trial"
	PlanStarter    Plan = "starter"
	PlanTeam       Plan = "team"
	PlanEnterprise Plan = "enterprise"
	PlanSelfHosted Plan = "self-hosted"
	// A subscription that ended or stopped paying: the workspace stays readable and launches nothing.
	PlanInactive Plan = "inactive"

	// Older plan names still found in stored rows. Free reads as a trial and Business as Team with 15
	// agents.
	PlanFree     Plan = "free"
	PlanBusiness Plan = "business"
)

const GiB int64 = 1 << 30

// The numbers of the price list that the daemon enforces. Prices themselves are not here.
const (
	TrialAgents   = 2
	TrialHours    = 50
	TrialDays     = 30
	StarterAgents = 2
	HoursPerAgent = 100
	AgentsPerNode = 8
	ExtraHourUSD  = 0.05
	// The default cap on extra hours per agent the workspace pays for: $5 is 100 extra hours, about 20%
	// on top of an agent's $25, so nobody's bill grows more than that without an admin raising the cap.
	DefaultExtraCapPerAgentUSD = 5.0
	legacyBusinessAgents       = 15
	defaultTeamAgents          = 2
	defaultEnterpriseAgents    = 8
)

// A limit of 0 means unlimited.
type Entitlement struct {
	WorkspaceID      string `json:"workspace_id"`
	Plan             Plan   `json:"plan"`
	SeatLimit        int    `json:"seat_limit"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
	// Agent hours included each calendar month (UTC). 0 = no cap.
	HourQuotaMonthly int `json:"hour_quota_monthly"`
	// Dedicated nodes of an Enterprise workspace; 0 = shared cluster.
	Nodes             int   `json:"nodes,omitempty"`
	StorageQuotaBytes int64 `json:"storage_quota_bytes"`
	// What the workspace accepts to spend on extra hours each month, in USD. 0 = no extra hours: launches
	// stop at the included hours. Negative = the plan's default.
	ExtraHoursCapUSD float64   `json:"extra_hours_cap_usd"`
	TrialEndsAt      time.Time `json:"trial_ends_at,omitzero"`
	Features         []string  `json:"features,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// DefaultFor returns the limits of a plan with its default capacity.
func DefaultFor(plan Plan) Entitlement { return ForPlan(plan, 0, 0) }

// ForPlan returns the limits of a plan for the agents (Starter, Team, shared Enterprise) or dedicated
// nodes (Enterprise) the workspace pays for. 0 picks the plan's default.
func ForPlan(plan Plan, agents, nodes int) Entitlement {
	agents = max(agents, 0)
	nodes = max(nodes, 0)
	switch plan {
	case PlanTrial, PlanFree:
		return Entitlement{Plan: plan, SeatLimit: 4, ConcurrencyLimit: TrialAgents,
			HourQuotaMonthly: TrialHours, StorageQuotaBytes: 2 * GiB, ExtraHoursCapUSD: -1,
			Features: []string{"audit_export"}}
	case PlanStarter:
		a := max(agents, StarterAgents)
		return Entitlement{Plan: plan, SeatLimit: 4, ConcurrencyLimit: a, HourQuotaMonthly: HoursPerAgent * a,
			StorageQuotaBytes: 20*GiB + int64(a-StarterAgents)*10*GiB, ExtraHoursCapUSD: -1}
	case PlanTeam, PlanBusiness:
		a := agents
		if a == 0 {
			a = defaultTeamAgents
			if plan == PlanBusiness {
				a = legacyBusinessAgents
			}
		}
		return Entitlement{Plan: plan, ConcurrencyLimit: a, HourQuotaMonthly: HoursPerAgent * a,
			StorageQuotaBytes: int64(a) * 10 * GiB, ExtraHoursCapUSD: -1,
			Features: []string{"audit_export"}}
	case PlanEnterprise:
		if nodes > 0 {
			return Entitlement{Plan: plan, ConcurrencyLimit: AgentsPerNode * nodes, Nodes: nodes,
				StorageQuotaBytes: int64(nodes) * 100 * GiB, ExtraHoursCapUSD: -1, Features: []string{"audit_export"}}
		}
		// The shared cluster never comes without an hour cap: one tenant's unlimited is everyone
		// else's queue.
		a := agents
		if a == 0 {
			a = defaultEnterpriseAgents
		}
		return Entitlement{Plan: plan, ConcurrencyLimit: a, HourQuotaMonthly: HoursPerAgent * a,
			StorageQuotaBytes: int64(a) * 10 * GiB, ExtraHoursCapUSD: -1,
			Features: []string{"audit_export"}}
	case PlanInactive:
		return Entitlement{Plan: plan, SeatLimit: 4, ConcurrencyLimit: 1, StorageQuotaBytes: 2 * GiB,
			ExtraHoursCapUSD: -1}
	default:
		return Entitlement{Plan: PlanSelfHosted}
	}
}

// KnownPlan reports whether p is a plan the operator or the billing provider may assign.
func KnownPlan(p Plan) bool {
	switch p {
	case PlanTrial, PlanStarter, PlanTeam, PlanEnterprise, PlanSelfHosted, PlanInactive,
		PlanFree, PlanBusiness:
		return true
	}
	return false
}

func (p Plan) isTrial() bool { return p == PlanTrial || p == PlanFree }

// ExtraCap resolves the spending cap on extra hours: the trial never has extra hours, and the default
// grows with the agents (Starter with 2 agents, $10; Team with 10, $50).
func (e Entitlement) ExtraCap() float64 {
	switch {
	case e.Plan.isTrial() || e.Plan == PlanInactive:
		return 0
	case e.ExtraHoursCapUSD < 0:
		return DefaultExtraCapPerAgentUSD * float64(max(e.ConcurrencyLimit, 1))
	}
	return e.ExtraHoursCapUSD
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
		// Quotas count agent hours. run_quota_monthly and workspace_run_usage stay for databases that roll
		// back to a run quota; nothing reads them.
		// -1 in extra_hours_cap_cents means "the plan's default".
		{ID: 4, SQL: `ALTER TABLE workspace_entitlements
			ADD COLUMN IF NOT EXISTS hour_quota_monthly INTEGER NOT NULL DEFAULT -1,
			ADD COLUMN IF NOT EXISTS nodes INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS extra_hours_cap_cents INTEGER NOT NULL DEFAULT -1,
			ADD COLUMN IF NOT EXISTS trial_ends_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS billing_customer TEXT NOT NULL DEFAULT ''`},
		// Seconds of pod time per workspace and calendar month (UTC), already weighted by pod kind.
		// warned_pct remembers which usage notice (80, 100) already went out this month; reported_at
		// marks the month's extra hours as sent to the billing provider. included_hours and cap_cents
		// are the plan's terms the last time pod time was counted in that month (-1 = not seen), so a
		// workspace that cancels before the month closes is still billed by the plan it had.
		{ID: 5, SQL: `CREATE TABLE IF NOT EXISTS workspace_hour_usage (
			workspace_id   TEXT NOT NULL,
			period         TEXT NOT NULL,
			seconds        BIGINT NOT NULL DEFAULT 0,
			warned_pct     INTEGER NOT NULL DEFAULT 0,
			reported_at    TIMESTAMPTZ,
			included_hours INTEGER NOT NULL DEFAULT -1,
			cap_cents      INTEGER NOT NULL DEFAULT -1,
			PRIMARY KEY (workspace_id, period)
		)`},
		// How far each pod's time is already counted, so a restart or a second tick never counts the
		// same seconds twice.
		{ID: 6, SQL: `CREATE TABLE IF NOT EXISTS pod_meter (
			pod_key         TEXT PRIMARY KEY,
			workspace_id    TEXT NOT NULL,
			accounted_until TIMESTAMPTZ NOT NULL
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
		`SELECT workspace_id, plan, seat_limit, concurrency_limit, hour_quota_monthly, nodes,
		        storage_quota_bytes, extra_hours_cap_cents, trial_ends_at, features, updated_at
		 FROM workspace_entitlements WHERE workspace_id = ?`), workspaceID)
	var (
		capCents int
		trialEnd sql.NullTime
	)
	err := row.Scan(&e.WorkspaceID, &e.Plan, &e.SeatLimit, &e.ConcurrencyLimit,
		&e.HourQuotaMonthly, &e.Nodes, &e.StorageQuotaBytes, &capCents, &trialEnd, &featuresJSON, &e.UpdatedAt)
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
	def := ForPlan(e.Plan, e.ConcurrencyLimit, e.Nodes)
	if e.StorageQuotaBytes < 0 {
		e.StorageQuotaBytes = def.StorageQuotaBytes
	}
	// Rows from before agent hours: the plan's hours for the agents the row already allows.
	if e.HourQuotaMonthly < 0 {
		e.HourQuotaMonthly = def.HourQuotaMonthly
	}
	e.ExtraHoursCapUSD = -1
	if capCents >= 0 {
		e.ExtraHoursCapUSD = float64(capCents) / 100
	}
	if trialEnd.Valid {
		e.TrialEndsAt = trialEnd.Time.UTC()
	}
	if jsonErr := json.Unmarshal([]byte(featuresJSON), &e.Features); jsonErr != nil {
		e.Features = nil
	}
	return e, nil
}

func (s *Store) SetPlan(workspaceID string, plan Plan) error {
	return s.ApplyPlan(workspaceID, plan, 0, 0)
}

// ApplyPlan moves a workspace to a plan with the agents or nodes it pays for. The spending cap the
// workspace chose and the end of its trial survive the change.
func (s *Store) ApplyPlan(workspaceID string, plan Plan, agents, nodes int) error {
	current, err := s.Get(workspaceID)
	if err != nil {
		return err
	}
	e := ForPlan(plan, agents, nodes)
	e.WorkspaceID = workspaceID
	if current.ExtraHoursCapUSD >= 0 && !plan.isTrial() {
		e.ExtraHoursCapUSD = current.ExtraHoursCapUSD
	}
	e.TrialEndsAt = current.TrialEndsAt
	if err := s.Set(e); err != nil {
		return err
	}
	if e.HourQuotaMonthly > current.HourQuotaMonthly && current.HourQuotaMonthly > 0 {
		return s.rearmNotices(workspaceID)
	}
	return nil
}

// rearmNotices lets the 80% and 100% notices fire again this month after the workspace got more
// hours: the ones it saw were about the old quota.
func (s *Store) rearmNotices(workspaceID string) error {
	period, _ := s.period()
	_, err := s.db.ExecContext(context.Background(), s.rb(
		`UPDATE workspace_hour_usage SET warned_pct = 0 WHERE workspace_id = ? AND period = ?`),
		workspaceID, period)
	return err
}

// SetExtraHoursCap stores what the workspace accepts to spend on extra hours per month.
func (s *Store) SetExtraHoursCap(workspaceID string, usd float64) error {
	if usd < 0 {
		return apperr.Invalid("entitlements.errors.negative_cap")
	}
	e, err := s.Get(workspaceID)
	if err != nil {
		return err
	}
	e.ExtraHoursCapUSD = usd
	return s.Set(e)
}

// SetBillingCustomer remembers the billing provider's customer so extra hours can be reported.
func (s *Store) SetBillingCustomer(workspaceID, customer string) error {
	_, err := s.db.ExecContext(context.Background(), s.rb(
		`UPDATE workspace_entitlements SET billing_customer = ? WHERE workspace_id = ?`),
		strings.TrimSpace(customer), workspaceID)
	return err
}

func (s *Store) DeleteByWorkspace(workspaceID string) error {
	if _, err := s.db.ExecContext(context.Background(),
		s.rb(`DELETE FROM workspace_entitlements WHERE workspace_id = ?`), workspaceID); err != nil {
		return err
	}
	for _, table := range []string{"workspace_run_usage", "workspace_hour_usage", "pod_meter"} {
		if _, err := s.db.ExecContext(context.Background(),
			s.rb(`DELETE FROM `+table+` WHERE workspace_id = ?`), workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Set(e Entitlement) error {
	if e.WorkspaceID == "" {
		return apperr.Invalid("entitlements.errors.workspace_id_required")
	}
	featuresJSON, err := json.Marshal(e.Features)
	if err != nil {
		return apperr.Wrap(err, "entitlements.errors.marshal_features")
	}
	capCents := -1
	if e.ExtraHoursCapUSD >= 0 {
		capCents = int(math.Round(e.ExtraHoursCapUSD * 100))
	}
	var trialEnd any
	if !e.TrialEndsAt.IsZero() {
		trialEnd = e.TrialEndsAt.UTC()
	}
	_, err = s.db.ExecContext(context.Background(), s.rb(
		`INSERT INTO workspace_entitlements
		   (workspace_id, plan, seat_limit, concurrency_limit, hour_quota_monthly, nodes,
		    storage_quota_bytes, extra_hours_cap_cents, trial_ends_at, features, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET
		   plan = excluded.plan, seat_limit = excluded.seat_limit,
		   concurrency_limit = excluded.concurrency_limit,
		   hour_quota_monthly = excluded.hour_quota_monthly, nodes = excluded.nodes,
		   storage_quota_bytes = excluded.storage_quota_bytes,
		   extra_hours_cap_cents = excluded.extra_hours_cap_cents,
		   trial_ends_at = excluded.trial_ends_at,
		   features = excluded.features, updated_at = excluded.updated_at`),
		e.WorkspaceID, e.Plan, e.SeatLimit, e.ConcurrencyLimit, max(e.HourQuotaMonthly, 0), max(e.Nodes, 0),
		max(e.StorageQuotaBytes, 0), capCents, trialEnd, string(featuresJSON), time.Now().UTC(),
	)
	return err
}
