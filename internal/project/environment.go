package project

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Holds no secret: the cloud credential lives in the secrets store keyed by this ID and is resolved
// only server-side.
type Environment struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// Environments sharing (workspace, system, name) are one deployment seen from different repos;
	// empty means standalone.
	System    string            `json:"system,omitempty"`
	Provider  string            `json:"provider"`
	Region    string            `json:"region,omitempty"`
	Account   string            `json:"account,omitempty"`
	Selectors map[string]string `json:"selectors,omitempty"`
	// A flag rather than name == "prod": the naming convention is the user's.
	IsProduction bool   `json:"is_production"`
	URL          string `json:"url,omitempty"`

	HasCredential bool `json:"has_credential"`

	IngestEndpoint string `json:"ingest_endpoint,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EnvironmentInput struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	System       string            `json:"system,omitempty"`
	Provider     string            `json:"provider"`
	Region       string            `json:"region,omitempty"`
	Account      string            `json:"account,omitempty"`
	Selectors    map[string]string `json:"selectors,omitempty"`
	IsProduction bool              `json:"is_production"`
	URL          string            `json:"url,omitempty"`
}

var ErrEnvNotFound error = apperr.NotFound("project.errors.env_not_found")

var validProviders = map[string]bool{
	"aws": true, "gcp": true,
	"prometheus": true,
	"other":      true,
}

var envNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

const MaxEnvDisplayName = 60

func ValidateEnvironmentInput(in EnvironmentInput) (EnvironmentInput, error) {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.System = strings.ToLower(strings.TrimSpace(in.System))
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.Region = strings.TrimSpace(in.Region)
	in.Account = strings.TrimSpace(in.Account)
	in.URL = strings.TrimSpace(in.URL)

	if !envNameRe.MatchString(in.Name) {
		return in, apperr.Invalid("project.errors.env_name_invalid", "name", in.Name)
	}
	if utf8.RuneCountInString(in.DisplayName) > MaxEnvDisplayName {
		return in, apperr.Invalid("project.errors.env_display_name_too_long", "max", MaxEnvDisplayName)
	}
	if in.System != "" && !envNameRe.MatchString(in.System) {
		return in, apperr.Invalid("project.errors.env_system_invalid", "system", in.System)
	}
	if in.Provider == "" {
		in.Provider = "other"
	}
	if !validProviders[in.Provider] {
		return in, apperr.Invalid("project.errors.env_provider_invalid", "provider", in.Provider)
	}
	if in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return in, apperr.Invalid("project.errors.env_url_invalid")
	}
	for k := range in.Selectors {
		if strings.TrimSpace(k) == "" {
			return in, apperr.Invalid("project.errors.env_selector_key_blank")
		}
	}
	return in, nil
}

var envMigrations = []migrate.Migration{
	{ID: 9, SQL: `CREATE TABLE IF NOT EXISTS project_environments (
		id            TEXT PRIMARY KEY,
		project_id    TEXT NOT NULL,
		name          TEXT NOT NULL,
		provider      TEXT NOT NULL DEFAULT 'other',
		region        TEXT NOT NULL DEFAULT '',
		account       TEXT NOT NULL DEFAULT '',
		selectors     TEXT NOT NULL DEFAULT '{}',
		is_production  BOOLEAN NOT NULL DEFAULT FALSE,
		url           TEXT NOT NULL DEFAULT '',
		created_at    TIMESTAMP NOT NULL,
		updated_at    TIMESTAMP NOT NULL
	)`},
	{ID: 10, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_project_env_name
		ON project_environments(project_id, name)`},
	{ID: 11, SQL: `CREATE INDEX IF NOT EXISTS idx_project_env_project
		ON project_environments(project_id)`},
	{ID: 12, SQL: `ALTER TABLE project_environments
		ADD COLUMN IF NOT EXISTS system TEXT NOT NULL DEFAULT ''`},
	{ID: 13, SQL: `CREATE INDEX IF NOT EXISTS idx_project_env_system
		ON project_environments(system) WHERE system != ''`},
}

// Migrations appends this last: ID 25 comes after the prewarm set.
var envDisplayNameMigration = migrate.Migration{ID: 25, SQL: `ALTER TABLE project_environments
		ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''`}

const envCols = `id, project_id, name, system, provider, region, account, selectors,
	is_production, url, created_at, updated_at, display_name`

func (s *Store) ListEnvironments(ctx context.Context, projectID string) ([]Environment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+envCols+` FROM project_environments
		  WHERE project_id = ? ORDER BY is_production DESC, name ASC`), projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Environment{}
	for rows.Next() {
		e, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *Store) GetEnvironment(ctx context.Context, projectID, envID string) (*Environment, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+envCols+` FROM project_environments
		  WHERE id = ? AND project_id = ?`), envID, projectID)
	e, err := scanEnvironment(row)
	if err != nil {
		return nil, ErrEnvNotFound
	}
	return e, nil
}

// Unscoped lookup, only for internal credential resolution; caller-facing routes must use
// GetEnvironment.
func (s *Store) FindEnvironment(ctx context.Context, envID string) (*Environment, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+envCols+` FROM project_environments WHERE id = ?`), envID)
	e, err := scanEnvironment(row)
	if err != nil {
		return nil, ErrEnvNotFound
	}
	return e, nil
}

func (s *Store) CreateEnvironment(ctx context.Context, projectID string, in EnvironmentInput) (*Environment, error) {
	in, err := ValidateEnvironmentInput(in)
	if err != nil {
		return nil, err
	}
	sel, err := marshalSelectors(in.Selectors)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	e := &Environment{
		ID: generateID(), ProjectID: projectID, Name: in.Name, DisplayName: in.DisplayName,
		System: in.System, Provider: in.Provider, Region: in.Region, Account: in.Account,
		Selectors: in.Selectors, IsProduction: in.IsProduction, URL: in.URL,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO project_environments (`+envCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		e.ID, e.ProjectID, e.Name, e.System, e.Provider, e.Region, e.Account, sel,
		e.IsProduction, e.URL, e.CreatedAt, e.UpdatedAt, e.DisplayName)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Store) UpdateEnvironment(ctx context.Context, projectID, envID string, in EnvironmentInput) (*Environment, error) {
	if _, err := s.GetEnvironment(ctx, projectID, envID); err != nil {
		return nil, err
	}
	in, err := ValidateEnvironmentInput(in)
	if err != nil {
		return nil, err
	}
	sel, err := marshalSelectors(in.Selectors)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(
		`UPDATE project_environments
		    SET name=?, display_name=?, system=?, provider=?, region=?, account=?, selectors=?,
		        is_production=?, url=?, updated_at=?
		  WHERE id=? AND project_id=?`),
		in.Name, in.DisplayName, in.System, in.Provider, in.Region, in.Account, sel,
		in.IsProduction, in.URL, now, envID, projectID)
	if err != nil {
		return nil, err
	}
	return s.GetEnvironment(ctx, projectID, envID)
}

func (s *Store) DeleteEnvironment(ctx context.Context, projectID, envID string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM project_environments WHERE id = ? AND project_id = ?`), envID, projectID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrEnvNotFound
	}
	return nil
}

func (s *Store) DeleteEnvironmentsForProject(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM project_environments WHERE project_id = ?`), projectID)
	return err
}

func marshalSelectors(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.marshal_selectors")
	}
	return string(b), nil
}

type envScanner interface{ Scan(dest ...any) error }

func scanEnvironment(r envScanner) (*Environment, error) {
	var e Environment
	var sel string
	if err := r.Scan(&e.ID, &e.ProjectID, &e.Name, &e.System, &e.Provider, &e.Region,
		&e.Account, &sel, &e.IsProduction, &e.URL, &e.CreatedAt, &e.UpdatedAt, &e.DisplayName); err != nil {
		return nil, err
	}
	if sel != "" && sel != "{}" {
		if err := json.Unmarshal([]byte(sel), &e.Selectors); err != nil {
			// A corrupt selectors blob degrades to no selectors instead of failing the whole list.
			e.Selectors = nil
		}
	}
	return &e, nil
}
