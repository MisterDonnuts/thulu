package sandbox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	SetupRunTask    = "task"
	SetupRunPrewarm = "prewarm"
)

const (
	ToolsCached      = "cached"
	ToolsDownloaded  = "downloaded"
	ToolsUnavailable = "unavailable"
	ToolsFailed      = "failed"
)

const (
	setupRunMaxError     = 1000
	setupLogWriteTimeout = 10 * time.Second
)

type SetupRun struct {
	ID              string
	RunKey          string
	WorkspaceID     string
	ProjectID       string
	TaskID          string
	Kind            string
	SetupKey        string
	Status          string
	ToolsStatus     string
	ToolsDownloaded []string
	ToolsUsed       []string
	ToolsMs         int64
	SetupMs         int64
	RestoreMs       int64
	TotalMs         int64
	NoCache         bool
	Error           string
	CreatedAt       time.Time
}

var setupRunsMigration = migrate.Migration{ID: 3, SQL: `
	CREATE TABLE IF NOT EXISTS sandbox_setup_runs (
		id               TEXT      PRIMARY KEY,
		run_key          TEXT      NOT NULL,
		workspace_id     TEXT      NOT NULL DEFAULT '',
		project_id       TEXT      NOT NULL,
		task_id          TEXT      NULL,
		kind             TEXT      NOT NULL,
		setup_key        TEXT      NOT NULL DEFAULT '',
		status           TEXT      NOT NULL,
		tools_status     TEXT      NOT NULL DEFAULT '',
		tools_downloaded TEXT      NOT NULL DEFAULT '',
		tools_used       TEXT      NOT NULL DEFAULT '',
		tools_ms         BIGINT    NOT NULL DEFAULT 0,
		setup_ms         BIGINT    NOT NULL DEFAULT 0,
		restore_ms       BIGINT    NOT NULL DEFAULT 0,
		total_ms         BIGINT    NOT NULL DEFAULT 0,
		no_cache         BOOLEAN   NOT NULL DEFAULT FALSE,
		error            TEXT      NOT NULL DEFAULT '',
		created_at       TIMESTAMP NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS uq_setup_runs_key ON sandbox_setup_runs(run_key);
	CREATE INDEX IF NOT EXISTS idx_setup_runs_created ON sandbox_setup_runs(created_at);
	CREATE INDEX IF NOT EXISTS idx_setup_runs_project ON sandbox_setup_runs(project_id, created_at);`}

func newRowID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ToolsStatusOf(state string, downloaded []string) string {
	switch state {
	case "":
		return ""
	case "installed":
		if len(downloaded) > 0 {
			return ToolsDownloaded
		}
		return ToolsCached
	}
	return state
}

func (s *Store) RecordSetupRun(ctx context.Context, r SetupRun) (bool, error) {
	if r.ID == "" {
		r.ID = newRowID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if len(r.Error) > setupRunMaxError {
		r.Error = r.Error[len(r.Error)-setupRunMaxError:]
	}
	task := sql.NullString{String: r.TaskID, Valid: r.TaskID != ""}
	res, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO sandbox_setup_runs
		(id, run_key, workspace_id, project_id, task_id, kind, setup_key, status, tools_status,
		 tools_downloaded, tools_used, tools_ms, setup_ms, restore_ms, total_ms, no_cache, error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (run_key) DO NOTHING`),
		r.ID, r.RunKey, r.WorkspaceID, r.ProjectID, task, r.Kind, r.SetupKey, r.Status, r.ToolsStatus,
		strings.Join(r.ToolsDownloaded, ","), strings.Join(r.ToolsUsed, ","),
		r.ToolsMs, r.SetupMs, r.RestoreMs, r.TotalMs, r.NoCache, r.Error, r.CreatedAt.UTC())
	if err != nil {
		return false, apperr.Wrap(err, "sandbox.errors.setup_log_save")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, apperr.Wrap(err, "sandbox.errors.setup_log_save")
	}
	return n > 0, nil
}

//i18n:ignore SQL
const setupRunColumns = `id, run_key, workspace_id, project_id, COALESCE(task_id, ''), kind, setup_key, status,
	tools_status, tools_downloaded, tools_used, tools_ms, setup_ms, restore_ms, total_ms, no_cache, error, created_at`

func (s *Store) setupRunsSince(ctx context.Context, since time.Time) ([]SetupRun, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+setupRunColumns+`
		  FROM sandbox_setup_runs WHERE created_at >= ? ORDER BY created_at, id`), since.UTC())
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.setup_log_load")
	}
	defer rows.Close()
	var out []SetupRun
	for rows.Next() {
		var r SetupRun
		var downloaded, used string
		if err := rows.Scan(&r.ID, &r.RunKey, &r.WorkspaceID, &r.ProjectID, &r.TaskID, &r.Kind, &r.SetupKey,
			&r.Status, &r.ToolsStatus, &downloaded, &used, &r.ToolsMs, &r.SetupMs, &r.RestoreMs, &r.TotalMs,
			&r.NoCache, &r.Error, &r.CreatedAt); err != nil {
			return nil, apperr.Wrap(err, "sandbox.errors.setup_log_load")
		}
		r.ToolsDownloaded = splitToolList(downloaded)
		r.ToolsUsed = splitToolList(used)
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.setup_log_load")
	}
	return out, nil
}

func (s *Store) oldestSetupRun(ctx context.Context) (*time.Time, error) {
	var t sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM sandbox_setup_runs`).Scan(&t); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.setup_log_load")
	}
	if !t.Valid {
		return nil, nil
	}
	v := t.Time.UTC()
	return &v, nil
}

func (s *Store) PruneCacheMetricsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	runs, err := dbpkg.DeleteBatched(ctx, s.db, "sandbox_setup_runs", `created_at < ?`, cutoff.UTC())
	if err != nil {
		return runs, err
	}
	usage, err := dbpkg.DeleteBatched(ctx, s.db, "sandbox_cache_usage", `measured_at < ?`, cutoff.UTC())
	return runs + usage, err
}

// setup.result writes the exit code right after the failed status: wait for it (or the run's end)
// so the row carries it.
func setupSettled(r *InitRun) bool {
	if r.Setup == nil {
		return false
	}
	switch r.Setup.Status {
	case SetupStateSkipped, SetupStateRestored, SetupStateRan, SetupStateNone:
		return true
	case SetupStateFailed:
		return r.Setup.ExitCode != nil || r.Setup.Tools == ToolsFailed || r.State != InitStateRunning
	case SetupStateRunning:
		return r.State != InitStateRunning
	}
	return false
}

// The key must survive a daemon restart: the run id comes from the sandbox's setup.result.
// Scripts that predate the id fall back to the start second.
func initRunKey(owner, dir string, run InitRun) string {
	id := strconv.FormatInt(run.StartedAt.Unix(), 10)
	if run.Setup != nil && run.Setup.RunID != "" {
		id = run.Setup.RunID
	}
	return SetupRunTask + ":" + owner + "/" + dir + "/" + run.container + "/" + id
}

func taskSetupRun(t *initTarget, run InitRun) SetupRun {
	rep := run.Setup
	r := SetupRun{
		RunKey:          initRunKey(t.owner, t.proj.Dir, run),
		WorkspaceID:     t.opts.WorkspaceID,
		ProjectID:       t.proj.ID,
		TaskID:          t.taskID,
		Kind:            SetupRunTask,
		SetupKey:        rep.Key,
		Status:          rep.Status,
		ToolsStatus:     ToolsStatusOf(rep.Tools, rep.ToolsDownloaded),
		ToolsDownloaded: rep.ToolsDownloaded,
		ToolsUsed:       rep.ToolsUsed,
		ToolsMs:         rep.ToolsMs,
		SetupMs:         rep.SetupMs,
		RestoreMs:       rep.RestoreMs,
		TotalMs:         rep.ToolsMs + rep.RestoreMs + rep.SetupMs,
		NoCache:         rep.NoCache,
	}
	switch rep.Status {
	case SetupStateRunning:
		r.Status = SetupStateFailed
		r.Error = i18n.Text("sandbox.setup_log.interrupted", "state", run.State)
	case SetupStateFailed:
		r.Error = setupFailedMessage(rep)
	}
	return r
}

// setupLogged is claimed under the registry lock so init_run's wait loop and init_status polls log
// once;
// the unique run_key covers restarts and other replicas.
func (m *Manager) noteInitRun(ctx context.Context, t *initTarget, run InitRun) {
	if m == nil || m.store == nil {
		return
	}
	var logIt, measure bool
	cur, ok := m.inits.update(t.owner, t.proj.Dir, run.gen, func(r *InitRun) {
		if !r.setupLogged && setupSettled(r) {
			r.setupLogged, logIt = true, true
		}
		if r.setupLogged && !r.measured && r.State != InitStateRunning {
			r.measured, measure = true, true
		}
	})
	if !ok {
		return
	}
	if logIt {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), setupLogWriteTimeout)
		_, err := m.store.RecordSetupRun(wctx, taskSetupRun(t, cur))
		cancel()
		logx.Warn(err, "sandbox.log.setup_log_failed", "task", t.owner, "project", t.proj.ID)
	}
	if measure && cur.Setup != nil && !cur.Setup.NoCache {
		m.measureAfterInit(ctx, t.opts.WorkspaceID, cur.container)
	}
}
