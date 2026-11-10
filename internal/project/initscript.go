package project

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	InitScriptMaxBytes        = 64 << 10
	InitDefaultTimeoutSeconds = 300
	InitMaxTimeoutSeconds     = 900

	initMaxParams       = 32
	initMaxPorts        = 16
	initMaxParamName    = 64
	initMaxValueBytes   = 4096
	initMaxReadyBytes   = 4096
	initMaxWorkdirBytes = 256
	initMaxNoteRunes    = 500
	initMaxLabelRunes   = 200

	initMaxTools        = 16
	initMaxToolBytes    = 64
	initMaxCacheEntries = 32
	initMaxCachePath    = 256

	initMaxFiles         = 32
	initMaxFileBytes     = 64 << 10
	initMaxFilesTotal    = 256 << 10
	initMaxFilePathBytes = 256
)

const (
	InitAuthorUser  = "user"
	InitAuthorAgent = "agent"
)

type InitParam struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type InitReady struct {
	Command        string `json:"command,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type InitCache struct {
	Key   []string `json:"key"`
	Paths []string `json:"paths"`
}

type InitManifest struct {
	Params  []InitParam       `json:"params"`
	Ports   []int             `json:"ports"`
	Ready   InitReady         `json:"ready"`
	Workdir string            `json:"workdir"`
	Setup   string            `json:"setup,omitempty"`
	Tools   map[string]string `json:"tools,omitempty"`
	Cache   InitCache         `json:"cache"`
	Files   map[string]string `json:"files,omitempty"`
}

func (m InitManifest) HasSetupSnapshot() bool {
	return strings.TrimSpace(m.Setup) != "" && len(m.Cache.Paths) > 0
}

// Taken from the caller's credential, never from a request body.
type InitAuthor struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
}

type InitScriptInput struct {
	Script       string       `json:"script"`
	Manifest     InitManifest `json:"manifest"`
	Note         string       `json:"note,omitempty"`
	RestoredFrom int          `json:"-"`
}

type InitScript struct {
	ProjectID    string       `json:"project_id"`
	Revision     int          `json:"revision"`
	Script       string       `json:"script"`
	Manifest     InitManifest `json:"manifest"`
	AuthorKind   string       `json:"author_kind"`
	AuthorID     string       `json:"author_id,omitempty"`
	AuthorLabel  string       `json:"author_label,omitempty"`
	Note         string       `json:"note,omitempty"`
	RestoredFrom int          `json:"restored_from,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

type InitScriptSummary struct {
	Revision     int       `json:"revision"`
	AuthorKind   string    `json:"author_kind"`
	AuthorID     string    `json:"author_id,omitempty"`
	AuthorLabel  string    `json:"author_label,omitempty"`
	Note         string    `json:"note,omitempty"`
	RestoredFrom int       `json:"restored_from,omitempty"`
	Size         int       `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
}

var initScriptMigrations = []migrate.Migration{
	{ID: 21, SQL: `CREATE TABLE IF NOT EXISTS project_init_script_revisions (
		project_id    TEXT      NOT NULL,
		revision      INTEGER   NOT NULL,
		workspace_id  TEXT      NOT NULL DEFAULT '',
		script        TEXT      NOT NULL,
		manifest      TEXT      NOT NULL DEFAULT '{}',
		author_kind   TEXT      NOT NULL,
		author_id     TEXT      NOT NULL DEFAULT '',
		author_label  TEXT      NOT NULL DEFAULT '',
		note          TEXT      NOT NULL DEFAULT '',
		restored_from INTEGER   NOT NULL DEFAULT 0,
		created_at    TIMESTAMP NOT NULL,
		PRIMARY KEY (project_id, revision)
	)`},
}

var ErrNoInitScript error = apperr.NotFound("project.errors.init_script_not_found")

var (
	initParamNameRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	initToolNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@+-]*$`)
	initToolVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]*$`)
)

func DefaultInitManifest() InitManifest {
	return InitManifest{
		Params:  []InitParam{},
		Ports:   []int{},
		Ready:   InitReady{TimeoutSeconds: InitDefaultTimeoutSeconds},
		Workdir: ".",
		Cache:   InitCache{Key: []string{}, Paths: []string{}},
	}
}

// Older revisions lack newer fields; normalize so every manifest marshals the same (sameInitContent
// compares JSON).
func (m *InitManifest) normalize() {
	if m.Params == nil {
		m.Params = []InitParam{}
	}
	if m.Ports == nil {
		m.Ports = []int{}
	}
	if m.Cache.Key == nil {
		m.Cache.Key = []string{}
	}
	if m.Cache.Paths == nil {
		m.Cache.Paths = []string{}
	}
	if len(m.Tools) == 0 {
		m.Tools = nil
	}
	if len(m.Files) == 0 {
		m.Files = nil
	}
}

func ValidateInitScript(in InitScriptInput) (InitScriptInput, error) {
	if strings.TrimSpace(in.Script) == "" {
		return in, apperr.Invalid("project.errors.init_script_empty")
	}
	if len(in.Script) > InitScriptMaxBytes {
		return in, apperr.TooLarge("project.errors.init_script_too_large", "max", InitScriptMaxBytes)
	}
	if strings.ContainsRune(in.Script, 0) {
		return in, apperr.Invalid("project.errors.init_script_nul")
	}
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > initMaxNoteRunes {
		return in, apperr.Invalid("project.errors.init_note_too_long", "max", initMaxNoteRunes)
	}
	m, err := validateInitManifest(in.Manifest)
	if err != nil {
		return in, err
	}
	in.Manifest = m
	return in, nil
}

func validateInitManifest(m InitManifest) (InitManifest, error) {
	params, err := validateInitParams(m.Params)
	if err != nil {
		return m, err
	}
	m.Params = params

	if len(m.Ports) > initMaxPorts {
		return m, apperr.Invalid("project.errors.init_too_many_ports", "max", initMaxPorts)
	}
	ports := make([]int, 0, len(m.Ports))
	seenPort := map[int]bool{}
	for _, p := range m.Ports {
		if p < 1 || p > 65535 {
			return m, apperr.Invalid("project.errors.init_port_range", "port", p)
		}
		if !seenPort[p] {
			seenPort[p] = true
			ports = append(ports, p)
		}
	}
	m.Ports = ports

	m.Ready.Command = strings.TrimSpace(m.Ready.Command)
	if len(m.Ready.Command) > initMaxReadyBytes || strings.ContainsRune(m.Ready.Command, 0) {
		return m, apperr.Invalid("project.errors.init_ready_invalid", "max", initMaxReadyBytes)
	}
	if m.Ready.TimeoutSeconds == 0 {
		m.Ready.TimeoutSeconds = InitDefaultTimeoutSeconds
	}
	if m.Ready.TimeoutSeconds < 1 || m.Ready.TimeoutSeconds > InitMaxTimeoutSeconds {
		return m, apperr.Invalid("project.errors.init_timeout_range", "max", InitMaxTimeoutSeconds)
	}

	wd, err := cleanInitWorkdir(m.Workdir)
	if err != nil {
		return m, err
	}
	m.Workdir = wd

	out, err := validateInitInstall(m)
	if err != nil {
		return m, err
	}
	out.normalize()
	return out, nil
}

func validateInitInstall(m InitManifest) (InitManifest, error) {
	if strings.TrimSpace(m.Setup) == "" {
		m.Setup = ""
	}
	if len(m.Setup) > InitScriptMaxBytes {
		return m, apperr.TooLarge("project.errors.init_setup_too_large", "max", InitScriptMaxBytes)
	}
	if strings.ContainsRune(m.Setup, 0) {
		return m, apperr.Invalid("project.errors.init_setup_nul")
	}
	tools, err := validateInitTools(m.Tools)
	if err != nil {
		return m, err
	}
	m.Tools = tools
	if m.Cache.Key, err = cleanCachePaths(m.Cache.Key, false); err != nil {
		return m, err
	}
	if m.Cache.Paths, err = cleanCachePaths(m.Cache.Paths, true); err != nil {
		return m, err
	}
	if m.Files, err = validateInitFiles(m.Files); err != nil {
		return m, err
	}
	return m, nil
}

// Names and versions become mise arguments in the sandbox, hence the tight alphabet.
func validateInitTools(in map[string]string) (map[string]string, error) {
	if len(in) > initMaxTools {
		return nil, apperr.Invalid("project.errors.init_too_many_tools", "max", initMaxTools)
	}
	out := make(map[string]string, len(in))
	for rawName, rawVersion := range in {
		name, version := strings.TrimSpace(rawName), strings.TrimSpace(rawVersion)
		if len(name) > initMaxToolBytes || !initToolNameRe.MatchString(name) {
			return nil, apperr.Invalid("project.errors.init_tool_name", "name", name)
		}
		if len(version) > initMaxToolBytes || !initToolVersionRe.MatchString(version) {
			return nil, apperr.Invalid("project.errors.init_tool_version", "name", name, "version", version)
		}
		if _, dup := out[name]; dup {
			return nil, apperr.Invalid("project.errors.init_tool_duplicate", "name", name)
		}
		out[name] = version
	}
	return out, nil
}

// A path becomes a file under $OCTOPUS_INIT_FILES and a line of the setup key, so it must already
// be clean: what the editor shows is what lands in the sandbox.
func validateInitFiles(in map[string]string) (map[string]string, error) {
	if len(in) > initMaxFiles {
		return nil, apperr.Invalid("project.errors.init_too_many_files", "max", initMaxFiles)
	}
	out := make(map[string]string, len(in))
	total := 0
	for p, content := range in {
		if len(p) > initMaxFilePathBytes || !validCachePath(p, true) || path.Clean(p) != p {
			return nil, apperr.Invalid("project.errors.init_file_path", "path", p)
		}
		if len(content) > initMaxFileBytes {
			return nil, apperr.TooLarge("project.errors.init_file_too_large", "path", p, "max", initMaxFileBytes)
		}
		if strings.ContainsRune(content, 0) {
			return nil, apperr.Invalid("project.errors.init_file_nul", "path", p)
		}
		total += len(content)
		out[p] = content
	}
	if total > initMaxFilesTotal {
		return nil, apperr.TooLarge("project.errors.init_files_too_large", "max", initMaxFilesTotal)
	}
	for p := range out {
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if _, clash := out[dir]; clash {
				return nil, apperr.Invalid("project.errors.init_file_path", "path", p)
			}
		}
	}
	return out, nil
}

// No control characters: each path is one line of the setup key's text. dirs also refuses the
// workdir and .git, which a snapshot must never hold.
func cleanCachePaths(in []string, dirs bool) ([]string, error) {
	if len(in) > initMaxCacheEntries {
		return nil, apperr.Invalid("project.errors.init_too_many_cache_paths", "max", initMaxCacheEntries)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if !validCachePath(p, dirs) {
			return nil, apperr.Invalid("project.errors.init_cache_path", "path", raw)
		}
		clean := path.Clean(p)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out, nil
}

func validCachePath(p string, dir bool) bool {
	if p == "" || len(p) > initMaxCachePath || strings.HasPrefix(p, "/") {
		return false
	}
	if strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	clean := path.Clean(p)
	switch {
	case clean == "." || clean == ".." || strings.HasPrefix(clean, "../"):
		return false
	case dir && (clean == ".git" || strings.HasPrefix(clean, ".git/")):
		return false
	}
	return true
}

func validateInitParams(in []InitParam) ([]InitParam, error) {
	if len(in) > initMaxParams {
		return nil, apperr.Invalid("project.errors.init_too_many_params", "max", initMaxParams)
	}
	out := make([]InitParam, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p.Name = strings.TrimSpace(p.Name)
		p.Description = strings.TrimSpace(p.Description)
		if len(p.Name) > initMaxParamName || !initParamNameRe.MatchString(p.Name) {
			return nil, apperr.Invalid("project.errors.init_param_name", "name", p.Name)
		}
		// The runner exports OCTOPUS_* itself and would silently overwrite such a param.
		if strings.HasPrefix(p.Name, "OCTOPUS_") {
			return nil, apperr.Invalid("project.errors.init_param_reserved", "name", p.Name)
		}
		if seen[p.Name] {
			return nil, apperr.Invalid("project.errors.init_param_duplicate", "name", p.Name)
		}
		seen[p.Name] = true
		if len(p.Default) > initMaxValueBytes || len(p.Description) > initMaxValueBytes ||
			strings.ContainsRune(p.Default, 0) {
			return nil, apperr.Invalid("project.errors.init_param_value", "name", p.Name, "max", initMaxValueBytes)
		}
		out = append(out, p)
	}
	return out, nil
}

func cleanInitWorkdir(wd string) (string, error) {
	wd = strings.TrimSpace(wd)
	if wd == "" {
		return ".", nil
	}
	if len(wd) > initMaxWorkdirBytes || strings.ContainsRune(wd, 0) || strings.HasPrefix(wd, "/") {
		return "", apperr.Invalid("project.errors.init_workdir_invalid", "workdir", wd)
	}
	clean := path.Clean(wd)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", apperr.Invalid("project.errors.init_workdir_invalid", "workdir", wd)
	}
	return clean, nil
}

func validateInitAuthor(a InitAuthor) (InitAuthor, error) {
	if a.Kind != InitAuthorUser && a.Kind != InitAuthorAgent {
		return a, apperr.Invalid("project.errors.init_author_kind", "kind", a.Kind)
	}
	if r := []rune(a.Label); len(r) > initMaxLabelRunes {
		a.Label = string(r[:initMaxLabelRunes])
	}
	return a, nil
}

func (s *Store) SaveInitScript(ctx context.Context, projectID string, in InitScriptInput, author InitAuthor) (*InitScript, error) {
	in, err := ValidateInitScript(in)
	if err != nil {
		return nil, err
	}
	if author, err = validateInitAuthor(author); err != nil {
		return nil, err
	}
	manifest, err := json.Marshal(in.Manifest)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_marshal_manifest")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_save_failed", "id", projectID)
	}
	defer func() { _ = tx.Rollback() }()

	var workspaceID string
	err = tx.QueryRowContext(ctx, s.rb(`SELECT workspace_id FROM projects WHERE id = ? FOR UPDATE`), projectID).
		Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("project.errors.not_found", "id", projectID)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_save_failed", "id", projectID)
	}
	var last int
	if err := tx.QueryRowContext(ctx, s.rb(
		`SELECT COALESCE(MAX(revision), 0) FROM project_init_script_revisions WHERE project_id = ?`),
		projectID).Scan(&last); err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_save_failed", "id", projectID)
	}
	out := &InitScript{
		ProjectID:    projectID,
		Revision:     last + 1,
		Script:       in.Script,
		Manifest:     in.Manifest,
		AuthorKind:   author.Kind,
		AuthorID:     author.ID,
		AuthorLabel:  author.Label,
		Note:         in.Note,
		RestoredFrom: in.RestoredFrom,
		CreatedAt:    time.Now().UTC(),
	}
	if err := insertInitRevision(ctx, s, tx, out, workspaceID, string(manifest)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_save_failed", "id", projectID)
	}
	return out, nil
}

func insertInitRevision(ctx context.Context, s *Store, tx *sql.Tx, r *InitScript, workspaceID, manifest string) error {
	_, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO project_init_script_revisions
		   (project_id, revision, workspace_id, script, manifest, author_kind,
		    author_id, author_label, note, restored_from, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
		r.ProjectID, r.Revision, workspaceID, r.Script, manifest, r.AuthorKind,
		r.AuthorID, r.AuthorLabel, r.Note, r.RestoredFrom, r.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "project.errors.init_save_failed", "id", r.ProjectID)
	}
	return nil
}

const initRevisionColumns = `project_id, revision, script, manifest, author_kind,
	author_id, author_label, note, restored_from, created_at`

func scanInitScript(r rowScanner) (*InitScript, error) {
	out := &InitScript{}
	var manifest string
	if err := r.Scan(&out.ProjectID, &out.Revision, &out.Script, &manifest, &out.AuthorKind,
		&out.AuthorID, &out.AuthorLabel, &out.Note, &out.RestoredFrom, &out.CreatedAt); err != nil {
		return nil, err
	}
	out.Manifest = DefaultInitManifest()
	if err := json.Unmarshal([]byte(manifest), &out.Manifest); err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_unmarshal_manifest", "id", out.ProjectID)
	}
	out.Manifest.normalize()
	return out, nil
}

func (s *Store) InitScript(ctx context.Context, projectID string) (*InitScript, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`SELECT `+initRevisionColumns+`
		  FROM project_init_script_revisions WHERE project_id = ?
		 ORDER BY revision DESC LIMIT 1`), projectID)
	return initScriptOrNotFound(scanInitScript(row))
}

func (s *Store) InitScriptRevision(ctx context.Context, projectID string, revision int) (*InitScript, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`SELECT `+initRevisionColumns+`
		  FROM project_init_script_revisions WHERE project_id = ? AND revision = ?`), projectID, revision)
	return initScriptOrNotFound(scanInitScript(row))
}

func initScriptOrNotFound(sc *InitScript, err error) (*InitScript, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoInitScript
	}
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_load_failed")
	}
	return sc, nil
}

func (s *Store) ListInitScriptRevisions(ctx context.Context, projectID string, limit int) ([]InitScriptSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT revision, author_kind, author_id, author_label,
		       note, restored_from, LENGTH(script), created_at
		  FROM project_init_script_revisions WHERE project_id = ?
		 ORDER BY revision DESC LIMIT ?`), projectID, limit)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.init_load_failed")
	}
	defer rows.Close()
	out := []InitScriptSummary{}
	for rows.Next() {
		var r InitScriptSummary
		if err := rows.Scan(&r.Revision, &r.AuthorKind, &r.AuthorID, &r.AuthorLabel,
			&r.Note, &r.RestoredFrom, &r.Size, &r.CreatedAt); err != nil {
			return nil, apperr.Wrap(err, "project.errors.init_load_failed")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RestoreInitScript(ctx context.Context, projectID string, revision int, author InitAuthor, note string) (*InitScript, error) {
	old, err := s.InitScriptRevision(ctx, projectID, revision)
	if err != nil {
		return nil, err
	}
	return s.SaveInitScript(ctx, projectID, InitScriptInput{
		Script: old.Script, Manifest: old.Manifest, Note: note, RestoredFrom: old.Revision,
	}, author)
}

func (s *Store) DeleteInitScripts(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM project_init_script_revisions WHERE project_id = ?`), projectID)
	return err
}

func (s *Store) ImportInitScript(ctx context.Context, sc InitScript) (bool, error) {
	if sc.ProjectID == "" {
		return false, apperr.Invalid("project.errors.init_import_project_required")
	}
	in, err := ValidateInitScript(InitScriptInput{Script: sc.Script, Manifest: sc.Manifest, Note: sc.Note})
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.init_import_failed", "id", sc.ProjectID)
	}
	cur, err := s.InitScript(ctx, sc.ProjectID)
	switch {
	case errors.Is(err, ErrNoInitScript):
	case err != nil:
		return false, err
	case sameInitContent(cur, in):
		return false, nil
	}
	author, err := validateInitAuthor(InitAuthor{Kind: sc.AuthorKind, ID: sc.AuthorID, Label: sc.AuthorLabel})
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.init_import_failed", "id", sc.ProjectID)
	}
	saved, err := s.SaveInitScript(ctx, sc.ProjectID, in, author)
	if err != nil {
		return false, err
	}
	if !sc.CreatedAt.IsZero() {
		if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE project_init_script_revisions
			   SET created_at = ? WHERE project_id = ? AND revision = ?`),
			sc.CreatedAt.UTC(), saved.ProjectID, saved.Revision); err != nil {
			return true, apperr.Wrap(err, "project.errors.init_import_failed", "id", sc.ProjectID)
		}
	}
	return true, nil
}

func sameInitContent(a *InitScript, b InitScriptInput) bool {
	if a.Script != b.Script {
		return false
	}
	am, aErr := json.Marshal(a.Manifest)
	bm, bErr := json.Marshal(b.Manifest)
	return aErr == nil && bErr == nil && bytes.Equal(am, bm)
}
