package cli

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/stage"
)

func installEnvFile() *initwizard.EnvFile {
	root, err := resolveRoot("")
	if err != nil {
		return nil
	}
	f, err := initwizard.LoadEnvFile(filepath.Join(root, "deploy", ".env"))
	if err != nil {
		return nil
	}
	return f
}

func requireDSN() (string, error) {
	if dsn := strings.TrimSpace(os.Getenv("OCTOPUS_DATABASE_URL")); dsn != "" {
		return dsn, nil
	}
	if dsn := initwizard.HostDatabaseURL(installEnvFile()); dsn != "" {
		return dsn, nil
	}
	return "", apperr.Invalid("cli.errors.database_url_required")
}

func openStore[T any](build func(*sql.DB) (T, error)) (T, func(), error) {
	var zero T
	dsn, err := requireDSN()
	if err != nil {
		return zero, nil, err
	}
	handle, err := db.Open(dsn)
	if err != nil {
		return zero, nil, apperr.Wrap(err, "cli.errors.open_database")
	}
	store, err := build(handle)
	if err != nil {
		_ = handle.Close()
		return zero, nil, err
	}
	return store, func() { _ = handle.Close() }, nil
}

type workspaceRow struct{ id, slug string }

func listWorkspaces(ctx context.Context, handle *sql.DB) ([]workspaceRow, error) {
	rows, err := handle.QueryContext(ctx, `SELECT id, slug FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, apperr.Wrap(err, "cli.errors.list_workspaces")
	}
	defer func() { _ = rows.Close() }()
	var out []workspaceRow
	for rows.Next() {
		var w workspaceRow
		if err := rows.Scan(&w.id, &w.slug); err != nil {
			return nil, apperr.Wrap(err, "cli.errors.list_workspaces")
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "cli.errors.list_workspaces")
	}
	return out, nil
}

// fallback must be false for keys: a key minted into the fallback workspace answers to no workspace
// anyone can sign into.
func resolveWorkspace(ctx context.Context, handle *sql.DB, named string, fallback bool) (string, error) {
	all, err := listWorkspaces(ctx, handle)
	if err != nil {
		return "", err
	}
	named = strings.TrimSpace(named)
	if fallback && named == "" && len(all) > 0 {
		holds, holdsErr := fallbackHoldsData(ctx, handle)
		if holdsErr != nil {
			return "", holdsErr
		}
		all = withFallback(all, holds)
	}
	return pickWorkspace(all, named, fallback)
}

func withFallback(all []workspaceRow, holds bool) []workspaceRow {
	if !holds {
		return all
	}
	return append(slices.Clone(all), workspaceRow{id: stage.FallbackWorkspaceID, slug: stage.FallbackWorkspaceID})
}

// Boards do not count: the daemon seeds one under the fallback on first boot, before any workspace
// exists.
func fallbackHoldsData(ctx context.Context, handle *sql.DB) (bool, error) {
	var holds bool
	err := handle.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM projects WHERE workspace_id = $1) OR
		EXISTS (SELECT 1 FROM tasks WHERE workspace_id = $1)`, stage.FallbackWorkspaceID).Scan(&holds)
	if err != nil {
		return false, apperr.Wrap(err, "cli.errors.list_workspaces")
	}
	return holds, nil
}

func pickWorkspace(all []workspaceRow, named string, fallback bool) (string, error) {
	if named != "" {
		for _, w := range all {
			if w.id == named || w.slug == named {
				return w.id, nil
			}
		}
		if fallback && named == stage.FallbackWorkspaceID {
			return named, nil
		}
		return "", apperr.NotFound("cli.errors.workspace_not_found", "id", named)
	}
	switch len(all) {
	case 0:
		if fallback {
			return stage.FallbackWorkspaceID, nil
		}
		return "", apperr.Invalid("cli.errors.no_workspaces")
	case 1:
		return all[0].id, nil
	}
	names := make([]string, len(all))
	for i, w := range all {
		names[i] = w.slug
	}
	slices.Sort(names)
	return "", apperr.Invalid("cli.errors.workspace_ambiguous", "workspaces", strings.Join(names, ", "))
}
