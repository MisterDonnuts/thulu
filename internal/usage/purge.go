package usage

import (
	"context"
	"database/sql"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The store does not remove these files: it cannot know which roots a stored path is trusted under,
// so callers must check.
type RunFiles struct {
	LogsFile  string
	OutputDir string
}

// usage_daily is kept on purpose: it is spend history and carries no task id.
func (s *Store) DeleteByTask(ctx context.Context, taskID string) ([]RunFiles, error) {
	if taskID == "" {
		// '' would match every unattributed session.
		return nil, nil
	}
	return s.deleteSessionsWhere(ctx, `task_id = ?`, taskID)
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) ([]RunFiles, error) {
	if workspaceID == "" {
		// '' would match every unattributed row.
		return nil, nil
	}
	files, err := s.deleteSessionsWhere(ctx, `workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM usage_daily WHERE workspace_id = ?`), workspaceID); err != nil {
		return files, apperr.Wrap(err, "usage.errors.delete_rollup")
	}
	return files, nil
}

// where is interpolated into SQL: pass only fixed clauses from this file.
func (s *Store) deleteSessionsWhere(ctx context.Context, where string, arg any) ([]RunFiles, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range []string{"session_tool_events", "session_calls", "session_cost_breakdown"} {
		if _, err = tx.ExecContext(ctx, s.rb(
			`DELETE FROM `+table+` WHERE session_id IN (SELECT id FROM sessions WHERE `+where+`)`), arg); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.delete_tool_events")
		}
	}
	if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM delegations WHERE `+where), arg); err != nil {
		return nil, apperr.Wrap(err, "usage.errors.delete_tool_events")
	}
	rows, err := tx.QueryContext(ctx, s.rb(
		`DELETE FROM sessions WHERE `+where+` RETURNING logs_file, output_dir`), arg)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.delete_sessions")
	}
	files, err := scanRunFiles(rows)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.delete_sessions")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return files, nil
}

func scanRunFiles(rows *sql.Rows) ([]RunFiles, error) {
	defer rows.Close()
	var out []RunFiles
	for rows.Next() {
		var f RunFiles
		if err := rows.Scan(&f.LogsFile, &f.OutputDir); err != nil {
			return nil, err
		}
		if f.LogsFile != "" || f.OutputDir != "" {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}
