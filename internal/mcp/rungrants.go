package mcp

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The proxy enforces the MCP_TOOLS a run launched with, not a recomputation: chat agents are
// augmented at launch
// and mid-run agent edits must not change a running grant (which also matches its .mcp.json).

type RunGrant struct {
	Tools    string
	Locale   string
	OnRunner bool
}

// Run and chat keys live a day at most; a week covers them with room.
const runGrantRetention = 7 * 24 * time.Hour

func (r *Registry) RecordRunGrant(ctx context.Context, ref, agent, stage string, g RunGrant) error {
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, r.rb(
		`INSERT INTO mcp_run_grants(run_ref, agent, stage, tools, locale, on_runner, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_ref, agent, stage)
		 DO UPDATE SET tools = excluded.tools, locale = excluded.locale, on_runner = excluded.on_runner,
		   updated_at = excluded.updated_at`),
		ref, agent, stage, g.Tools, g.Locale, g.OnRunner, now); err != nil {
		return apperr.Wrap(err, "mcp.errors.record_run_grant", "run", ref)
	}
	_, err := r.db.ExecContext(ctx, r.rb(`DELETE FROM mcp_run_grants WHERE updated_at < ?`), now.Add(-runGrantRetention))
	logx.Warn(err, "mcp.log.prune_run_grants_failed")
	return nil
}

// found is false for runs launched before grants were recorded or by a launcher without a registry.
func (r *Registry) LookupRunGrant(ctx context.Context, ref, agent, stage string) (g RunGrant, found bool, err error) {
	err = r.db.QueryRowContext(ctx, r.rb(
		`SELECT tools, locale, on_runner FROM mcp_run_grants WHERE run_ref = ? AND agent = ? AND stage = ?`),
		ref, agent, stage).Scan(&g.Tools, &g.Locale, &g.OnRunner)
	if errors.Is(err, sql.ErrNoRows) {
		return RunGrant{}, false, nil
	}
	if err != nil {
		return RunGrant{}, false, apperr.Wrap(err, "mcp.errors.read_run_grant", "run", ref)
	}
	return g, true, nil
}
