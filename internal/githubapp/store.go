package githubapp

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

var ErrNotConfigured error = apperr.Precondition("githubapp.errors.not_configured")

var ErrInstallationNotFound error = apperr.NotFound("githubapp.errors.installation_not_found")

// Changing this label makes stored App secrets undecryptable; it must differ from the ghtoken/byok
// labels.
const ghAppInfo = "octopus-ghapp-v1"

type AppConfig struct {
	AppID         int64  `json:"app_id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret"`
	UpdatedAt     time.Time
}

type Installation struct {
	InstallationID int64      `json:"installation_id"`
	WorkspaceID    string     `json:"workspace_id,omitempty"`
	AccountLogin   string     `json:"account_login"`
	AccountType    string     `json:"account_type,omitempty"`
	RepoSelection  string     `json:"repository_selection,omitempty"`
	InstalledBy    string     `json:"installed_by,omitempty"`
	SuspendedAt    *time.Time `json:"suspended_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "githubapp"

// Append only: a published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS github_app_config (
			id             INTEGER PRIMARY KEY CHECK (id = 1),
			app_id         BIGINT NOT NULL,
			slug           TEXT NOT NULL DEFAULT '',
			client_id      TEXT NOT NULL DEFAULT '',
			client_secret  TEXT NOT NULL DEFAULT '',
			private_key    TEXT NOT NULL,
			webhook_secret TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL,
			updated_at     TIMESTAMP NOT NULL
		)`},
		{ID: 2, SQL: `CREATE TABLE IF NOT EXISTS github_installations (
			installation_id      BIGINT PRIMARY KEY,
			workspace_id         TEXT NOT NULL DEFAULT '',
			account_login        TEXT NOT NULL DEFAULT '',
			account_type         TEXT NOT NULL DEFAULT '',
			repository_selection TEXT NOT NULL DEFAULT '',
			installed_by         TEXT NOT NULL DEFAULT '',
			suspended_at         TIMESTAMP,
			created_at           TIMESTAMP NOT NULL,
			updated_at           TIMESTAMP NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_github_installations_workspace ON github_installations(workspace_id)`},
		{ID: 3, SQL: `CREATE TABLE IF NOT EXISTS github_install_states (
			state_hash   TEXT PRIMARY KEY,
			binder_hash  TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			user_id      TEXT NOT NULL DEFAULT '',
			expires_at   TIMESTAMP NOT NULL,
			created_at   TIMESTAMP NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_github_install_states_expires ON github_install_states(expires_at)`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "githubapp.errors.create_tables")
	}
	return &Store{db: db}, nil
}

func (s *Store) SetConfig(ctx context.Context, cfg AppConfig) error {
	if cfg.AppID <= 0 {
		return apperr.Invalid("githubapp.errors.app_id_required")
	}
	if cfg.PrivateKey == "" {
		return apperr.Invalid("githubapp.errors.private_key_required")
	}
	encKey, err := secretbox.Encrypt(ghAppInfo, cfg.PrivateKey)
	if err != nil {
		return apperr.Wrap(err, "githubapp.errors.encrypt_private_key")
	}
	encClientSecret, err := secretbox.Encrypt(ghAppInfo, cfg.ClientSecret)
	if err != nil {
		return apperr.Wrap(err, "githubapp.errors.encrypt_client_secret")
	}
	encWebhookSecret, err := secretbox.Encrypt(ghAppInfo, cfg.WebhookSecret)
	if err != nil {
		return apperr.Wrap(err, "githubapp.errors.encrypt_webhook_secret")
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO github_app_config(id, app_id, slug, client_id, client_secret, private_key, webhook_secret, created_at, updated_at)
		 VALUES(1, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   app_id         = excluded.app_id,
		   slug           = excluded.slug,
		   client_id      = excluded.client_id,
		   client_secret  = excluded.client_secret,
		   private_key    = excluded.private_key,
		   webhook_secret = excluded.webhook_secret,
		   updated_at     = excluded.updated_at`),
		cfg.AppID, cfg.Slug, cfg.ClientID, encClientSecret, encKey, encWebhookSecret, now, now,
	)
	return err
}

func (s *Store) GetConfig(ctx context.Context) (*AppConfig, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT app_id, slug, client_id, client_secret, private_key, webhook_secret, updated_at
		 FROM github_app_config WHERE id = 1`))
	var out AppConfig
	err := row.Scan(&out.AppID, &out.Slug, &out.ClientID, &out.ClientSecret,
		&out.PrivateKey, &out.WebhookSecret, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	if out.PrivateKey, err = secretbox.Decrypt(ghAppInfo, out.PrivateKey); err != nil {
		return nil, apperr.Wrap(err, "githubapp.errors.decrypt_private_key")
	}
	if out.ClientSecret, err = secretbox.Decrypt(ghAppInfo, out.ClientSecret); err != nil {
		return nil, apperr.Wrap(err, "githubapp.errors.decrypt_client_secret")
	}
	if out.WebhookSecret, err = secretbox.Decrypt(ghAppInfo, out.WebhookSecret); err != nil {
		return nil, apperr.Wrap(err, "githubapp.errors.decrypt_webhook_secret")
	}
	return &out, nil
}

// Installation rows are kept: deleting them would silently unlink projects.
func (s *Store) DeleteConfig(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_app_config WHERE id = 1`)
	return err
}

// Empty incoming fields keep the stored value, and the workspace is set only while unclaimed: no
// upsert moves a claim.
const installationUpsert = `INSERT INTO github_installations(installation_id, workspace_id, account_login, account_type,
	   repository_selection, installed_by, suspended_at, created_at, updated_at)
	 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(installation_id) DO UPDATE SET
	   workspace_id = CASE WHEN github_installations.workspace_id = ''
	     THEN excluded.workspace_id ELSE github_installations.workspace_id END,
	   account_login = CASE WHEN excluded.account_login = ''
	     THEN github_installations.account_login ELSE excluded.account_login END,
	   account_type = CASE WHEN excluded.account_type = ''
	     THEN github_installations.account_type ELSE excluded.account_type END,
	   repository_selection = CASE WHEN excluded.repository_selection = ''
	     THEN github_installations.repository_selection ELSE excluded.repository_selection END,
	   installed_by = CASE WHEN excluded.installed_by = ''
	     THEN github_installations.installed_by ELSE excluded.installed_by END,
	   suspended_at = COALESCE(excluded.suspended_at, github_installations.suspended_at),
	   updated_at   = excluded.updated_at`

// Webhook payloads such as installation_target carry an abbreviated installation; a partial
// delivery must not blank the row.
func (s *Store) UpsertInstallation(ctx context.Context, inst Installation) error {
	if inst.InstallationID <= 0 {
		return apperr.Invalid("githubapp.errors.installation_id_required")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(installationUpsert),
		inst.InstallationID, inst.WorkspaceID, inst.AccountLogin, inst.AccountType,
		inst.RepoSelection, inst.InstalledBy, inst.SuspendedAt, now, now,
	)
	return err
}

// Check and write are one statement so two workspaces racing for an unclaimed installation cannot
// both win.
func (s *Store) ClaimInstallation(ctx context.Context, inst Installation) error {
	if inst.InstallationID <= 0 {
		return apperr.Invalid("githubapp.errors.installation_id_required")
	}
	if inst.WorkspaceID == "" {
		return apperr.Invalid("githubapp.errors.claim_workspace_required")
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.rb(installationUpsert+`
	 WHERE github_installations.workspace_id IN ('', excluded.workspace_id)`),
		inst.InstallationID, inst.WorkspaceID, inst.AccountLogin, inst.AccountType,
		inst.RepoSelection, inst.InstalledBy, inst.SuspendedAt, now, now,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrInstallationClaimed
	}
	return nil
}

// Operator-only: no install flow may reach this.
func (s *Store) ReassignInstallation(ctx context.Context, id int64, workspaceID string) (string, error) {
	var prev string
	err := s.db.QueryRowContext(ctx, s.rb(
		`UPDATE github_installations AS g SET workspace_id = ?, updated_at = ?
		 FROM (SELECT installation_id, workspace_id FROM github_installations
		       WHERE installation_id = ? FOR UPDATE) AS old
		 WHERE g.installation_id = old.installation_id
		 RETURNING old.workspace_id`),
		workspaceID, time.Now().UTC(), id).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInstallationNotFound
	}
	return prev, err
}

func (s *Store) GetInstallation(ctx context.Context, id int64) (*Installation, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT installation_id, workspace_id, account_login, account_type,
		   repository_selection, installed_by, suspended_at, created_at, updated_at
		 FROM github_installations WHERE installation_id = ?`), id)
	return scanInstallation(row)
}

// Installation IDs are low-entropy; restricted callers must never see other tenants' or unclaimed
// rows.
func (s *Store) ListInstallations(ctx context.Context, workspaceID string, unrestricted bool) ([]Installation, error) {
	q := `SELECT installation_id, workspace_id, account_login, account_type,
	        repository_selection, installed_by, suspended_at, created_at, updated_at
	      FROM github_installations`
	var args []any
	if !unrestricted {
		q += ` WHERE workspace_id = ?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY account_login`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Installation
	for rows.Next() {
		inst, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inst)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInstallation(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM github_installations WHERE installation_id = ?`), id)
	return err
}

func (s *Store) SetSuspended(ctx context.Context, id int64, suspended bool) error {
	var at any
	if suspended {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE github_installations SET suspended_at = ?, updated_at = ? WHERE installation_id = ?`),
		at, time.Now().UTC(), id)
	return err
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM github_installations WHERE workspace_id = ?`), workspaceID)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanInstallation(row scanner) (*Installation, error) {
	var (
		out         Installation
		suspendedAt sql.NullTime
	)
	err := row.Scan(&out.InstallationID, &out.WorkspaceID, &out.AccountLogin, &out.AccountType,
		&out.RepoSelection, &out.InstalledBy, &suspendedAt, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInstallationNotFound
	}
	if err != nil {
		return nil, err
	}
	if suspendedAt.Valid {
		t := suspendedAt.Time
		out.SuspendedAt = &t
	}
	return &out, nil
}
