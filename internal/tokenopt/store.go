package tokenopt

import (
	"context"
	"database/sql"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// schema_migrations records this package's history under this name. Never change it.
const MigrationComponent = "tokenopt"

// A published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		// Migration 2 drops this table; this one stays because databases that ran it record it.
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS token_optimizer_settings (
			workspace_id     TEXT PRIMARY KEY,
			default_level    TEXT NOT NULL DEFAULT 'off',
			read_profile_id  TEXT NOT NULL DEFAULT '',
			read_model       TEXT NOT NULL DEFAULT '',
			write_profile_id TEXT NOT NULL DEFAULT '',
			write_model      TEXT NOT NULL DEFAULT '',
			threshold_bytes  INTEGER NOT NULL DEFAULT 0,
			updated_at       TIMESTAMPTZ NOT NULL,
			updated_by       TEXT NOT NULL DEFAULT ''
		)`},
		{ID: 2, SQL: `DROP TABLE IF EXISTS token_optimizer_settings`, Contract: &migrate.Contract{
			StoppedUsingIn: "0.3.2",
			Reason:         "token_optimizer_settings is new in 0.3.3: no released binary reads it", //i18n:ignore migration contract note
		}},
	}, nil
}

func Migrate(db *sql.DB) error {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return apperr.Wrap(err, "tokenopt.errors.migrations")
	}
	return nil
}
