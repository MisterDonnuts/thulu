package project

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/patch"
	"github.com/MisterDonnuts/thulu/internal/syncx"
)

type Project struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Path                 string            `json:"path"`
	RepoURL              string            `json:"repo_url"`
	DefaultBranch        string            `json:"default_branch"`
	Description          string            `json:"description,omitempty"`
	StageTemplate        map[string]string `json:"stage_template,omitempty"`
	DefaultBoardID       string            `json:"default_board_id,omitempty"`
	WorkspaceID          string            `json:"workspace_id,omitempty"`
	GithubInstallationID int64             `json:"github_installation_id,omitempty"`
	Source               string            `json:"source"`
	CloneStatus          string            `json:"clone_status,omitempty"`
	CloneError           string            `json:"clone_error,omitempty"`
	// Git's output with the token and the daemon's paths stripped: it is served to API callers.
	CloneDetail string    `json:"clone_detail,omitempty"`
	RunnerID    string    `json:"runner_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Empty means the clone is on disk, which every project created before background clones has.
const (
	CloneCloning = "cloning"
	CloneFailed  = "failed"
)

func (p *Project) ready() error {
	if p.CodeOnRunner() {
		return apperr.Precondition("project.errors.on_runner", "name", p.Name)
	}
	switch p.CloneStatus {
	case CloneCloning:
		return apperr.Conflict("project.errors.clone_pending", "name", p.Name)
	case CloneFailed:
		return apperr.Precondition("project.errors.clone_failed", "name", p.Name, "reason", p.CloneError)
	}
	if _, err := os.Stat(p.Path); errors.Is(err, fs.ErrNotExist) {
		return apperr.Precondition("project.errors.clone_missing", "name", p.Name)
	}
	return nil
}

func (p *Project) checkout() error {
	if err := p.ready(); err != nil {
		return err
	}
	if p.Path == "" {
		return apperr.Precondition("project.errors.clone_missing", "name", p.Name)
	}
	// Check HEAD, not the directory: a .git holding only what the daemon writes (info/exclude) is
	// no repository either.
	if _, err := os.Stat(filepath.Join(p.Path, ".git", "HEAD")); err != nil {
		return apperr.Precondition("project.errors.clone_missing", "name", p.Name)
	}
	return nil
}

const (
	SourceGit    = "git"
	SourceUpload = "upload"
	SourceRunner = "runner"
)

func (p *Project) Hosted() bool { return p.Source == SourceUpload }

// GitHubToken is used only by the initial clone and not persisted here; the API layer stores it in
// the secrets DB after Create returns.
type CreateOpts struct {
	DefaultBranch        string
	Description          string
	StageTemplate        map[string]string
	DefaultBoardID       string
	GitHubToken          string
	WorkspaceID          string
	GithubInstallationID int64
}

type Patch struct {
	Name           *string            `json:"name,omitempty"`
	DefaultBranch  *string            `json:"default_branch,omitempty"`
	Description    *string            `json:"description,omitempty"`
	StageTemplate  *map[string]string `json:"stage_template,omitempty"`
	DefaultBoardID *string            `json:"default_board_id,omitempty"`
	// Not authorized here: the API layer must check the caller may use the target installation.
	GithubInstallationID *int64 `json:"github_installation_id,omitempty"`
	// Not authorized here: the API layer must check the runner belongs to the workspace and is not
	// revoked.
	RunnerID *string `json:"runner_id,omitempty"`
}

type Store struct {
	db          *sql.DB
	baseDir     string
	hostedLocks syncx.KeyedMutex
	clones      cloneRegistry
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Migration ID 4 is retired; never reuse it.
func NewStore(db *sql.DB, baseDir string) (*Store, error) {
	ctx := context.Background()
	migrations, err := Migrations(migrate.DBInspector(ctx, db))
	if err == nil {
		err = migrate.Run(ctx, db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.schema")
	}

	s := &Store{db: db, baseDir: baseDir}
	s.ensureReservedDirs()
	if baseDir != "" {
		s.clearIncoming()
	}
	return s, nil
}

const (
	worktreesDirName    = ".worktrees"
	projectStageDirName = ".project-stage"
	prewarmDirName      = ".prewarm"
)

// Second wall behind checkProjectDirName: with these dirs present, a name that slipped past finds
// them taken and is renamed instead of cloned into them.
func (s *Store) ensureReservedDirs() {
	if s.baseDir == "" {
		return
	}
	for _, d := range []string{worktreesDirName, projectStageDirName, prewarmDirName, hostedDirName} {
		p := filepath.Join(s.baseDir, d)
		logx.Warn(os.MkdirAll(p, 0o750), "project.log.reserved_dir", "path", p)
	}
}

func checkProjectDirName(name, dir string) error {
	if strings.HasPrefix(dir, ".") {
		return apperr.Invalid("project.errors.name_reserved", "name", name)
	}
	return nil
}

// Never change: databases that ran these migrations under another name would run them all again.
const MigrationComponent = "project"

// A published migration never changes.
func Migrations(in migrate.Inspector) ([]migrate.Migration, error) {
	base := []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS projects (
			id              TEXT PRIMARY KEY,
			name            TEXT NOT NULL,
			path            TEXT NOT NULL,
			repo_url        TEXT NOT NULL,
			default_branch  TEXT NOT NULL DEFAULT 'main',
			description     TEXT NOT NULL DEFAULT '',
			ai_review_cmd   TEXT NOT NULL DEFAULT '',
			testing_cmd     TEXT NOT NULL DEFAULT '',
			stage_template  TEXT NOT NULL DEFAULT '{}',
			created_at      TIMESTAMP NOT NULL,
			workspace_id    TEXT NOT NULL DEFAULT ''
		)`},
		{ID: 2, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT ''`},
		{ID: 3, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`},
		{ID: 5, SQL: `CREATE INDEX IF NOT EXISTS idx_projects_workspace ON projects(workspace_id)`},
		{ID: 6, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_workspace_name ON projects(workspace_id, name)`},
		{ID: 7, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS github_installation_id BIGINT NOT NULL DEFAULT 0`},
		{ID: 8, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS default_board_id TEXT NOT NULL DEFAULT ''`},
	}
	sourceMigration := migrate.Migration{ID: 26, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'git'`}
	// IDs 9–13 are reserved for envMigrations.
	migrations := make([]migrate.Migration, 0, len(base)+len(envMigrations)+3+len(initScriptMigrations)+len(prewarmMigrations)+len(cloneMigrations))
	migrations = append(migrations, base...)
	migrations = append(migrations, envMigrations...)

	// Guarded on tasks.workspace_id: the project store migrates first on a fresh database. Projects
	// with no tasks keep workspace '' rather than a guessed tenant.
	adoptable, adoptErr := in.ColumnExists("tasks", "workspace_id")
	if adoptErr != nil {
		return nil, adoptErr
	}
	if adoptable {
		migrations = append(migrations, migrate.Migration{ID: 20, SQL: `
			UPDATE projects SET workspace_id = COALESCE((
			    SELECT t.workspace_id
			      FROM task_projects tp
			      JOIN tasks t ON t.id = tp.task_id
			     WHERE tp.project_id = projects.id AND t.workspace_id <> ''
			     LIMIT 1), '')
			 WHERE workspace_id = ''`})
	}
	// 21+ come after the optional 20 so the list stays in ID order either way.
	migrations = append(migrations, initScriptMigrations...)
	migrations = append(migrations, prewarmMigrations...)
	// 25 is envDisplayNameMigration, 26 source, 27 and 28 cloneMigrations.
	migrations = append(migrations, envDisplayNameMigration, sourceMigration)
	migrations = append(migrations, cloneMigrations...)
	migrations = append(migrations, runnerMigration)
	return migrations, nil
}

func (s *Store) DB() *sql.DB { return s.db }

// In copy mode this is the shared data volume's mount point: paths made relative to it are the
// volume subpaths mounted into agent containers.
func (s *Store) DataRoot() string { return filepath.Dir(s.baseDir) }

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sanitizeDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "project"
	}
	return out
}

// Generous on purpose: it only stops a stalled connection from hanging the request.
const landingWriteTimeout = 30 * time.Second

func createInput(ctx context.Context, name, repoURL string, opts CreateOpts) (string, error) {
	if strings.TrimSpace(repoURL) == "" {
		return "", apperr.Invalid("project.errors.repo_url_required")
	}
	if strings.TrimSpace(name) == "" {
		return "", apperr.Invalid("project.errors.name_required")
	}
	// Workspace '' is the unstamped row hidden from every tenant; tasks inherit their
	// workspace from the project, and the per-workspace name index relies on it.
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return "", apperr.Invalid("project.errors.workspace_required")
	}
	// repo_url and default_branch are tenant input that become git arguments; the URL must pass the
	// remote policy, including the DNS check against loopback and link-local hosts.
	if opts.GitHubToken != "" {
		repoURL = githubSSHToHTTPS(repoURL)
	} else {
		repoURL = NormalizeRepoURL(repoURL)
	}
	if err := ValidateRepoURLResolved(ctx, repoURL); err != nil {
		return "", err
	}
	if opts.DefaultBranch != "" {
		if err := ValidateBranchName(opts.DefaultBranch); err != nil {
			return "", err
		}
	}
	return repoURL, nil
}

func (s *Store) Create(ctx context.Context, name, repoURL string, opts CreateOpts) (*Project, error) {
	proj, err := s.prepareCreate(ctx, name, repoURL, opts)
	if err != nil {
		return nil, err
	}
	// The token goes through -c url.insteadOf so it never lands in the on-disk git config, and is
	// redacted from errors.
	if err := gitCloneAuth(ctx, opts.GitHubToken, proj.RepoURL, proj.Path, opts.DefaultBranch); err != nil {
		logx.Warn(os.RemoveAll(proj.Path), "project.log.cleanup_clone_dir", "path", proj.Path)
		return nil, err
	}
	finishClone(proj, opts.DefaultBranch)

	if err := s.insertProject(ctx, proj, proj.templateJSON()); err != nil {
		logx.Warn(os.RemoveAll(proj.Path), "project.log.cleanup_clone_dir", "path", proj.Path)
		return nil, err
	}
	return proj, nil
}

func (s *Store) prepareCreate(ctx context.Context, name, repoURL string, opts CreateOpts) (*Project, error) {
	repoURL, inputErr := createInput(ctx, name, repoURL, opts)
	if inputErr != nil {
		return nil, inputErr
	}

	var exists int
	if err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND name = ?`),
		opts.WorkspaceID, name,
	).Scan(&exists); err == nil && exists > 0 {
		return nil, apperr.Conflict("project.errors.name_taken", "name", name)
	}

	id := generateID()
	projPath, err := s.claimProjectDir(name, id)
	if err != nil {
		return nil, err
	}
	proj := &Project{
		ID:                   id,
		Name:                 name,
		Path:                 projPath,
		RepoURL:              repoURL,
		DefaultBranch:        opts.DefaultBranch,
		Description:          opts.Description,
		StageTemplate:        opts.StageTemplate,
		DefaultBoardID:       opts.DefaultBoardID,
		WorkspaceID:          opts.WorkspaceID,
		GithubInstallationID: opts.GithubInstallationID,
		Source:               SourceGit,
		CreatedAt:            time.Now().UTC(),
	}
	if proj.StageTemplate == nil {
		proj.StageTemplate = map[string]string{}
	}
	return proj, nil
}

func finishClone(proj *Project, defaultBranch string) {
	proj.DefaultBranch = defaultBranch
	if proj.DefaultBranch == "" {
		proj.DefaultBranch = detectDefaultBranch(proj.Path)
	}
	logx.Warn(gitRun(proj.Path, "config", "user.email", commitEmail),
		"project.log.git_config", "setting", "user.email", "path", proj.Path)
	logx.Warn(gitRun(proj.Path, "config", "user.name", commitName),
		"project.log.git_config", "setting", "user.name", "path", proj.Path)
}

func (p *Project) templateJSON() string {
	raw, err := json.Marshal(p.StageTemplate)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// The checkout is already on disk, so the row must land even if the caller hung up: a row-less
// clone is an orphan nothing reclaims.
func (s *Store) insertProject(ctx context.Context, proj *Project, tplJSON string) error {
	insertCtx, cancelInsert := context.WithTimeout(context.WithoutCancel(ctx), landingWriteTimeout)
	defer cancelInsert()
	if _, err := s.db.ExecContext(insertCtx,
		s.rb(`INSERT INTO projects (id, name, path, repo_url, default_branch,
		    description, stage_template, default_board_id, workspace_id,
		    github_installation_id, source, clone_status, clone_error, runner_id, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		proj.ID, proj.Name, proj.Path, proj.RepoURL, proj.DefaultBranch,
		proj.Description, tplJSON, proj.DefaultBoardID, proj.WorkspaceID,
		proj.GithubInstallationID, proj.Source, proj.CloneStatus, proj.CloneError, proj.RunnerID, proj.CreatedAt,
	); err != nil {
		return apperr.Wrap(err, "project.errors.insert_failed")
	}
	return nil
}

func (s *Store) Update(ctx context.Context, id string, p Patch) (*Project, error) {
	if p.DefaultBranch != nil {
		if err := ValidateBranchName(*p.DefaultBranch); err != nil {
			return nil, err
		}
	}
	proj, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if proj.Hosted() && p.DefaultBranch != nil && *p.DefaultBranch != proj.DefaultBranch {
		// An upload project's default must be a hosted branch, fetched into the clone before a
		// worktree branches from origin/<default>.
		if _, err = revParse(s.hostedRepoDir(id), "refs/heads/"+*p.DefaultBranch); err != nil {
			return nil, apperr.Invalid("project.errors.archive_branch_missing", "branch", *p.DefaultBranch)
		}
		if ferr := fetchHostedBranch(proj, *p.DefaultBranch); ferr != nil {
			return nil, ferr
		}
	}
	patch.Set(&proj.Name, p.Name)
	patch.Set(&proj.DefaultBranch, p.DefaultBranch)
	patch.Set(&proj.Description, p.Description)
	patch.Set(&proj.StageTemplate, p.StageTemplate)
	patch.Set(&proj.DefaultBoardID, p.DefaultBoardID)
	patch.Set(&proj.GithubInstallationID, p.GithubInstallationID)
	if p.RunnerID != nil {
		runnerID := strings.TrimSpace(*p.RunnerID)
		if rerr := proj.checkRunnerPatch(runnerID); rerr != nil {
			return nil, rerr
		}
		proj.RunnerID = runnerID
	}
	tplJSON, err := json.Marshal(proj.StageTemplate)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.marshal_template")
	}
	_, err = s.db.ExecContext(ctx,
		s.rb(`UPDATE projects SET name=?, default_branch=?, description=?,
		    stage_template=?, default_board_id=?, github_installation_id=?, runner_id=? WHERE id=?`),
		proj.Name, proj.DefaultBranch, proj.Description,
		string(tplJSON), proj.DefaultBoardID, proj.GithubInstallationID, proj.RunnerID, proj.ID,
	)
	return proj, err
}

// ai_review_cmd and testing_cmd are left out on purpose: they stay in the schema only for
// MigrateLegacyStageCmds to drain.
const projectColumns = `id, name, path, repo_url, default_branch, description,
	        stage_template, default_board_id, workspace_id, github_installation_id, source,
	        clone_status, clone_error, clone_detail, runner_id, created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(r rowScanner) (*Project, error) {
	p := &Project{}
	var tplJSON string
	if err := r.Scan(&p.ID, &p.Name, &p.Path, &p.RepoURL, &p.DefaultBranch,
		&p.Description, &tplJSON, &p.DefaultBoardID, &p.WorkspaceID,
		&p.GithubInstallationID, &p.Source, &p.CloneStatus, &p.CloneError, &p.CloneDetail, &p.RunnerID, &p.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tplJSON), &p.StageTemplate); err != nil {
		return nil, apperr.Wrap(err, "project.errors.unmarshal_template_for", "id", p.ID)
	}
	if p.StageTemplate == nil {
		p.StageTemplate = map[string]string{}
	}
	return p, nil
}

func (s *Store) List(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		s.rb(`SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC`),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListByWorkspace(ctx context.Context, workspaceID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		s.rb(`SELECT `+projectColumns+` FROM projects WHERE workspace_id = ? ORDER BY created_at DESC`),
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (*Project, error) {
	row := s.db.QueryRowContext(ctx,
		s.rb(`SELECT `+projectColumns+` FROM projects WHERE id = ?`), id,
	)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("project.errors.not_found", "id", id)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.load_failed", "id", id)
	}
	return p, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	proj, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	// Stop a running clone first, or it keeps writing into the directory removed below.
	s.clones.stop(id)
	if proj.CodeOnRunner() {
		return s.deleteRunnerProject(ctx, proj)
	}
	worktreesDir := filepath.Join(s.baseDir, worktreesDirName)
	base := filepath.Base(proj.Path)
	if entries, readErr := os.ReadDir(worktreesDir); readErr == nil {
		for _, e := range entries {
			if worktreeOf(e.Name()) == base {
				wt := filepath.Join(worktreesDir, e.Name())
				logx.Warn(os.RemoveAll(wt), "project.log.remove_worktree", "path", wt)
			}
		}
	}
	logx.Warn(gitRun(proj.Path, "worktree", "prune"), "project.log.worktree_prune", "path", proj.Path)
	logx.Warn(os.RemoveAll(proj.Path), "project.log.remove_clone", "path", proj.Path)
	if proj.Hosted() {
		hosted := s.hostedRepoDir(proj.ID)
		logx.Warn(os.RemoveAll(hosted), "project.log.remove_hosted_repo", "path", hosted)
	}
	stageDir := s.StageDir(proj)
	logx.Warn(os.RemoveAll(stageDir), "project.log.remove_stage_dir", "path", stageDir)
	// A project later created under this id (an import) must not inherit this one's init script or
	// prewarm history.
	initCtx, cancelInit := context.WithTimeout(context.WithoutCancel(ctx), landingWriteTimeout)
	defer cancelInit()
	logx.Warn(s.DeleteInitScripts(initCtx, id), "project.log.delete_init_scripts", "id", id)
	logx.Warn(s.DeletePrewarms(initCtx, id), "project.log.delete_prewarms", "id", id)
	// The clone and worktrees are already gone, so the row must go even if the caller left.
	delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(ctx), landingWriteTimeout)
	defer cancelDel()
	_, err = s.db.ExecContext(delCtx, s.rb(`DELETE FROM projects WHERE id = ?`), id)
	return err
}

// The project dir is tenant input that may contain "--", so split at the last "--task-": a prefix
// match let deleting "app" remove another workspace's "app--x" worktrees.
func worktreeOf(entry string) string {
	i := strings.LastIndex(entry, "--"+taskBranchName(""))
	if i <= 0 {
		return ""
	}
	return entry[:i]
}

// Kept outside the clone so it is never in the git tree; mounted in agent containers at
// /workspace/projects/<name>/.thulu/, where project-stage.sh reads prompts/$STAGE.md and
// hooks/$STAGE.sh.
func (s *Store) StageDir(proj *Project) string {
	return filepath.Join(s.baseDir, projectStageDirName, filepath.Base(proj.Path))
}

func (s *Store) EnsureStageDirs(proj *Project) (string, error) {
	root := s.StageDir(proj)
	for _, sub := range []string{"prompts", "hooks"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o750); err != nil {
			return "", err
		}
	}
	return root, nil
}
