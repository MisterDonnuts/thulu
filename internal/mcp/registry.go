package mcp

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Registration struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   string    `json:"created_by,omitempty"`
	// Only servers that call back into the daemon may get the run's API key; giving it to a
	// third-party server leaks a working workspace credential.
	ForwardsCredential bool   `json:"forwards_credential"`
	Transport          string `json:"transport"`
	Auth               string `json:"auth"`
	// An id, or a name resolved in the calling run's own workspace, so each tenant gets its own
	// token.
	CredentialRef  string `json:"credential_ref,omitempty"`
	HeaderTemplate string `json:"header_template,omitempty"`
}

const (
	TransportSSE  = "sse"
	TransportHTTP = "http"
)

const (
	AuthNone       = "none"
	AuthCredential = "credential"
)

// URL and Description keep the older "empty means unchanged" reading; the pointer fields use nil.
type Patch struct {
	URL                string
	Description        string
	ForwardsCredential *bool
	Transport          *string
	Auth               *string
	CredentialRef      *string
	HeaderTemplate     *string
}

func (r Registration) EnvKey() string { return NameToEnvKey(r.Name) }

// Must mirror images/snippets/mcp-config.sh (uppercase, - to _, MCP_<...>_URL).
func NameToEnvKey(name string) string {
	up := strings.ToUpper(strings.TrimSpace(name))
	up = strings.ReplaceAll(up, "-", "_")
	return "MCP_" + up + "_URL"
}

// Lossy: - and _ fold together; round-trips only because nameRe allows no underscores.
func EnvKeyToName(key string) string {
	if !strings.HasPrefix(key, "MCP_") || !strings.HasSuffix(key, "_URL") || len(key) <= len("MCP__URL") {
		return ""
	}
	name := strings.TrimSuffix(strings.TrimPrefix(key, "MCP_"), "_URL")
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

var (
	ErrRegistrationExists   error = apperr.Conflict("mcp.errors.registration_exists")
	ErrRegistrationNotFound error = apperr.NotFound("mcp.errors.registration_not_found")
	ErrInvalidName          error = apperr.Invalid("mcp.errors.invalid_name")
	ErrInvalidURL           error = apperr.Invalid("mcp.errors.invalid_url")
	ErrReservedName         error = apperr.Conflict("mcp.errors.reserved_name")
	ErrInvalidTransport     error = apperr.Invalid("mcp.errors.invalid_transport")
	ErrInvalidAuth          error = apperr.Invalid("mcp.errors.invalid_auth")
	ErrCredentialRefNeeded  error = apperr.Invalid("mcp.errors.credential_ref_required")
)

// thulu-sandbox's URL is computed per task by the dispatcher; a static registration would fight it.
var reservedNames = map[string]struct{}{
	"thulu-sandbox": {},
}

type Registry struct {
	db *sql.DB
}

func (r *Registry) rb(q string) string { return dbpkg.Rebind(q) }

// Never change it: databases that already ran these migrations would run them all again.
const MigrationComponent = "mcp_registrations"

// A published migration never changes; internal/schema reads this list for the migrations lock and
// lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS mcp_registrations (
			name        TEXT PRIMARY KEY,
			url         TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_by  TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL,
			updated_at  TIMESTAMP NOT NULL
		)`},
		{ID: 2, SQL: `ALTER TABLE mcp_registrations
			ADD COLUMN IF NOT EXISTS forwards_credential BOOLEAN NOT NULL DEFAULT FALSE`},
		{ID: 3, SQL: `UPDATE mcp_registrations SET forwards_credential = TRUE
			WHERE name IN ('octopus-api', 'argus-github', 'argus-browser', 'argus-cloud')`},
		{ID: 4, SQL: `ALTER TABLE mcp_registrations
			ADD COLUMN IF NOT EXISTS transport       TEXT NOT NULL DEFAULT 'sse',
			ADD COLUMN IF NOT EXISTS auth            TEXT NOT NULL DEFAULT 'none',
			ADD COLUMN IF NOT EXISTS credential_ref  TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS header_template TEXT NOT NULL DEFAULT ''`},
		{ID: 5, SQL: `CREATE TABLE IF NOT EXISTS mcp_run_grants (
			run_ref    TEXT NOT NULL,
			agent      TEXT NOT NULL,
			stage      TEXT NOT NULL,
			tools      TEXT NOT NULL DEFAULT '',
			locale     TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (run_ref, agent, stage)
		)`},
		{ID: 6, SQL: `ALTER TABLE mcp_run_grants ADD COLUMN IF NOT EXISTS on_runner BOOLEAN NOT NULL DEFAULT FALSE`},
	}, nil
}

func NewRegistry(db *sql.DB) (*Registry, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.create_registrations_table")
	}
	return &Registry{db: db}, nil
}

func ValidateName(s string) error {
	if !nameRe.MatchString(strings.TrimSpace(s)) {
		return ErrInvalidName
	}
	if _, bad := reservedNames[strings.TrimSpace(s)]; bad {
		return ErrReservedName
	}
	return nil
}

func ValidateURL(s string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return ErrInvalidURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrInvalidURL
	}
	if u.Host == "" {
		return ErrInvalidURL
	}
	return nil
}

func normalize(reg *Registration) error {
	reg.Transport = strings.ToLower(strings.TrimSpace(reg.Transport))
	if reg.Transport == "" {
		reg.Transport = TransportSSE
	}
	if reg.Transport != TransportSSE && reg.Transport != TransportHTTP {
		return ErrInvalidTransport
	}
	reg.Auth = strings.ToLower(strings.TrimSpace(reg.Auth))
	if reg.Auth == "" {
		reg.Auth = AuthNone
	}
	reg.CredentialRef = strings.TrimSpace(reg.CredentialRef)
	reg.HeaderTemplate = strings.TrimSpace(reg.HeaderTemplate)
	switch reg.Auth {
	case AuthNone:
		// A leftover reference would revive on re-enabling auth, pointing at whatever then has that
		// name.
		reg.CredentialRef, reg.HeaderTemplate = "", ""
	case AuthCredential:
		if reg.CredentialRef == "" {
			return ErrCredentialRefNeeded
		}
		if reg.HeaderTemplate != "" {
			if _, err := credentials.ParseHeaderTemplate(reg.HeaderTemplate); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidAuth
	}
	return nil
}

const registrationColumns = `name, url, description, created_by, created_at, updated_at, forwards_credential,
		transport, auth, credential_ref, header_template`

func scanRegistration(row interface{ Scan(...any) error }, reg *Registration) error {
	return row.Scan(&reg.Name, &reg.URL, &reg.Description, &reg.CreatedBy,
		&reg.CreatedAt, &reg.UpdatedAt, &reg.ForwardsCredential,
		&reg.Transport, &reg.Auth, &reg.CredentialRef, &reg.HeaderTemplate)
}

func (r *Registry) List(ctx context.Context) ([]Registration, error) {
	rows, err := r.db.QueryContext(ctx, r.rb(
		`SELECT `+registrationColumns+` FROM mcp_registrations ORDER BY name`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registration{}
	for rows.Next() {
		var reg Registration
		if err := scanRegistration(rows, &reg); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

func (r *Registry) Get(ctx context.Context, name string) (Registration, error) {
	var reg Registration
	err := scanRegistration(r.db.QueryRowContext(ctx, r.rb(
		`SELECT `+registrationColumns+` FROM mcp_registrations WHERE name = ?`), name), &reg)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrRegistrationNotFound
	}
	if err != nil {
		return Registration{}, err
	}
	return reg, nil
}

func (r *Registry) Register(ctx context.Context, in Registration) (Registration, error) {
	reg := in
	reg.Name = strings.TrimSpace(reg.Name)
	reg.URL = strings.TrimSpace(reg.URL)
	reg.Description = strings.TrimSpace(reg.Description)
	if err := ValidateName(reg.Name); err != nil {
		return Registration{}, err
	}
	if err := ValidateURL(reg.URL); err != nil {
		return Registration{}, err
	}
	if err := normalize(&reg); err != nil {
		return Registration{}, err
	}
	now := time.Now().UTC()
	reg.CreatedAt, reg.UpdatedAt = now, now
	res, err := r.db.ExecContext(ctx, r.rb(
		`INSERT INTO mcp_registrations(`+registrationColumns+`)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO NOTHING`),
		reg.Name, reg.URL, reg.Description, reg.CreatedBy, now, now, reg.ForwardsCredential,
		reg.Transport, reg.Auth, reg.CredentialRef, reg.HeaderTemplate)
	if err != nil {
		return Registration{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Registration{}, ErrRegistrationExists
	}
	return reg, nil
}

func (p Patch) Apply(current Registration) (Registration, error) {
	next := current
	if u := strings.TrimSpace(p.URL); u != "" {
		if err := ValidateURL(u); err != nil {
			return Registration{}, err
		}
		next.URL = u
	}
	if d := strings.TrimSpace(p.Description); d != "" {
		next.Description = d
	}
	if p.ForwardsCredential != nil {
		next.ForwardsCredential = *p.ForwardsCredential
	}
	if p.Transport != nil {
		next.Transport = *p.Transport
	}
	if p.Auth != nil {
		next.Auth = *p.Auth
	}
	if p.CredentialRef != nil {
		next.CredentialRef = *p.CredentialRef
	}
	if p.HeaderTemplate != nil {
		next.HeaderTemplate = *p.HeaderTemplate
	}
	if err := normalize(&next); err != nil {
		return Registration{}, err
	}
	return next, nil
}

func (r *Registry) Update(ctx context.Context, name string, p Patch) (Registration, error) {
	current, err := r.Get(ctx, strings.TrimSpace(name))
	if err != nil {
		return Registration{}, err
	}
	next, err := p.Apply(current)
	if err != nil {
		return Registration{}, err
	}
	next.UpdatedAt = time.Now().UTC()
	_, err = r.db.ExecContext(ctx, r.rb(
		`UPDATE mcp_registrations
		    SET url = ?, description = ?, updated_at = ?, forwards_credential = ?,
		        transport = ?, auth = ?, credential_ref = ?, header_template = ?
		  WHERE name = ?`),
		next.URL, next.Description, next.UpdatedAt, next.ForwardsCredential,
		next.Transport, next.Auth, next.CredentialRef, next.HeaderTemplate, next.Name)
	if err != nil {
		return Registration{}, err
	}
	return next, nil
}

func (r *Registry) Delete(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, r.rb(
		`DELETE FROM mcp_registrations WHERE name = ?`), strings.TrimSpace(name))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil
	}
	if n == 0 {
		return ErrRegistrationNotFound
	}
	return nil
}
