package usage

import (
	"context"
	"database/sql"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

func (p Pricing) Cost(input, output, cacheRead, cacheCreation int) float64 {
	return (float64(input)*p.InPerMTok +
		float64(output)*p.OutPerMTok +
		float64(cacheRead)*p.CacheReadPerMTok +
		float64(cacheCreation)*p.CacheCreatePerMTok) / 1_000_000
}

func (s *Store) estimateCost(ctx context.Context, sess *Session) {
	if sess.CostUSD != 0 || sess.TotalTokens() == 0 {
		return
	}
	p, ok := s.PricingForModel(ctx, sess.Model)
	if !ok {
		return
	}
	cost := p.Cost(sess.InputTokens, sess.OutputTokens, sess.CacheRead, sess.CacheCreation)
	if cost <= 0 {
		return
	}
	sess.CostUSD = cost
	sess.CostEstimated = true
}

const unpricedCountSQL = `COUNT(*) FILTER (WHERE cost_usd = 0 AND NOT cost_estimated
		AND input_tokens + output_tokens + cache_read + cache_creation > 0)`

type unpriced struct {
	id                          string
	model, engine, agentType    string
	projectID, workspaceID      string
	finishedAt                  time.Time
	input, output, cRead, cMake int
}

func (s *Store) estimateUnpricedSessions(ctx context.Context) error {
	todo, err := s.unpricedSessions(ctx)
	if err != nil {
		return err
	}
	prices := map[string]*Pricing{}
	for i := range todo {
		u := &todo[i]
		p, seen := prices[u.model]
		if !seen {
			if found, ok := s.PricingForModel(ctx, u.model); ok {
				p = &found
			}
			prices[u.model] = p
		}
		if p == nil {
			continue
		}
		cost := p.Cost(u.input, u.output, u.cRead, u.cMake)
		if cost <= 0 {
			continue
		}
		if err := s.applyEstimate(ctx, *u, cost); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) unpricedSessions(ctx context.Context) ([]unpriced, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, model, engine,
		COALESCE(NULLIF(agent_type, ''), agent), project_id, workspace_id, finished_at,
		input_tokens, output_tokens, cache_read, cache_creation
		FROM sessions
		WHERE cost_usd = 0 AND NOT cost_estimated AND model != ''
		  AND input_tokens + output_tokens + cache_read + cache_creation > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todo []unpriced
	for rows.Next() {
		var u unpriced
		if err := rows.Scan(&u.id, &u.model, &u.engine, &u.agentType, &u.projectID,
			&u.workspaceID, &u.finishedAt, &u.input, &u.output, &u.cRead, &u.cMake); err != nil {
			return nil, err
		}
		todo = append(todo, u)
	}
	return todo, rows.Err()
}

// The conditional UPDATE stops two daemons starting at once from adding the same cost to the bucket
// twice.
func (s *Store) applyEstimate(ctx context.Context, u unpriced, cost float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rb(`UPDATE sessions SET cost_usd = ?, cost_estimated = true
		WHERE id = ? AND cost_usd = 0 AND NOT cost_estimated`), cost, u.id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if err := addRollupCost(ctx, tx, u, cost); err != nil {
		return err
	}
	return tx.Commit()
}

// Key must match upsertDailyRollup's. A missing bucket is left alone, not created with one session.
func addRollupCost(ctx context.Context, tx *sql.Tx, u unpriced, cost float64) error {
	_, err := tx.ExecContext(ctx, dbpkg.Rebind(`UPDATE usage_daily
		SET cost_usd = cost_usd + ?, estimated_cost_usd = estimated_cost_usd + ?
		WHERE day = ? AND engine = ? AND model = ? AND agent_type = ? AND project_id = ? AND workspace_id = ?`),
		cost, cost, u.finishedAt.UTC().Format("2006-01-02"), u.engine, u.model, u.agentType, u.projectID, u.workspaceID)
	return err
}
