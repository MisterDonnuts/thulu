package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type RunUsage struct {
	Period    string    `json:"period"`
	Used      int       `json:"used"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetsAt  time.Time `json:"resets_at"`
}

func (s *Store) period() (string, time.Time) {
	now := s.now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start.Format("2006-01"), start.AddDate(0, 1, 0)
}

func usageOf(period string, resets time.Time, used, limit int) RunUsage {
	u := RunUsage{Period: period, Used: used, Limit: limit, Remaining: -1, ResetsAt: resets}
	if limit > 0 {
		u.Remaining = max(limit-used, 0)
	}
	return u
}

func (s *Store) RunUsage(ctx context.Context, workspaceID string) (RunUsage, error) {
	ent, err := s.Get(workspaceID)
	if err != nil {
		return RunUsage{}, err
	}
	period, resets := s.period()
	var used int
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT runs FROM workspace_run_usage WHERE workspace_id = ? AND period = ?`),
		workspaceID, period).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RunUsage{}, err
	}
	return usageOf(period, resets, used, ent.RunQuotaMonthly), nil
}

// Check and increment are one statement so concurrent launches can't overshoot the quota. ok=false:
// nothing counted, caller must not launch.
func (s *Store) ConsumeRun(ctx context.Context, workspaceID string) (RunUsage, bool, error) {
	period, resets := s.period()
	if workspaceID == "" {
		return usageOf(period, resets, 0, 0), true, nil
	}
	ent, err := s.Get(workspaceID)
	if err != nil {
		return RunUsage{}, false, err
	}
	limit := ent.RunQuotaMonthly
	q := `INSERT INTO workspace_run_usage (workspace_id, period, runs) VALUES (?, ?, 1)
		ON CONFLICT (workspace_id, period) DO UPDATE SET runs = workspace_run_usage.runs + 1`
	args := []any{workspaceID, period}
	if limit > 0 {
		q += ` WHERE workspace_run_usage.runs < ?`
		args = append(args, limit)
	}
	var used int
	err = s.db.QueryRowContext(ctx, s.rb(q+` RETURNING runs`), args...).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return usageOf(period, resets, limit, limit), false, nil
	}
	if err != nil {
		return RunUsage{}, false, err
	}
	return usageOf(period, resets, used, limit), true, nil
}

func (s *Store) RefundRun(ctx context.Context, workspaceID, period string) error {
	if workspaceID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE workspace_run_usage SET runs = runs - 1
		 WHERE workspace_id = ? AND period = ? AND runs > 0`), workspaceID, period)
	return err
}
