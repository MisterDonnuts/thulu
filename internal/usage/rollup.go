package usage

import (
	"context"
	"database/sql"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func upsertDailyRollup(ctx context.Context, tx *sql.Tx, s Session) error {
	day := s.FinishedAt.UTC().Format("2006-01-02")
	agentType := s.AgentType
	if agentType == "" {
		agentType = s.Agent
	}
	estimated := 0.0
	if s.CostEstimated {
		estimated = s.CostUSD
	}
	_, err := tx.ExecContext(ctx, dbpkg.Rebind(`
		INSERT INTO usage_daily
			(day, engine, model, agent_type, project_id, workspace_id,
			 sessions, turns, input_tokens, output_tokens, cache_read, cache_creation,
			 cost_usd, estimated_cost_usd, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(day, engine, model, agent_type, project_id, workspace_id) DO UPDATE SET
			sessions           = usage_daily.sessions + 1,
			turns              = usage_daily.turns + excluded.turns,
			input_tokens       = usage_daily.input_tokens + excluded.input_tokens,
			output_tokens      = usage_daily.output_tokens + excluded.output_tokens,
			cache_read         = usage_daily.cache_read + excluded.cache_read,
			cache_creation     = usage_daily.cache_creation + excluded.cache_creation,
			cost_usd           = usage_daily.cost_usd + excluded.cost_usd,
			estimated_cost_usd = usage_daily.estimated_cost_usd + excluded.estimated_cost_usd,
			duration_ms        = usage_daily.duration_ms + excluded.duration_ms`),
		day, s.Engine, s.Model, agentType, s.ProjectID, s.WorkspaceID,
		s.Turns, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheCreation,
		s.CostUSD, estimated, s.DurationMs,
	)
	return err
}

// Part of migration 29: never edit. New rollup columns go in rebuildDailySinceSQL.
const rebuildDailySQL = `
	INSERT INTO usage_daily
		(day, engine, model, agent_type, project_id, workspace_id,
		 sessions, turns, input_tokens, output_tokens, cache_read, cache_creation,
		 cost_usd, duration_ms)
	SELECT
		to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
		engine,
		model,
		COALESCE(NULLIF(agent_type, ''), agent) AS agent_type,
		project_id,
		workspace_id,
		COUNT(*) AS sessions,
		SUM(turns), SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read), SUM(cache_creation),
		SUM(cost_usd), SUM(duration_ms)
	FROM sessions
	WHERE finished_at IS NOT NULL` + rebuildDailyGroup

const rebuildDailySinceSQL = `
	INSERT INTO usage_daily
		(day, engine, model, agent_type, project_id, workspace_id,
		 sessions, turns, input_tokens, output_tokens, cache_read, cache_creation,
		 cost_usd, estimated_cost_usd, duration_ms)
	SELECT
		to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
		engine,
		model,
		COALESCE(NULLIF(agent_type, ''), agent) AS agent_type,
		project_id,
		workspace_id,
		COUNT(*) AS sessions,
		SUM(turns), SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read), SUM(cache_creation),
		SUM(cost_usd), COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0),
		SUM(duration_ms)
	FROM sessions
	WHERE finished_at >= $1` + rebuildDailyGroup

const backfillDailyEstimatedSQL = `
	UPDATE usage_daily d SET estimated_cost_usd = e.estimated
	FROM (
		SELECT
			to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
			engine, model,
			COALESCE(NULLIF(agent_type, ''), agent) AS agent_type,
			project_id, workspace_id,
			SUM(cost_usd) AS estimated
		FROM sessions
		WHERE cost_estimated AND finished_at IS NOT NULL
		GROUP BY to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), engine, model,
			COALESCE(NULLIF(agent_type, ''), agent), project_id, workspace_id
	) e
	WHERE d.day = e.day AND d.engine = e.engine AND d.model = e.model
		AND d.agent_type = e.agent_type AND d.project_id = e.project_id
		AND d.workspace_id = e.workspace_id
		AND d.estimated_cost_usd IS DISTINCT FROM e.estimated`

// Runs on every start, not as a migration: a pre-33 binary after a rollback writes rollup rows
// without the estimated share.
func (s *Store) backfillDailyEstimated(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, backfillDailyEstimatedSQL)
	return err
}

const rebuildDailyGroup = `
	GROUP BY to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), engine, model,
		COALESCE(NULLIF(agent_type, ''), agent), project_id, workspace_id`

// Rebuild only from the oldest remaining session's day: older days exist only in the rollup since
// PruneSessions removed their sessions.
func (s *Store) RebuildDaily(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var oldest sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT MIN(finished_at) FROM sessions`).Scan(&oldest); err != nil {
		return err
	}
	if !oldest.Valid {
		return tx.Commit()
	}
	since := utcMidnight(oldest.Time)
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM usage_daily WHERE day >= ?`),
		since.Format("2006-01-02")); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebuildDailySinceSQL, since); err != nil {
		return err
	}
	return tx.Commit()
}

func utcMidnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Store) RollupTotals(ctx context.Context, sc Scope) AgentSummary {
	t := AgentSummary{AgentType: "all_time"}
	where, args := sc.where()
	row := s.db.QueryRowContext(ctx, s.rb(`SELECT
		COALESCE(SUM(sessions), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read), 0),
		COALESCE(SUM(cache_creation), 0),
		COALESCE(SUM(cost_usd), 0),
		COALESCE(SUM(estimated_cost_usd), 0),
		COALESCE(SUM(turns), 0)
		FROM usage_daily `+where), args...)
	if err := row.Scan(&t.Sessions, &t.InputTokens, &t.OutputTokens,
		&t.CacheRead, &t.CacheCreation, &t.TotalCostUSD, &t.EstimatedCostUSD, &t.TotalTurns); err != nil {
		logx.Warn(err, "usage.log.rollup_totals")
		return t
	}
	t.TotalTokens = t.InputTokens + t.OutputTokens + t.CacheRead + t.CacheCreation
	if t.Sessions > 0 {
		t.AvgCostUSD = t.TotalCostUSD / float64(t.Sessions)
	}
	return t
}
