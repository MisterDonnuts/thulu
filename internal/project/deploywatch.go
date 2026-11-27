package project

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// A deploy watch tells Thulu where a project's production deploys show up, so each new one opens a
// release task without anyone asking. The release agent registers it; the daemon turns it into an
// automation it owns (automation_id). One per project, source and target.
var deployWatchMigration = migrate.Migration{ID: 33, SQL: `
	CREATE TABLE IF NOT EXISTS project_deploy_watches (
		id               TEXT PRIMARY KEY,
		workspace_id     TEXT NOT NULL DEFAULT '',
		project_id       TEXT NOT NULL,
		source           TEXT NOT NULL,
		target           TEXT NOT NULL,
		interval_seconds INTEGER NOT NULL DEFAULT 0,
		board_id         TEXT NOT NULL DEFAULT '',
		type_id          TEXT NOT NULL DEFAULT '',
		automation_id    TEXT NOT NULL DEFAULT '',
		created_by       TEXT NOT NULL DEFAULT '',
		created_by_task  TEXT NOT NULL DEFAULT '',
		last_state       TEXT NOT NULL DEFAULT '',
		last_version     TEXT NOT NULL DEFAULT '',
		last_error       TEXT NOT NULL DEFAULT '',
		last_checked_at  TIMESTAMP,
		last_changed_at  TIMESTAMP,
		created_at       TIMESTAMP NOT NULL,
		updated_at       TIMESTAMP NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS uq_project_deploy_watch
		ON project_deploy_watches(project_id, source, target);
	CREATE INDEX IF NOT EXISTS idx_project_deploy_watch_ws ON project_deploy_watches(workspace_id)`}

// The closed menu of sources. GitHub ones ride webhooks; cloud and http are polled.
const (
	WatchGitHubWorkflow   = "github_workflow"
	WatchGitHubDeployment = "github_deployment"
	WatchCloud            = "cloud"
	WatchHTTP             = "http"
)

// Polling bounds: a shorter interval hammers the customer's endpoint, a longer one finds a deploy a
// day late.
const (
	MinWatchInterval     = 5 * time.Minute
	MaxWatchInterval     = 24 * time.Hour
	DefaultWatchInterval = 15 * time.Minute
)

// Observed states of a polled source.
const (
	WatchStateSuccess = "success"
	WatchStateFailure = "failure"
)

var ErrDeployWatchNotFound error = apperr.NotFound("project.errors.deploy_watch_not_found")

type DeployWatch struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	// Seconds between polls; 0 for the webhook sources.
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// Where the release tasks land: the board and task type of the run that registered the watch.
	BoardID string `json:"board_id"`
	TypeID  string `json:"type_id,omitempty"`
	// The automation the daemon generated for this watch and owns.
	AutomationID string `json:"automation_id,omitempty"`
	// The agent and task that registered it, or the person who did.
	CreatedBy     string `json:"created_by,omitempty"`
	CreatedByTask string `json:"created_by_task,omitempty"`
	// What the last poll saw (polled sources only).
	LastState     string     `json:"last_state,omitempty"`
	LastVersion   string     `json:"last_version,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastChangedAt *time.Time `json:"last_changed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (w *DeployWatch) Interval() time.Duration { return time.Duration(w.IntervalSeconds) * time.Second }

// Polled reports whether the daemon reads the source on a schedule instead of waiting for a webhook.
func (w *DeployWatch) Polled() bool { return PolledWatchSource(w.Source) }

func PolledWatchSource(source string) bool { return source == WatchCloud || source == WatchHTTP }

type DeployWatchInput struct {
	Source   string
	Target   string
	Interval time.Duration
}

var (
	// A workflow's name or file. Quotes, braces and backslashes stay out: the target lands inside a
	// recipe filter and a task title template.
	watchWorkflowRe = regexp.MustCompile(`^[A-Za-z0-9.][A-Za-z0-9 ._/()-]{0,99}$`)
	watchEnvIDRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

const maxWatchURL = 2048

// ValidateDeployWatch normalizes a watch request against the closed menu. A workflow file given by
// name ("deploy.yml") becomes its path, as GitHub reports it in workflow_run.path.
func ValidateDeployWatch(in DeployWatchInput) (DeployWatchInput, error) {
	in.Source = strings.TrimSpace(in.Source)
	target, err := validateWatchTarget(in.Source, strings.TrimSpace(in.Target))
	if err != nil {
		return in, err
	}
	in.Target = target
	return validateWatchInterval(in)
}

func validateWatchTarget(source, target string) (string, error) {
	switch source {
	case WatchGitHubWorkflow:
		return validateWatchWorkflow(target)
	case WatchGitHubDeployment:
		if !releaseEnvRe.MatchString(target) {
			return target, apperr.Invalid("project.errors.deploy_watch_environment_invalid", "target", target)
		}
	case WatchCloud:
		if !watchEnvIDRe.MatchString(target) {
			return target, apperr.Invalid("project.errors.deploy_watch_cloud_target", "target", target)
		}
	case WatchHTTP:
		return target, validateWatchURL(target)
	default:
		return target, apperr.Invalid("project.errors.deploy_watch_source_invalid", "source", source,
			"known", strings.Join([]string{WatchGitHubWorkflow, WatchGitHubDeployment, WatchCloud, WatchHTTP}, ", "))
	}
	return target, nil
}

func validateWatchWorkflow(target string) (string, error) {
	if !watchWorkflowRe.MatchString(target) {
		return target, apperr.Invalid("project.errors.deploy_watch_workflow_invalid", "target", target)
	}
	if isWorkflowFile(target) && !strings.Contains(target, "/") {
		target = workflowsDir + target
	}
	if strings.Contains(target, "/") && (!strings.HasPrefix(target, workflowsDir) || !isWorkflowFile(target)) {
		return target, apperr.Invalid("project.errors.deploy_watch_workflow_path", "target", target, "dir", workflowsDir)
	}
	return target, nil
}

func validateWatchInterval(in DeployWatchInput) (DeployWatchInput, error) {
	if !PolledWatchSource(in.Source) {
		if in.Interval != 0 {
			return in, apperr.Invalid("project.errors.deploy_watch_interval_webhook", "source", in.Source)
		}
		return in, nil
	}
	if in.Interval == 0 {
		in.Interval = DefaultWatchInterval
	}
	if in.Interval < MinWatchInterval || in.Interval > MaxWatchInterval || in.Interval%time.Minute != 0 {
		return in, apperr.Invalid("project.errors.deploy_watch_interval_range",
			"interval", in.Interval, "min", MinWatchInterval, "max", MaxWatchInterval)
	}
	return in, nil
}

func isWorkflowFile(s string) bool {
	return strings.HasSuffix(s, ".yml") || strings.HasSuffix(s, ".yaml")
}

func validateWatchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > maxWatchURL ||
		strings.ContainsAny(raw, " {}\"\\") {
		return apperr.Invalid("project.errors.deploy_watch_url_invalid", "target", raw)
	}
	if u.User != nil {
		// A password in the URL would sit in the watch row, the recipe and the page.
		return apperr.Invalid("project.errors.deploy_watch_url_credentials")
	}
	return nil
}

const deployWatchCols = `id, workspace_id, project_id, source, target, interval_seconds, board_id, type_id,
	automation_id, created_by, created_by_task, last_state, last_version, last_error,
	last_checked_at, last_changed_at, created_at, updated_at`

// UpsertDeployWatch stores w, or updates the watch of the same project, source and target. On an
// update the interval, board, type and author change; the observation and the automation stay.
// It returns the stored row and whether it was new.
func (s *Store) UpsertDeployWatch(ctx context.Context, w *DeployWatch) (*DeployWatch, bool, error) {
	now := time.Now().UTC()
	var id string
	var inserted bool
	err := s.db.QueryRowContext(ctx, s.rb(`
		INSERT INTO project_deploy_watches (id, workspace_id, project_id, source, target, interval_seconds,
		    board_id, type_id, created_by, created_by_task, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (project_id, source, target) DO UPDATE SET
		    interval_seconds = EXCLUDED.interval_seconds,
		    board_id = EXCLUDED.board_id,
		    type_id = EXCLUDED.type_id,
		    created_by = EXCLUDED.created_by,
		    created_by_task = EXCLUDED.created_by_task,
		    updated_at = EXCLUDED.updated_at
		RETURNING id, (xmax = 0)`),
		generateID(), w.WorkspaceID, w.ProjectID, w.Source, w.Target, w.IntervalSeconds,
		w.BoardID, w.TypeID, w.CreatedBy, w.CreatedByTask, now, now).Scan(&id, &inserted)
	if err != nil {
		return nil, false, err
	}
	got, err := s.GetDeployWatch(ctx, id)
	return got, inserted, err
}

// SwapDeployWatchAutomation points the watch at next only if it still points at prev, and returns
// the automation the watch points at afterwards: next when the swap went through, or the one another
// writer set first. Two sets of a new watch both create an automation; only one may own the watch.
func (s *Store) SwapDeployWatchAutomation(ctx context.Context, id, prev, next string) (string, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE project_deploy_watches SET automation_id = ? WHERE id = ? AND automation_id = ?`), next, id, prev)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return next, nil
	}
	cur, err := s.GetDeployWatch(ctx, id)
	if err != nil {
		return "", err
	}
	return cur.AutomationID, nil
}

func (s *Store) GetDeployWatch(ctx context.Context, id string) (*DeployWatch, error) {
	row := s.db.QueryRowContext(ctx, s.rb(`SELECT `+deployWatchCols+` FROM project_deploy_watches WHERE id = ?`), id)
	w, err := scanDeployWatch(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeployWatchNotFound
	}
	return w, err
}

func (s *Store) ListDeployWatches(ctx context.Context, projectID string) ([]DeployWatch, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+deployWatchCols+` FROM project_deploy_watches
		WHERE project_id = ? ORDER BY created_at, id`), projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeployWatch{}
	for rows.Next() {
		w, err := scanDeployWatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDeployWatch(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM project_deploy_watches WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeployWatchNotFound
	}
	return nil
}

func (s *Store) deleteDeployWatchesForProject(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM project_deploy_watches WHERE project_id = ?`), projectID)
	return err
}

// DeployObservation is one poll of a watched source.
type DeployObservation struct {
	State   string
	Version string
	Error   string
	At      time.Time
}

// RecordDeployObservation stores what a poll saw. A failed read (Error set) keeps the last state and
// version, so a blip does not count as a change twice.
func (s *Store) RecordDeployObservation(ctx context.Context, id string, obs DeployObservation, changed bool) error {
	if obs.Error != "" {
		_, err := s.db.ExecContext(ctx, s.rb(`UPDATE project_deploy_watches
			SET last_error = ?, last_checked_at = ? WHERE id = ?`), obs.Error, obs.At, id)
		return err
	}
	q := `UPDATE project_deploy_watches
		SET last_state = ?, last_version = ?, last_error = '', last_checked_at = ? WHERE id = ?`
	args := []any{obs.State, obs.Version, obs.At, id}
	if changed {
		q = `UPDATE project_deploy_watches
			SET last_state = ?, last_version = ?, last_error = '', last_checked_at = ?, last_changed_at = ? WHERE id = ?`
		args = []any{obs.State, obs.Version, obs.At, obs.At, id}
	}
	_, err := s.db.ExecContext(ctx, s.rb(q), args...)
	return err
}

func scanDeployWatch(scan func(dest ...any) error) (*DeployWatch, error) {
	var w DeployWatch
	var checked, changed sql.NullTime
	if err := scan(&w.ID, &w.WorkspaceID, &w.ProjectID, &w.Source, &w.Target, &w.IntervalSeconds,
		&w.BoardID, &w.TypeID, &w.AutomationID, &w.CreatedBy, &w.CreatedByTask,
		&w.LastState, &w.LastVersion, &w.LastError, &checked, &changed, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	if checked.Valid {
		t := checked.Time.UTC()
		w.LastCheckedAt = &t
	}
	if changed.Valid {
		t := changed.Time.UTC()
		w.LastChangedAt = &t
	}
	w.CreatedAt, w.UpdatedAt = w.CreatedAt.UTC(), w.UpdatedAt.UTC()
	return &w, nil
}
