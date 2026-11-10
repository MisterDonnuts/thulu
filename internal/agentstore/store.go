package agentstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	KindAgent = "agent"
	KindSkill = "skill"
	KindTool  = "tool"
)

const ToolManualFile = "skill.md"

const GlobalTenant = ""

const (
	ActionCreated  = "created"
	ActionUpdated  = "updated"
	ActionDeleted  = "deleted"
	ActionRestored = "restored"
)

var (
	ErrNotFound        error = apperr.NotFound("agentstore.errors.not_found")
	ErrExists          error = apperr.Conflict("agentstore.errors.exists")
	ErrInvalidName     error = apperr.Invalid("agentstore.errors.invalid_name")
	ErrInvalidPath     error = apperr.Invalid("agentstore.errors.invalid_path")
	ErrInvalidKind     error = apperr.Invalid("agentstore.errors.invalid_kind")
	ErrSecretInBody    error = apperr.Unprocessable("agentstore.errors.secret_in_body")
	ErrBodyTooLarge    error = apperr.TooLarge("agentstore.errors.body_too_large")
	ErrReservedOwner   error = apperr.Invalid("agentstore.errors.reserved_owner")
	ErrInvalidMode     error = apperr.Invalid("agentstore.errors.invalid_mode")
	ErrSettingsRestore error = apperr.Invalid("agentstore.errors.settings_restore")
)

const maxBodyBytes = 1 << 20

// Names must be safe as a path component, an env-var fragment and a compose label value at once.
// The leading underscore exists for the `_base` skill.
var nameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]{0,63}$`)

// "octopus" is the daemon's own data directory under the agents dir; it must never import as an
// agent.
var reservedOwners = map[string]struct{}{
	"octopus": {},
	"runs":    {},
}

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DB() *sql.DB { return s.db }

// Never change: a database that ran these migrations under another name would run them all again.
const MigrationComponent = "agentstore"

func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	base := []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS content_blobs (
			sha256     TEXT PRIMARY KEY,
			body       BYTEA NOT NULL,
			size       INTEGER NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)`},
		{ID: 2, SQL: `CREATE TABLE IF NOT EXISTS content_files (
			tenant_id  TEXT NOT NULL DEFAULT '',
			kind       TEXT NOT NULL,
			owner      TEXT NOT NULL,
			path       TEXT NOT NULL,
			blob_sha   TEXT NOT NULL REFERENCES content_blobs(sha256),
			updated_at TIMESTAMPTZ NOT NULL,
			updated_by TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (tenant_id, kind, owner, path)
		)`},
		{ID: 3, SQL: `CREATE TABLE IF NOT EXISTS agents (
			tenant_id          TEXT NOT NULL DEFAULT '',
			name               TEXT NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			type               TEXT NOT NULL DEFAULT '',
			engine             TEXT NOT NULL DEFAULT '',
			supported_engines  JSONB NOT NULL DEFAULT '[]',
			model              TEXT NOT NULL DEFAULT '',
			effort             TEXT NOT NULL DEFAULT '',
			skills_mode        TEXT NOT NULL DEFAULT 'auto',
			works_without_repo BOOLEAN NOT NULL DEFAULT false,
			valid_stages       JSONB NOT NULL DEFAULT '[]',
			turns_by_stage     JSONB NOT NULL DEFAULT '{}',
			skills_by_stage    JSONB NOT NULL DEFAULT '{}',
			mcp_by_stage       JSONB NOT NULL DEFAULT '{}',
			auditor            JSONB,
			params             JSONB NOT NULL DEFAULT '[]',
			image              TEXT NOT NULL DEFAULT '',
			is_template        BOOLEAN NOT NULL DEFAULT false,
			created_by         TEXT NOT NULL DEFAULT '',
			created_at         TIMESTAMPTZ NOT NULL,
			updated_at         TIMESTAMPTZ NOT NULL,
			disabled_at        TIMESTAMPTZ,
			PRIMARY KEY (tenant_id, name)
		)`},
		{ID: 4, SQL: `CREATE INDEX IF NOT EXISTS content_files_blob
			ON content_files (blob_sha)`},
		{ID: 5, SQL: `CREATE TABLE IF NOT EXISTS content_revisions (
			id            BIGSERIAL PRIMARY KEY,
			tenant_id     TEXT NOT NULL DEFAULT '',
			kind          TEXT NOT NULL,
			owner         TEXT NOT NULL,
			path          TEXT NOT NULL,
			blob_sha      TEXT,
			prev_blob_sha TEXT,
			action        TEXT NOT NULL,
			actor         TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL
		)`},
		{ID: 6, SQL: `CREATE INDEX IF NOT EXISTS content_revisions_target
			ON content_revisions (tenant_id, kind, owner, path, id DESC)`},
		{ID: 7, SQL: `CREATE INDEX IF NOT EXISTS content_revisions_blob
			ON content_revisions (blob_sha)`},
	}
	catalog := catalogMigrations()
	out := make([]migrate.Migration, 0, len(base)+len(catalog)+1)
	out = append(out, base...)
	for i, q := range catalog {
		out = append(out, migrate.Migration{ID: 8 + i, SQL: q})
	}
	out = append(out, renameMigration(8+len(catalog)))
	return out, nil
}

func New(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.create_tables")
	}
	return &Store{db: db}, nil
}

func ValidateName(s string) error {
	if !nameRe.MatchString(s) {
		return ErrInvalidName
	}
	if _, bad := reservedOwners[s]; bad {
		return ErrReservedOwner
	}
	return nil
}

// Names the daemon signs its own task writes with; an agent named "user" would post comments that
// read as the operator's.
var reservedAgentNames = map[string]struct{}{
	"user":            {},
	"system":          {},
	auth.AuditorAgent: {},
	"automation":      {},
}

func ValidateAgentName(s string) error {
	if err := ValidateName(s); err != nil {
		return err
	}
	if _, bad := reservedAgentNames[s]; bad {
		return ErrReservedOwner
	}
	return nil
}

func ValidateKind(k string) error {
	switch k {
	case KindAgent, KindSkill, KindTool:
		return nil
	}
	return ErrInvalidKind
}

// The store is the only thing between an API caller and the layout of a run bundle.
func ValidatePath(p string) error {
	if p == "" || len(p) > 256 {
		return ErrInvalidPath
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return ErrInvalidPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return ErrInvalidPath
		}
	}
	return nil
}

// Hard reject: agent text travels in the clear to every run granted it, so a pasted key is handed
// to
// every agent. Credentials belong in the credential manager.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-(api|oat)[A-Za-z0-9_\-]{10,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-{5}BEGIN [A-Z ]*PRIVATE KEY-{5}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`),
}

func ScanForSecrets(body []byte) error {
	for _, re := range secretPatterns {
		if re.Match(body) {
			return ErrSecretInBody
		}
	}
	return nil
}

func blobSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (s *Store) putBlob(ctx context.Context, tx *sql.Tx, body []byte) (string, error) {
	sha := blobSHA(body)
	_, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO content_blobs (sha256, body, size, created_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT (sha256) DO NOTHING`),
		sha, body, len(body), time.Now().UTC())
	if err != nil {
		return "", apperr.Wrap(err, "agentstore.errors.put_blob")
	}
	return sha, nil
}
