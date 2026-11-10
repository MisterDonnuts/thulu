package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Workspace values live apart from the settings table, which GET /settings serves whole, so one
// tenant's write never reaches what another reads.

var workspaceKeys = map[string]bool{
	AgentMaxRunMinutesKey: true,
}

func KnownWorkspaceKey(key string) bool { return workspaceKeys[key] }

func (s *Store) GetWorkspaceRaw(ctx context.Context, workspaceID, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT value FROM workspace_settings WHERE workspace_id = ? AND key = ?`),
		workspaceID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func (s *Store) PutWorkspaceRaw(ctx context.Context, workspaceID, key, value string) error {
	if workspaceID == "" {
		return apperr.Invalid("settings.errors.workspace_required")
	}
	if !KnownWorkspaceKey(key) {
		return apperr.Invalid("settings.errors.not_workspace_key", "key", key)
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workspace_settings(workspace_id, key, value, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(workspace_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`),
		workspaceID, key, value, time.Now().UTC(),
	)
	return err
}

func (s *Store) DeleteWorkspaceSetting(ctx context.Context, workspaceID, key string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_settings WHERE workspace_id = ? AND key = ?`), workspaceID, key)
	return err
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_settings WHERE workspace_id = ?`), workspaceID)
	return err
}

func (s *Store) ListWorkspace(ctx context.Context, workspaceID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT key, value FROM workspace_settings WHERE workspace_id = ? ORDER BY key`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) workspaceInt(ctx context.Context, workspaceID, key string) (*int, error) {
	if workspaceID == "" {
		return nil, nil
	}
	raw, err := s.GetWorkspaceRaw(ctx, workspaceID, key)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var n int
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return nil, err
	}
	return &n, nil
}
