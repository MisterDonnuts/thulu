package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/listpage"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Session struct {
	ID            string    `json:"id"`
	Agent         string    `json:"agent"`
	AgentType     string    `json:"agent_type"`
	Engine        string    `json:"engine,omitempty"`
	Model         string    `json:"model"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	DurationMs    int64     `json:"duration_ms"`
	Turns         int       `json:"turns"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	CacheRead     int       `json:"cache_read_tokens"`
	CacheCreation int       `json:"cache_creation_tokens"`
	CostUSD       float64   `json:"cost_usd"`
	CostEstimated bool      `json:"cost_estimated,omitempty"`
	StopReason    string    `json:"stop_reason"`
	LogsFile      string    `json:"logs_file,omitempty"`
	OutputDir     string    `json:"output_dir,omitempty"`

	TaskID      string `json:"task_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Stage       string `json:"stage,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`

	WorkspaceID string `json:"workspace_id,omitempty"`
}

type RateLimit struct {
	Status        string  `json:"status"`
	ResetsAt      int64   `json:"resets_at"`
	RateLimitType string  `json:"rate_limit_type"`
	Utilization   float64 `json:"utilization"`
	IsOverage     bool    `json:"is_overage"`
}

func (s *Session) TotalTokens() int {
	return s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheCreation
}

type AgentSummary struct {
	AgentType        string  `json:"agent_type"`
	Sessions         int     `json:"sessions"`
	TotalTokens      int     `json:"total_tokens"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheRead        int     `json:"cache_read_tokens"`
	CacheCreation    int     `json:"cache_creation_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	UnpricedSessions int     `json:"unpriced_sessions"`
	TotalTurns       int     `json:"total_turns"`
	AvgCostUSD       float64 `json:"avg_cost_usd"`
}

type Store struct {
	db      *sql.DB
	dataDir string
	// Per workspace: on BYOK installs the snapshot describes one tenant's provider key.
	rateMu       sync.Mutex
	rateLimits   map[string]RateLimit
	rateLimitAny *RateLimit
	workspaceOf  WorkspaceResolver
}

type WorkspaceResolver func(ctx context.Context, taskID, projectID string) string

// Call once during wiring, before serving: workspaceOf is read without a lock.
func (s *Store) WithWorkspaceResolver(fn WorkspaceResolver) *Store {
	s.workspaceOf = fn
	return s
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DataDir() string { return s.dataDir }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "usage"

// A published migration never changes.
func Migrations(in migrate.Inspector) ([]migrate.Migration, error) {
	schema := []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS sessions (
			id            TEXT PRIMARY KEY,
			agent         TEXT NOT NULL,
			agent_type    TEXT NOT NULL DEFAULT '',
			model         TEXT NOT NULL DEFAULT '',
			started_at    TIMESTAMP NOT NULL,
			finished_at   TIMESTAMP NOT NULL,
			duration_ms   INTEGER NOT NULL DEFAULT 0,
			turns         INTEGER NOT NULL DEFAULT 0,
			input_tokens  INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read    INTEGER NOT NULL DEFAULT 0,
			cache_creation INTEGER NOT NULL DEFAULT 0,
			cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
			stop_reason   TEXT NOT NULL DEFAULT '',
			logs_file     TEXT NOT NULL DEFAULT '',
			output_dir    TEXT NOT NULL DEFAULT ''
		)`},
		{ID: 2, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS output_dir TEXT NOT NULL DEFAULT ''`},
		{ID: 3, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS engine TEXT NOT NULL DEFAULT ''`},

		{ID: 4, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task_id TEXT NOT NULL DEFAULT ''`},
		{ID: 5, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT ''`},
		{ID: 6, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT ''`},
		{ID: 7, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS service_tier TEXT NOT NULL DEFAULT ''`},
		{ID: 8, SQL: `CREATE INDEX IF NOT EXISTS sessions_finished_at_idx ON sessions(finished_at)`},
		{ID: 9, SQL: `CREATE INDEX IF NOT EXISTS sessions_engine_finished_idx ON sessions(engine, finished_at)`},
		{ID: 10, SQL: `CREATE INDEX IF NOT EXISTS sessions_model_finished_idx ON sessions(model, finished_at)`},
		{ID: 11, SQL: `CREATE INDEX IF NOT EXISTS sessions_task_idx ON sessions(task_id) WHERE task_id != ''`},
		{ID: 12, SQL: `CREATE INDEX IF NOT EXISTS sessions_project_finished_idx ON sessions(project_id, finished_at) WHERE project_id != ''`},
		{ID: 13, SQL: `CREATE INDEX IF NOT EXISTS sessions_agent_type_finished_idx ON sessions(agent_type, finished_at)`},

		{ID: 14, SQL: `CREATE TABLE IF NOT EXISTS session_tool_events (
			session_id   TEXT NOT NULL,
			ts           TIMESTAMP NOT NULL,
			turn_index   INTEGER NOT NULL DEFAULT 0,
			tool_name    TEXT NOT NULL,
			tool_kind    TEXT NOT NULL DEFAULT '',
			skill_name   TEXT NOT NULL DEFAULT '',
			input_bytes  INTEGER NOT NULL DEFAULT 0
		)`},
		{ID: 15, SQL: `CREATE INDEX IF NOT EXISTS tool_events_session_idx ON session_tool_events(session_id)`},
		{ID: 16, SQL: `CREATE INDEX IF NOT EXISTS tool_events_name_idx ON session_tool_events(tool_name)`},
		{ID: 17, SQL: `CREATE INDEX IF NOT EXISTS tool_events_skill_idx ON session_tool_events(skill_name) WHERE skill_name != ''`},
		{ID: 18, SQL: `CREATE INDEX IF NOT EXISTS tool_events_ts_idx ON session_tool_events(ts)`},

		{ID: 19, SQL: `CREATE TABLE IF NOT EXISTS usage_daily (
			day             TEXT NOT NULL,
			engine          TEXT NOT NULL DEFAULT '',
			model           TEXT NOT NULL DEFAULT '',
			agent_type      TEXT NOT NULL DEFAULT '',
			project_id      TEXT NOT NULL DEFAULT '',
			sessions        INTEGER NOT NULL DEFAULT 0,
			turns           INTEGER NOT NULL DEFAULT 0,
			input_tokens    INTEGER NOT NULL DEFAULT 0,
			output_tokens   INTEGER NOT NULL DEFAULT 0,
			cache_read      INTEGER NOT NULL DEFAULT 0,
			cache_creation  INTEGER NOT NULL DEFAULT 0,
			cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,
			duration_ms     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (day, engine, model, agent_type, project_id)
		)`},
		{ID: 20, SQL: `CREATE INDEX IF NOT EXISTS usage_daily_day_idx ON usage_daily(day)`},

		{ID: 21, SQL: `CREATE TABLE IF NOT EXISTS model_pricing (
			model                   TEXT PRIMARY KEY,
			in_per_mtok             DOUBLE PRECISION NOT NULL DEFAULT 0,
			out_per_mtok            DOUBLE PRECISION NOT NULL DEFAULT 0,
			cache_read_per_mtok     DOUBLE PRECISION NOT NULL DEFAULT 0,
			cache_creation_per_mtok DOUBLE PRECISION NOT NULL DEFAULT 0,
			updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`},

		{ID: 22, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`},
		{ID: 23, SQL: `CREATE INDEX IF NOT EXISTS sessions_workspace_finished_idx ON sessions(workspace_id, finished_at)`},
	}

	rollup := make([]migrate.Migration, 0, 16)
	rollup = append(rollup, []migrate.Migration{
		{ID: 26, SQL: `ALTER TABLE usage_daily ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`},
		{ID: 27, SQL: `ALTER TABLE usage_daily DROP CONSTRAINT IF EXISTS usage_daily_pkey`},
		{ID: 28, SQL: `ALTER TABLE usage_daily ADD PRIMARY KEY (day, engine, model, agent_type, project_id, workspace_id)`},
		{ID: 29, SQL: `DELETE FROM usage_daily;` + rebuildDailySQL},

		{ID: 30, SQL: `CREATE INDEX IF NOT EXISTS sessions_finished_id_idx ON sessions(finished_at, id)`},
		{ID: 31, SQL: `CREATE INDEX IF NOT EXISTS sessions_workspace_finished_id_idx ON sessions(workspace_id, finished_at, id)`},

		{ID: 32, SQL: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS cost_estimated BOOLEAN NOT NULL DEFAULT false`},

		{ID: 33, SQL: `ALTER TABLE usage_daily ADD COLUMN IF NOT EXISTS estimated_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0`},
	}...)
	rollup = append(rollup, attributionMigrations()...)

	// The workspace backfill needs workspace_id from the schema block and must run before migration
	// 29 rebuilds the rollup.
	backfill, err := workspaceBackfill(in)
	if err != nil {
		return nil, err
	}
	migrations := make([]migrate.Migration, 0, len(schema)+len(backfill)+len(rollup))
	migrations = append(migrations, schema...)
	migrations = append(migrations, backfill...)
	migrations = append(migrations, rollup...)
	return migrations, nil
}

func NewStore(db *sql.DB, dataDir string) (*Store, error) {
	ctx := context.Background()
	migrations, err := Migrations(migrate.DBInspector(ctx, db))
	if err != nil {
		return nil, err
	}
	if err := migrate.Run(ctx, db, MigrationComponent, migrations); err != nil {
		return nil, err
	}

	s := &Store{db: db, dataDir: dataDir}
	s.logUnattributed(ctx)

	if err := seedPricing(context.Background(), db); err != nil {
		logx.Warn(err, "usage.log.seed_model_pricing")
	}
	// Needs the seeded pricing table, so it runs after seedPricing.
	if err := s.estimateUnpricedSessions(ctx); err != nil {
		logx.Warn(err, "usage.log.estimate_unpriced")
	}
	if err := s.backfillDailyEstimated(ctx); err != nil {
		logx.Warn(err, "usage.log.backfill_daily_estimated")
	}

	s.migrateFromJSON(ctx)

	return s, nil
}

// Guarded: tasks/projects may not have migrated yet. A skipped migration stays unrecorded and runs
// on a later boot.
func workspaceBackfill(in migrate.Inspector) ([]migrate.Migration, error) {
	var out []migrate.Migration
	byTask, err := in.ColumnExists("tasks", "workspace_id")
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.migrations")
	}
	if byTask {
		out = append(out, migrate.Migration{ID: 24, SQL: `UPDATE sessions SET workspace_id = t.workspace_id
			FROM tasks t
			WHERE t.id = sessions.task_id
			  AND sessions.task_id != ''
			  AND sessions.workspace_id = ''
			  AND t.workspace_id != ''`})
	}
	byProject, err := in.ColumnExists("projects", "workspace_id")
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.migrations")
	}
	if byProject {
		out = append(out, migrate.Migration{ID: 25, SQL: `UPDATE sessions SET workspace_id = p.workspace_id
			FROM projects p
			WHERE p.id = sessions.project_id
			  AND sessions.project_id != ''
			  AND sessions.workspace_id = ''
			  AND p.workspace_id != ''`})
	}
	return out, nil
}

func (s *Store) logUnattributed(ctx context.Context) {
	var total, withTask, withProject int
	err := s.db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COUNT(*) FILTER (WHERE task_id != ''),
		COUNT(*) FILTER (WHERE project_id != '')
		FROM sessions WHERE workspace_id = ''`).Scan(&total, &withTask, &withProject)
	if err != nil {
		logx.Warn(err, "usage.log.count_unattributed")
		return
	}
	if total == 0 {
		return
	}
	log.Printf("[usage] %s", i18n.Log("usage.log.unattributed_sessions",
		"count", total, "tasks", withTask, "projects", withProject))
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) SetRateLimit(workspaceID string, rl RateLimit) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if workspaceID != "" {
		if s.rateLimits == nil {
			s.rateLimits = map[string]RateLimit{}
		}
		s.rateLimits[workspaceID] = rl
	}
	s.rateLimitAny = &rl
}

func (s *Store) GetRateLimit(sc Scope) *RateLimit {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if sc.Unrestricted {
		if s.rateLimitAny == nil {
			return nil
		}
		rl := *s.rateLimitAny
		return &rl
	}
	if rl, ok := s.rateLimits[sc.WorkspaceID]; ok && sc.WorkspaceID != "" {
		return &rl
	}
	return nil
}

func (s *Store) RunWorkspace(ctx context.Context, taskID, projectID string) string {
	if s.workspaceOf == nil {
		return ""
	}
	return s.workspaceOf(ctx, taskID, projectID)
}

// Order must match scanSession; count must match sessionInsertPlaceholders.
const sessionColumns = `id, agent, agent_type, engine, model, started_at, finished_at, duration_ms,
	turns, input_tokens, output_tokens, cache_read, cache_creation,
	cost_usd, stop_reason, logs_file, output_dir,
	task_id, project_id, stage, service_tier, workspace_id, cost_estimated`

const sessionInsertPlaceholders = `?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?`

// workspace_id keeps the existing value when the re-insert can't prove an owner; otherwise a second
// collection strips attribution and hides the run from its tenant.
const sessionUpsertClause = `ON CONFLICT(id) DO UPDATE SET
	agent = excluded.agent, agent_type = excluded.agent_type, engine = excluded.engine,
	model = excluded.model, started_at = excluded.started_at, finished_at = excluded.finished_at,
	duration_ms = excluded.duration_ms, turns = excluded.turns,
	input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
	cache_read = excluded.cache_read, cache_creation = excluded.cache_creation,
	cost_usd = excluded.cost_usd, stop_reason = excluded.stop_reason,
	logs_file = excluded.logs_file, output_dir = excluded.output_dir,
	task_id = excluded.task_id, project_id = excluded.project_id,
	stage = excluded.stage, service_tier = excluded.service_tier,
	cost_estimated = excluded.cost_estimated,
	workspace_id = COALESCE(NULLIF(excluded.workspace_id, ''), sessions.workspace_id)`

func (s *Store) Add(ctx context.Context, sess Session) error {
	sess.StartedAt = sess.StartedAt.UTC()
	sess.FinishedAt = sess.FinishedAt.UTC()
	// Every writer (collector, orphan adoption, JSON import) goes through Add; a row stored
	// unattributed is invisible to every tenant.
	if sess.WorkspaceID == "" && s.workspaceOf != nil {
		sess.WorkspaceID = s.workspaceOf(ctx, sess.TaskID, sess.ProjectID)
	}
	// Must run before upsertDailyRollup, which folds in the final cost.
	s.estimateCost(ctx, &sess)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, s.rb(`INSERT INTO sessions
		(`+sessionColumns+`) VALUES (`+sessionInsertPlaceholders+`)
		`+sessionUpsertClause),
		sess.ID, sess.Agent, sess.AgentType, sess.Engine, sess.Model,
		sess.StartedAt, sess.FinishedAt, sess.DurationMs,
		sess.Turns, sess.InputTokens, sess.OutputTokens,
		sess.CacheRead, sess.CacheCreation,
		sess.CostUSD, sess.StopReason, sess.LogsFile, sess.OutputDir,
		sess.TaskID, sess.ProjectID, sess.Stage, sess.ServiceTier, sess.WorkspaceID,
		sess.CostEstimated,
	); err != nil {
		return err
	}
	if err := upsertDailyRollup(ctx, tx, sess); err != nil {
		return err
	}
	return tx.Commit()
}

func scanSession(row interface{ Scan(...any) error }, sess *Session) error {
	return row.Scan(
		&sess.ID, &sess.Agent, &sess.AgentType, &sess.Engine, &sess.Model,
		&sess.StartedAt, &sess.FinishedAt, &sess.DurationMs,
		&sess.Turns, &sess.InputTokens, &sess.OutputTokens,
		&sess.CacheRead, &sess.CacheCreation,
		&sess.CostUSD, &sess.StopReason, &sess.LogsFile, &sess.OutputDir,
		&sess.TaskID, &sess.ProjectID, &sess.Stage, &sess.ServiceTier, &sess.WorkspaceID,
		&sess.CostEstimated,
	)
}

// No tenant boundary: handlers must go through sessionForCaller.
func (s *Store) Get(ctx context.Context, id string) *Session {
	row := s.db.QueryRowContext(ctx,
		s.rb(`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`), id)
	sess := &Session{}
	if err := scanSession(row, sess); err != nil {
		return nil
	}
	return sess
}

// id breaks finished_at ties; runs often finish in the same microsecond and a timestamp-only cursor
// would skip one.
const sessionOrder = ` ORDER BY finished_at DESC, id DESC`

func (s *Store) Sessions(ctx context.Context, sc Scope) []Session {
	out, _, err := s.SessionsPage(ctx, sc, listpage.Request{})
	if err != nil {
		return nil
	}
	return out
}

func (s *Store) SessionsPage(ctx context.Context, sc Scope, p listpage.Request) ([]Session, string, error) {
	where, args := sc.where()
	binds := append([]any(nil), args...)
	if p.Cursor != "" {
		key, err := listpage.Decode(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		if where == "" {
			where = `WHERE (finished_at, id) < (?, ?)`
		} else {
			where += ` AND (finished_at, id) < (?, ?)`
		}
		binds = append(binds, key.At, key.ID)
	}
	q := `SELECT ` + sessionColumns + ` FROM sessions ` + where + sessionOrder
	if p.Limit > 0 {
		q += ` LIMIT ?`
		binds = append(binds, p.Limit+1)
	}

	rows, err := s.db.QueryContext(ctx, s.rb(q), binds...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		if err := scanSession(rows, &sess); err != nil {
			continue
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		logx.Warn(err, "usage.log.iterate_sessions")
		return nil, "", err
	}
	if p.Limit <= 0 || len(out) <= p.Limit {
		return out, "", nil
	}
	out = out[:p.Limit]
	last := out[len(out)-1]
	return out, listpage.Encode(listpage.Key{At: last.FinishedAt, ID: last.ID}), nil
}

func (s *Store) SessionsForTask(ctx context.Context, sc Scope, taskID string) []Session {
	where, args := sc.where()
	if where == "" {
		where = "WHERE task_id = ?"
	} else {
		where += " AND task_id = ?"
	}
	args = append(args, taskID)
	rows, err := s.db.QueryContext(ctx,
		s.rb(`SELECT `+sessionColumns+` FROM sessions `+where+` ORDER BY started_at ASC`), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		if err := scanSession(rows, &sess); err != nil {
			continue
		}
		out = append(out, sess)
	}
	logx.Warn(rows.Err(), "usage.log.iterate_task_sessions")
	return out
}

func (s *Store) ByAgent(ctx context.Context, sc Scope) []AgentSummary {
	where, args := sc.where()
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT
		COALESCE(NULLIF(agent_type,''), agent) as atype,
		COUNT(*) as sessions,
		SUM(input_tokens + output_tokens + cache_read + cache_creation) as total_tokens,
		SUM(input_tokens) as input_tokens,
		SUM(output_tokens) as output_tokens,
		SUM(cache_read) as cache_read,
		SUM(cache_creation) as cache_creation,
		SUM(cost_usd) as total_cost,
		COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0) as estimated_cost,
		SUM(turns) as total_turns,
		`+unpricedCountSQL+`
		FROM sessions `+where+` GROUP BY atype ORDER BY total_cost DESC`), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []AgentSummary
	for rows.Next() {
		var a AgentSummary
		if err := rows.Scan(&a.AgentType, &a.Sessions, &a.TotalTokens,
			&a.InputTokens, &a.OutputTokens, &a.CacheRead, &a.CacheCreation,
			&a.TotalCostUSD, &a.EstimatedCostUSD, &a.TotalTurns, &a.UnpricedSessions); err != nil {
			continue
		}
		if a.Sessions > 0 {
			a.AvgCostUSD = a.TotalCostUSD / float64(a.Sessions)
		}
		out = append(out, a)
	}
	logx.Warn(rows.Err(), "usage.log.iterate_by_agent")
	return out
}

func (s *Store) Totals(ctx context.Context, sc Scope) AgentSummary {
	where, args := sc.where()
	return s.totalsWhere(ctx, "all", where, args...)
}

// No Scope: callers reach this via taskForCaller, which already enforced the workspace; filtering
// again drops the task's unattributed runs.
func (s *Store) TotalsByIDs(ctx context.Context, ids []string) AgentSummary {
	if len(ids) == 0 {
		var t AgentSummary
		t.AgentType = "task"
		return t
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.totalsWhere(ctx, "task", "WHERE id IN ("+placeholders+")", args...)
}

func (s *Store) totalsWhere(ctx context.Context, agentType, where string, args ...any) AgentSummary {
	var t AgentSummary
	t.AgentType = agentType
	row := s.db.QueryRowContext(ctx, s.rb(`SELECT
		COUNT(*),
		COALESCE(SUM(input_tokens + output_tokens + cache_read + cache_creation), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read), 0),
		COALESCE(SUM(cache_creation), 0),
		COALESCE(SUM(cost_usd), 0),
		COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0),
		`+unpricedCountSQL+`,
		COALESCE(SUM(turns), 0)
		FROM sessions `+where), args...)
	if err := row.Scan(&t.Sessions, &t.TotalTokens,
		&t.InputTokens, &t.OutputTokens, &t.CacheRead, &t.CacheCreation,
		&t.TotalCostUSD, &t.EstimatedCostUSD, &t.UnpricedSessions, &t.TotalTurns); err != nil {
		return t
	}
	if t.Sessions > 0 {
		t.AvgCostUSD = t.TotalCostUSD / float64(t.Sessions)
	}
	return t
}

func (s *Store) migrateFromJSON(ctx context.Context) {
	jsonPath := filepath.Join(s.dataDir, "usage.json")
	data, err := os.ReadFile(jsonPath) //nolint:gosec // G304: derived from operator-configured data dir
	if err != nil {
		return
	}

	var sessions []Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return
	}

	for i := range sessions {
		logx.Warn(s.Add(ctx, sessions[i]), "usage.log.migrate_session", "id", sessions[i].ID)
	}

	logx.Warn(os.Rename(jsonPath, jsonPath+".migrated"), "usage.log.rename_migrated_json", "path", jsonPath)
}
