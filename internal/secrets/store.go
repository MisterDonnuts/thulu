package secrets

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

var ErrNotFound error = apperr.NotFound("secrets.errors.not_found")

// The migration 8 test re-runs this over seeded rows; every statement must stay idempotent.
const migration8DefaultProfiles = `
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT FALSE;
			UPDATE llm_profiles SET is_default = TRUE WHERE name = ('default-' || engine);
			UPDATE llm_profiles AS p
			   SET key_encrypted = k.key_encrypted,
			       key_hint      = k.key_hint,
			       provider      = k.provider,
			       updated_at    = k.updated_at
			  FROM llm_keys AS k
			 WHERE p.is_default
			   AND k.workspace_id = p.workspace_id
			   AND k.engine = p.engine
			   AND k.updated_at > p.updated_at;
			INSERT INTO llm_profiles
			  (id, workspace_id, name, engine, model, effort, base_url, org_id,
			   provider, key_encrypted, key_hint, created_at, updated_at, last_used_at, is_default)
			SELECT
			  ('lp_' || substr(md5(k.workspace_id || '/' || k.engine || '/m8'), 1, 16)),
			  k.workspace_id, ('default-' || k.engine), k.engine,
			  '', '', '', '',
			  k.provider, k.key_encrypted, k.key_hint, k.created_at, k.updated_at, NULL, TRUE
			FROM llm_keys AS k
			WHERE NOT EXISTS (
			  SELECT 1 FROM llm_profiles p
			  WHERE p.workspace_id = k.workspace_id
			    AND ((p.engine = k.engine AND p.is_default) OR p.name = ('default-' || k.engine))
			);
			DELETE FROM llm_keys AS k
			 WHERE EXISTS (
			   SELECT 1 FROM llm_profiles p
			   WHERE p.workspace_id = k.workspace_id AND p.engine = k.engine AND p.is_default
			 );
			CREATE UNIQUE INDEX IF NOT EXISTS idx_llm_profiles_one_default
			  ON llm_profiles(workspace_id, engine) WHERE is_default;
		`

// secretbox info label for stored LLM keys; changing it makes every stored key undecryptable.
// secretbox.MigrateAtStartup rewraps llm_keys under the same label.
const llmKeyInfo = "octopus-byok-v1"

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: a database that ran these migrations under another name would run them all again.
const MigrationComponent = "secrets"

// A published migration never changes; internal/schema reads this list for the migrations lock and
// lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS github_tokens (
			project_id    TEXT PRIMARY KEY,
			token         TEXT NOT NULL,
			login         TEXT NOT NULL DEFAULT '',
			scopes        TEXT NOT NULL DEFAULT '',
			validated_at  TIMESTAMP NOT NULL,
			created_at    TIMESTAMP NOT NULL,
			updated_at    TIMESTAMP NOT NULL
		)`},
		{ID: 2, SQL: `CREATE TABLE IF NOT EXISTS llm_keys (
			workspace_id  TEXT PRIMARY KEY,
			provider      TEXT NOT NULL DEFAULT 'anthropic',
			key_encrypted TEXT NOT NULL,
			key_hint      TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL,
			updated_at    TIMESTAMP NOT NULL
		)`},
		{ID: 3, SQL: `
			DROP TABLE IF EXISTS llm_keys_v2;
			CREATE TABLE llm_keys_v2 (
				workspace_id  TEXT NOT NULL,
				engine        TEXT NOT NULL DEFAULT 'claude-code',
				provider      TEXT NOT NULL DEFAULT 'anthropic',
				key_encrypted TEXT NOT NULL,
				key_hint      TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL,
				updated_at    TIMESTAMP NOT NULL,
				PRIMARY KEY (workspace_id, engine)
			);
			INSERT INTO llm_keys_v2 (workspace_id, engine, provider, key_encrypted, key_hint, created_at, updated_at)
				SELECT workspace_id, 'claude-code', provider, key_encrypted, key_hint, created_at, updated_at FROM llm_keys;
			DROP TABLE llm_keys;
			ALTER TABLE llm_keys_v2 RENAME TO llm_keys;
		`},
		{ID: 4, SQL: `
			CREATE TABLE IF NOT EXISTS llm_profiles (
				id            TEXT PRIMARY KEY,
				workspace_id  TEXT NOT NULL,
				name          TEXT NOT NULL,
				engine        TEXT NOT NULL,
				model         TEXT NOT NULL DEFAULT '',
				effort        TEXT NOT NULL DEFAULT '',
				base_url      TEXT NOT NULL DEFAULT '',
				org_id        TEXT NOT NULL DEFAULT '',
				provider      TEXT NOT NULL DEFAULT '',
				key_encrypted TEXT NOT NULL,
				key_hint      TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL,
				updated_at    TIMESTAMP NOT NULL,
				last_used_at  TIMESTAMP,
				UNIQUE (workspace_id, name)
			);
			CREATE INDEX IF NOT EXISTS idx_llm_profiles_workspace ON llm_profiles(workspace_id);
			CREATE TABLE IF NOT EXISTS agent_llm_bindings (
				workspace_id TEXT NOT NULL,
				agent_name   TEXT NOT NULL,
				profile_id   TEXT NOT NULL,
				updated_at   TIMESTAMP NOT NULL,
				updated_by   TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (workspace_id, agent_name)
			);
			CREATE INDEX IF NOT EXISTS idx_agent_llm_bindings_profile ON agent_llm_bindings(profile_id);
			INSERT INTO llm_profiles
			  (id, workspace_id, name, engine, model, effort, base_url, org_id,
			   provider, key_encrypted, key_hint, created_at, updated_at, last_used_at)
			SELECT
			  ('legacy-' || workspace_id || '-' || engine),
			  workspace_id,
			  ('default-' || engine),
			  engine,
			  '', '', '', '',
			  provider, key_encrypted, key_hint, created_at, updated_at, NULL
			FROM llm_keys
			WHERE NOT EXISTS (
			  SELECT 1 FROM llm_profiles p
			  WHERE p.workspace_id = llm_keys.workspace_id
			    AND p.name = ('default-' || llm_keys.engine)
			);
		`},
		// Retired by migration 6 but kept: migration history is append-only.
		{ID: 5, SQL: `CREATE TABLE IF NOT EXISTS cloud_credentials (
			environment_id    TEXT PRIMARY KEY,
			kind              TEXT NOT NULL,
			payload_encrypted TEXT NOT NULL,
			hint              TEXT NOT NULL DEFAULT '',
			created_at        TIMESTAMP NOT NULL,
			updated_at        TIMESTAMP NOT NULL
		)`},
		{ID: 6, SQL: `DROP TABLE IF EXISTS cloud_credentials`},
		{ID: 7, SQL: `DROP TABLE IF EXISTS github_tokens`},
		{ID: 8, SQL: migration8DefaultProfiles},
		{ID: 9, SQL: `
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS auth_header TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS small_fast_model TEXT NOT NULL DEFAULT '';
		`},
		{ID: 10, SQL: `
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS key_state TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS key_checked_at TIMESTAMP;
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS key_reason TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS key_status INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS key_host TEXT NOT NULL DEFAULT '';
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS refused_status INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS refused_at TIMESTAMP;
		`},
		{ID: 11, SQL: `
			ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS federation TEXT NOT NULL DEFAULT '';
			CREATE TABLE IF NOT EXISTS workload_identity_keys (
				kid                   TEXT PRIMARY KEY,
				private_key_encrypted TEXT NOT NULL,
				created_at            TIMESTAMP NOT NULL,
				activates_at          TIMESTAMP NOT NULL,
				retired_at            TIMESTAMP
			);
		`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "secrets.errors.create_tables")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

type LLMKeyStatus struct {
	WorkspaceID string    `json:"workspace_id"`
	Engine      string    `json:"engine,omitempty"`
	HasKey      bool      `json:"has_key"`
	Provider    string    `json:"provider,omitempty"`
	Hint        string    `json:"hint,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

const defaultEngine = "claude-code"

func (s *Store) SetLLMKey(ctx context.Context, workspaceID, engine, provider, rawKey string) error {
	if workspaceID == "" {
		return apperr.Invalid("secrets.errors.workspace_id_required")
	}
	if strings.TrimSpace(rawKey) == "" {
		return apperr.Invalid("secrets.errors.key_required")
	}
	if engine == "" {
		engine = defaultEngine
	}
	if provider == "" {
		provider = "anthropic"
	}
	existing, err := s.DefaultLLMProfile(ctx, workspaceID, engine)
	if err == nil {
		_, err = s.UpdateLLMProfile(ctx, workspaceID, existing.ID, LLMProfileInput{
			Name: existing.Name, Engine: existing.Engine,
			Model: existing.Model, Effort: existing.Effort,
			BaseURL: existing.BaseURL, OrgID: existing.OrgID,
			Provider: provider, RawKey: rawKey,
			AuthHeader: existing.AuthHeader, Region: existing.Region,
			ProjectID: existing.ProjectID, SmallFastModel: existing.SmallFastModel,
		})
		return err
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	name, err := s.freeProfileName(ctx, workspaceID, "default-"+engine)
	if err != nil {
		return err
	}
	_, err = s.CreateLLMProfile(ctx, workspaceID, LLMProfileInput{
		Name: name, Engine: engine, Provider: provider, RawKey: rawKey, IsDefault: true,
	})
	return err
}

func (s *Store) GetLLMKey(ctx context.Context, workspaceID, engine string) (provider, rawKey string, err error) {
	if engine == "" {
		engine = defaultEngine
	}
	p, err := s.DefaultLLMProfile(ctx, workspaceID, engine)
	if err == nil {
		raw, cerr := s.GetLLMProfileCredential(ctx, workspaceID, p.ID)
		return p.Provider, raw, cerr
	}
	if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	return s.legacyLLMKey(ctx, workspaceID, engine)
}

func (s *Store) legacyLLMKey(ctx context.Context, workspaceID, engine string) (provider, rawKey string, err error) {
	var enc string
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT provider, key_encrypted FROM llm_keys WHERE workspace_id = ? AND engine = ?`), workspaceID, engine)
	if scanErr := row.Scan(&provider, &enc); errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if scanErr != nil {
		return "", "", scanErr
	}
	rawKey, err = secretbox.Decrypt(llmKeyInfo, enc)
	if err != nil {
		return "", "", apperr.Wrap(err, "secrets.errors.decrypt_llm_key")
	}
	return provider, rawKey, nil
}

func (s *Store) StatusLLMKey(ctx context.Context, workspaceID, engine string) (*LLMKeyStatus, error) {
	if engine == "" {
		engine = defaultEngine
	}
	all, err := s.ListLLMKeys(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if st, ok := all[engine]; ok {
		return st, nil
	}
	return &LLMKeyStatus{WorkspaceID: workspaceID, Engine: engine, HasKey: false}, nil
}

func (s *Store) ListLLMKeys(ctx context.Context, workspaceID string) (map[string]*LLMKeyStatus, error) {
	out := map[string]*LLMKeyStatus{}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT engine, provider, key_hint, updated_at FROM llm_keys WHERE workspace_id = ?`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eng, provider, hint string
		var updatedAt time.Time
		if scanErr := rows.Scan(&eng, &provider, &hint, &updatedAt); scanErr != nil {
			return nil, scanErr
		}
		out[eng] = &LLMKeyStatus{
			WorkspaceID: workspaceID, Engine: eng, HasKey: true,
			Provider: provider, Hint: hint, UpdatedAt: updatedAt,
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	profiles, err := s.ListLLMProfiles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		if !p.IsDefault {
			continue
		}
		out[p.Engine] = &LLMKeyStatus{
			WorkspaceID: workspaceID, Engine: p.Engine, HasKey: true,
			Provider: p.Provider, Hint: p.KeyHint, UpdatedAt: p.UpdatedAt,
		}
	}
	return out, nil
}

func (s *Store) DeleteLLMKey(ctx context.Context, workspaceID, engine string) error {
	if engine == "" {
		engine = defaultEngine
	}
	if p, err := s.DefaultLLMProfile(ctx, workspaceID, engine); err == nil {
		if derr := s.DeleteLLMProfile(ctx, workspaceID, p.ID); derr != nil {
			return derr
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM llm_keys WHERE workspace_id = ? AND engine = ?`), workspaceID, engine)
	return err
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	_, firstErr := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM llm_keys WHERE workspace_id = ?`), workspaceID)
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM agent_llm_bindings WHERE workspace_id = ?`), workspaceID); err != nil && firstErr == nil {
		firstErr = err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM llm_profiles WHERE workspace_id = ?`), workspaceID); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
