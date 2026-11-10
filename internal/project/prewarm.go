package project

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	PrewarmRunning = "running"
	PrewarmOK      = "ok"
	PrewarmFailed  = "failed"
	PrewarmSkipped = "skipped"
	PrewarmPruned  = "pruned"
)

const (
	PrewarmReasonLoop   = "loop"
	PrewarmReasonSave   = "save"
	PrewarmReasonManual = "manual"
)

const (
	PrewarmBackoffBase    = 10 * time.Minute
	PrewarmBackoffMax     = 6 * time.Hour
	PrewarmStaleAfter     = 2 * time.Hour
	prewarmKeepPerProject = 20
	prewarmMaxErrorBytes  = 4096
)

type SetupPrewarm struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Computed by the daemon from git objects; backoff and freshness decisions use it.
	SetupKey string `json:"setup_key"`
	// Computed by the sandbox from the checkout and names the snapshot file; differs from SetupKey
	// when git and the checkout disagree on a key file.
	SnapshotKey string     `json:"snapshot_key,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Error       string     `json:"error,omitempty"`
}

// The partial unique index (23) is what keeps one running prewarm per project, even across daemon
// replicas.
var prewarmMigrations = []migrate.Migration{
	{ID: 22, SQL: `CREATE TABLE IF NOT EXISTS project_setup_prewarms (
		id            TEXT      PRIMARY KEY,
		project_id    TEXT      NOT NULL,
		workspace_id  TEXT      NOT NULL DEFAULT '',
		setup_key     TEXT      NOT NULL,
		snapshot_key  TEXT      NOT NULL DEFAULT '',
		commit_sha    TEXT      NOT NULL DEFAULT '',
		status        TEXT      NOT NULL,
		reason        TEXT      NOT NULL DEFAULT '',
		started_at    TIMESTAMP NOT NULL,
		finished_at   TIMESTAMP NULL,
		size_bytes    BIGINT    NOT NULL DEFAULT 0,
		error         TEXT      NOT NULL DEFAULT ''
	)`},
	{ID: 23, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_setup_prewarm_running
		ON project_setup_prewarms(project_id) WHERE status = 'running'`, Contract: &migrate.Contract{
		StoppedUsingIn: "0.3.2",
		Reason:         "project_setup_prewarms is new in 0.3.3 (migration 22): no released binary writes it", //i18n:ignore migration contract note
	}},
	{ID: 24, SQL: `CREATE INDEX IF NOT EXISTS idx_setup_prewarm_project
		ON project_setup_prewarms(project_id, started_at)`},
}

var ErrPrewarmRunning error = apperr.Conflict("project.errors.prewarm_running")

const prewarmColumns = `id, project_id, workspace_id, setup_key, snapshot_key, commit_sha,
	status, reason, started_at, finished_at, size_bytes, error`

func scanPrewarm(r rowScanner) (*SetupPrewarm, error) {
	p := &SetupPrewarm{}
	var finished sql.NullTime
	if err := r.Scan(&p.ID, &p.ProjectID, &p.WorkspaceID, &p.SetupKey, &p.SnapshotKey, &p.Commit,
		&p.Status, &p.Reason, &p.StartedAt, &finished, &p.SizeBytes, &p.Error); err != nil {
		return nil, err
	}
	if finished.Valid {
		t := finished.Time.UTC()
		p.FinishedAt = &t
	}
	p.StartedAt = p.StartedAt.UTC()
	return p, nil
}

func (s *Store) StartPrewarm(ctx context.Context, projectID, key, commit, reason string) (*SetupPrewarm, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	p := &SetupPrewarm{
		ID: generateID(), ProjectID: projectID, WorkspaceID: proj.WorkspaceID,
		SetupKey: key, Commit: commit, Status: PrewarmRunning, Reason: reason,
		StartedAt: time.Now().UTC(),
	}
	_, err = s.db.ExecContext(ctx, s.rb(`INSERT INTO project_setup_prewarms
		(id, project_id, workspace_id, setup_key, commit_sha, status, reason, started_at)
		VALUES (?,?,?,?,?,?,?,?)`),
		p.ID, p.ProjectID, p.WorkspaceID, p.SetupKey, p.Commit, p.Status, p.Reason, p.StartedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrPrewarmRunning
		}
		return nil, apperr.Wrap(err, "project.errors.prewarm_save_failed", "id", projectID)
	}
	return p, nil
}

func isUniqueViolation(err error) bool {
	msg := err.Error()
	//i18n:ignore Postgres error text matched, not shown
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}

type PrewarmOutcome struct {
	Status      string
	SnapshotKey string
	SizeBytes   int64
	Error       string
	Pruned      []string
}

func (s *Store) FinishPrewarm(ctx context.Context, id string, out PrewarmOutcome) error {
	if len(out.Error) > prewarmMaxErrorBytes {
		out.Error = out.Error[len(out.Error)-prewarmMaxErrorBytes:]
	}
	var projectID string
	err := s.db.QueryRowContext(ctx, s.rb(`UPDATE project_setup_prewarms
		   SET status = ?, snapshot_key = ?, size_bytes = ?, error = ?, finished_at = ?
		 WHERE id = ? RETURNING project_id`),
		out.Status, out.SnapshotKey, out.SizeBytes, out.Error, time.Now().UTC(), id).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("project.errors.prewarm_not_found", "id", id)
	}
	if err != nil {
		return apperr.Wrap(err, "project.errors.prewarm_save_failed", "id", id)
	}
	for _, key := range out.Pruned {
		if _, pruneErr := s.db.ExecContext(ctx, s.rb(`UPDATE project_setup_prewarms SET status = ?
			 WHERE project_id = ? AND snapshot_key = ? AND status = ?`),
			PrewarmPruned, projectID, key, PrewarmOK); pruneErr != nil {
			return apperr.Wrap(pruneErr, "project.errors.prewarm_save_failed", "id", id)
		}
	}
	_, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM project_setup_prewarms
		 WHERE project_id = ? AND status <> ? AND id NOT IN (
		   SELECT id FROM project_setup_prewarms WHERE project_id = ?
		    ORDER BY started_at DESC LIMIT ?)`),
		projectID, PrewarmRunning, projectID, prewarmKeepPerProject)
	if err != nil {
		return apperr.Wrap(err, "project.errors.prewarm_save_failed", "id", id)
	}
	return nil
}

func (s *Store) ListPrewarms(ctx context.Context, projectID string, limit int) ([]SetupPrewarm, error) {
	if limit <= 0 || limit > prewarmKeepPerProject {
		limit = prewarmKeepPerProject
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+prewarmColumns+`
		  FROM project_setup_prewarms WHERE project_id = ?
		 ORDER BY started_at DESC LIMIT ?`), projectID, limit)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.prewarm_load_failed")
	}
	defer rows.Close()
	out := []SetupPrewarm{}
	for rows.Next() {
		p, err := scanPrewarm(rows)
		if err != nil {
			return nil, apperr.Wrap(err, "project.errors.prewarm_load_failed")
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) FailStalePrewarms(ctx context.Context, cutoff time.Time, keep map[string]bool, reason string) (int, error) {
	ids, err := s.stalePrewarmIDs(ctx, cutoff, keep)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.FinishPrewarm(ctx, id, PrewarmOutcome{Status: PrewarmFailed, Error: reason}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Store) stalePrewarmIDs(ctx context.Context, cutoff time.Time, keep map[string]bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM project_setup_prewarms
		 WHERE status = ? AND started_at < ?`), PrewarmRunning, cutoff.UTC())
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.prewarm_load_failed")
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Wrap(err, "project.errors.prewarm_load_failed")
		}
		if !keep[id] {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "project.errors.prewarm_load_failed")
	}
	return ids, nil
}

func (s *Store) DeletePrewarms(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM project_setup_prewarms WHERE project_id = ?`), projectID)
	return err
}

const (
	PrewarmDueYes     = "due"
	PrewarmDueNoSetup = "not_eligible"
	PrewarmDueFresh   = "fresh"
	PrewarmDueRunning = "running"
	PrewarmDueBackoff = "backoff"
	PrewarmDueNoKey   = "no_key"
)

// manual skips the backoff and reruns a succeeded key: the snapshot may have gone with the cache
// volume.
func PrewarmDue(m InitManifest, key string, history []SetupPrewarm, now time.Time, manual bool) (bool, string) {
	switch {
	case !m.HasSetupSnapshot():
		return false, PrewarmDueNoSetup
	case key == "":
		return false, PrewarmDueNoKey
	case prewarmRunning(history, now):
		return false, PrewarmDueRunning
	case manual:
		return true, PrewarmDueYes
	case prewarmSucceeded(history, key):
		return false, PrewarmDueFresh
	}
	if n, last := prewarmFailures(history, key); n > 0 && now.Before(last.Add(PrewarmBackoff(n))) {
		return false, PrewarmDueBackoff
	}
	return true, PrewarmDueYes
}

func prewarmRunning(history []SetupPrewarm, now time.Time) bool {
	for i := range history {
		if history[i].Status == PrewarmRunning && now.Sub(history[i].StartedAt) < PrewarmStaleAfter {
			return true
		}
	}
	return false
}

func prewarmSucceeded(history []SetupPrewarm, key string) bool {
	for i := range history {
		if history[i].SetupKey == key && history[i].Status == PrewarmOK {
			return true
		}
	}
	return false
}

func prewarmFailures(history []SetupPrewarm, key string) (int, time.Time) {
	n := 0
	var last time.Time
	for i := range history {
		p := &history[i]
		if p.SetupKey != key || p.Status == PrewarmSkipped || p.Status == PrewarmRunning {
			continue
		}
		if p.Status != PrewarmFailed {
			break
		}
		if n == 0 {
			last = p.StartedAt
			if p.FinishedAt != nil {
				last = *p.FinishedAt
			}
		}
		n++
	}
	return n, last
}

func PrewarmBackoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := PrewarmBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= PrewarmBackoffMax {
			return PrewarmBackoffMax
		}
	}
	return d
}
