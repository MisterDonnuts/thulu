package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Recorded in schema_migrations. Never change it.
const MigrationComponent = "runner"

// A published migration never changes; add a new ID instead.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS runners (
				id            TEXT PRIMARY KEY,
				workspace_id  TEXT NOT NULL,
				name          TEXT NOT NULL,
				token_hash    TEXT NOT NULL UNIQUE,
				hostname      TEXT NOT NULL DEFAULT '',
				version       TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMPTZ NOT NULL,
				created_by    TEXT NOT NULL DEFAULT '',
				last_seen_at  TIMESTAMPTZ,
				revoked_at    TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_runners_ws ON runners(workspace_id);
			CREATE TABLE IF NOT EXISTS runner_registrations (
				token_hash    TEXT PRIMARY KEY,
				workspace_id  TEXT NOT NULL,
				name          TEXT NOT NULL,
				created_by    TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMPTZ NOT NULL,
				expires_at    TIMESTAMPTZ NOT NULL,
				used_at       TIMESTAMPTZ
			);
			CREATE TABLE IF NOT EXISTS runner_jobs (
				id            TEXT PRIMARY KEY,
				runner_id     TEXT NOT NULL,
				workspace_id  TEXT NOT NULL,
				task_id       TEXT NOT NULL DEFAULT '',
				project_id    TEXT NOT NULL DEFAULT '',
				instance_id   TEXT NOT NULL,
				agent_name    TEXT NOT NULL DEFAULT '',
				agent_type    TEXT NOT NULL DEFAULT '',
				engine        TEXT NOT NULL DEFAULT '',
				stage         TEXT NOT NULL DEFAULT '',
				launch_kind   TEXT NOT NULL DEFAULT '',
				spec          TEXT NOT NULL DEFAULT '{}',
				state         TEXT NOT NULL,
				cancel        BOOLEAN NOT NULL DEFAULT FALSE,
				lease_until   TIMESTAMPTZ,
				exit_code     INTEGER,
				result        TEXT NOT NULL DEFAULT '{}',
				created_at    TIMESTAMPTZ NOT NULL,
				claimed_at    TIMESTAMPTZ,
				finished_at   TIMESTAMPTZ,
				reaped        BOOLEAN NOT NULL DEFAULT FALSE
			);
			CREATE INDEX IF NOT EXISTS idx_runner_jobs_claim ON runner_jobs(runner_id, state, created_at);
			CREATE INDEX IF NOT EXISTS idx_runner_jobs_settle ON runner_jobs(state, reaped);`},
	}, nil
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.init_schema")
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

const RegistrationTTL = time.Hour

func (s *Store) CreateRegistration(ctx context.Context, ws, name, by string) (token string, expires time.Time, err error) {
	name = strings.TrimSpace(name)
	if ws == "" {
		return "", time.Time{}, apperr.Invalid("runner.errors.workspace_required")
	}
	if name == "" || len(name) > 80 {
		return "", time.Time{}, apperr.Invalid("runner.errors.name_invalid")
	}
	token = newSecret(RegistrationPrefix)
	now := s.now()
	expires = now.Add(RegistrationTTL)
	_, err = s.db.ExecContext(ctx, `INSERT INTO runner_registrations
		(token_hash, workspace_id, name, created_by, created_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		hashSecret(token), ws, name, by, now, expires)
	if err != nil {
		return "", time.Time{}, apperr.Wrap(err, "runner.errors.create_registration")
	}
	return token, expires, nil
}

// The token is spent in the same transaction, so two runners racing on one token get one
// registration.
func (s *Store) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if !strings.HasPrefix(req.Token, RegistrationPrefix) {
		return nil, apperr.Unauthenticated("runner.errors.registration_invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.register")
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	var ws, name, by string
	err = tx.QueryRowContext(ctx, `UPDATE runner_registrations SET used_at = $1
		WHERE token_hash = $2 AND used_at IS NULL AND expires_at > $1
		RETURNING workspace_id, name, created_by`, now, hashSecret(req.Token)).Scan(&ws, &name, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Unauthenticated("runner.errors.registration_invalid")
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.register")
	}
	cred := newSecret(CredentialPrefix)
	id := newID()
	_, err = tx.ExecContext(ctx, `INSERT INTO runners
		(id, workspace_id, name, token_hash, hostname, version, created_at, created_by, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $7)`,
		id, ws, name, hashSecret(cred), clip(req.Hostname, 200), clip(req.Version, 64), now, by)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.register")
	}
	if err := tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.register")
	}
	return &RegisterResponse{RunnerID: id, WorkspaceID: ws, Name: name, Credential: cred}, nil
}

// Must stay well under 2*DefaultLease, the window Online is computed from.
const seenEvery = 10 * time.Second

func (s *Store) Authenticate(ctx context.Context, credential, version string) (*Runner, error) {
	if !strings.HasPrefix(credential, CredentialPrefix) {
		return nil, apperr.Unauthenticated("runner.errors.credential_invalid")
	}
	r, err := s.scanRunner(s.db.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners
		WHERE token_hash = $1 AND revoked_at IS NULL`, hashSecret(credential)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Unauthenticated("runner.errors.credential_invalid")
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.authenticate")
	}
	now := s.now()
	if r.LastSeenAt == nil || now.Sub(*r.LastSeenAt) > seenEvery || (version != "" && version != r.Version) {
		_, err := s.db.ExecContext(ctx, `UPDATE runners SET last_seen_at = $1,
			version = CASE WHEN $2 = '' THEN version ELSE $2 END WHERE id = $3`, now, clip(version, 64), r.ID)
		logx.Warn(err, "runner.log.seen_failed", "runner", r.ID)
		r.LastSeenAt = &now
		if version != "" {
			r.Version = version
		}
	}
	r.Online = true
	return r, nil
}

const runnerCols = `id, workspace_id, name, hostname, version, created_at, created_by, last_seen_at, revoked_at`

func (s *Store) scanRunner(row interface{ Scan(...any) error }) (*Runner, error) {
	var r Runner
	var seen, revoked sql.NullTime
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.Name, &r.Hostname, &r.Version, &r.CreatedAt, &r.CreatedBy,
		&seen, &revoked); err != nil {
		return nil, err
	}
	if seen.Valid {
		t := seen.Time.UTC()
		r.LastSeenAt = &t
		r.Online = !revoked.Valid && s.now().Sub(t) < 2*DefaultLease
	}
	if revoked.Valid {
		t := revoked.Time.UTC()
		r.RevokedAt = &t
	}
	return &r, nil
}

func (s *Store) List(ctx context.Context, ws string) ([]Runner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runnerCols+` FROM runners
		WHERE workspace_id = $1 ORDER BY created_at DESC`, ws)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.list")
	}
	defer rows.Close()
	out := []Runner{}
	for rows.Next() {
		r, err := s.scanRunner(rows)
		if err != nil {
			return nil, apperr.Wrap(err, "runner.errors.list")
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// An empty ws skips the workspace check: only the daemon's own lookups may pass it.
func (s *Store) Get(ctx context.Context, ws, id string) (*Runner, error) {
	r, err := s.scanRunner(s.db.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ws != "" && r.WorkspaceID != ws) {
		return nil, apperr.NotFound("runner.errors.not_found", "id", id)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.get")
	}
	return r, nil
}

func (s *Store) Revoke(ctx context.Context, ws, id string) error {
	if _, err := s.Get(ctx, ws, id); err != nil {
		return err
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `UPDATE runners SET revoked_at = $1 WHERE id = $2 AND revoked_at IS NULL`,
		now, id); err != nil {
		return apperr.Wrap(err, "runner.errors.revoke")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET state = $1, exit_code = $2, finished_at = $3, spec = '{}'
		WHERE runner_id = $4 AND state IN ($5, $6)`, StateLost, ExitLost, now, id, StateQueued, StateRunning)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.revoke")
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, j *Job) error {
	if j.ID == "" {
		j.ID = newID()
	}
	j.Spec.ID = j.ID
	spec, err := json.Marshal(j.Spec)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.enqueue")
	}
	j.State = StateQueued
	j.CreatedAt = s.now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runner_jobs
		(id, runner_id, workspace_id, task_id, project_id, instance_id, agent_name, agent_type, engine, stage,
		 launch_kind, spec, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		j.ID, j.RunnerID, j.WorkspaceID, j.TaskID, j.ProjectID, j.InstanceID, j.AgentName, j.AgentType, j.Engine,
		j.Stage, j.LaunchKind, string(spec), StateQueued, j.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.enqueue")
	}
	return nil
}

// The env leaves the database with the claim: it holds the run's API key, which has no reason to
// stay at rest.
func (s *Store) Claim(ctx context.Context, runnerID string, projects []string, lease time.Duration) (*JobSpec, error) {
	now := s.now()
	var raw, id string
	err := s.db.QueryRowContext(ctx, `UPDATE runner_jobs SET state = $1, claimed_at = $2, lease_until = $3
		WHERE id = (SELECT id FROM runner_jobs
			WHERE runner_id = $4 AND state = $5 AND cancel = FALSE
			  AND (project_id = '' OR project_id = ANY(string_to_array($6, ',')))
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, spec`, StateRunning, now, now.Add(lease), runnerID, StateQueued, strings.Join(projects, ",")).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.claim")
	}
	var spec JobSpec
	if err = json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.claim")
	}
	kept := spec
	kept.Env = nil
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.claim")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET spec = $1 WHERE id = $2`, string(b), id); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.claim")
	}
	return &spec, nil
}

func (s *Store) Heartbeat(ctx context.Context, runnerID, jobID string, lease time.Duration) (cancel bool, err error) {
	err = s.db.QueryRowContext(ctx, `UPDATE runner_jobs SET lease_until = $1
		WHERE id = $2 AND runner_id = $3 AND state = $4 RETURNING cancel`,
		s.now().Add(lease), jobID, runnerID, StateRunning).Scan(&cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return false, apperr.Conflict("runner.errors.job_not_held", "id", jobID)
	}
	if err != nil {
		return false, apperr.Wrap(err, "runner.errors.heartbeat")
	}
	return cancel, nil
}

// A log chunk or an output upload must pass Held before it is written anywhere.
func (s *Store) Held(ctx context.Context, runnerID, jobID string) (*Job, error) {
	j, err := s.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if j.RunnerID != runnerID || j.State != StateRunning {
		return nil, apperr.Conflict("runner.errors.job_not_held", "id", jobID)
	}
	return j, nil
}

// Reporting the same end twice succeeds: a runner that lost the answer to its first call retries.
func (s *Store) Finish(ctx context.Context, runnerID, jobID string, res FinishRequest) error {
	b, err := json.Marshal(res)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.finish")
	}
	out, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET state = $1, exit_code = $2, result = $3,
		finished_at = $4, lease_until = NULL WHERE id = $5 AND runner_id = $6 AND state = $7`,
		StateFinished, res.ExitCode, string(b), s.now(), jobID, runnerID, StateRunning)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.finish")
	}
	if n, _ := out.RowsAffected(); n == 1 {
		return nil
	}
	j, err := s.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if j.RunnerID == runnerID && j.State == StateFinished {
		return nil
	}
	return apperr.Conflict("runner.errors.job_not_held", "id", jobID)
}

func (s *Store) RequestCancel(ctx context.Context, jobID string) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET state = $1, exit_code = $2, finished_at = $3,
		spec = '{}', cancel = TRUE WHERE id = $4 AND state = $5`, StateFinished, ExitCanceled, now, jobID, StateQueued); err != nil {
		return apperr.Wrap(err, "runner.errors.cancel")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET cancel = TRUE WHERE id = $1 AND state = $2`,
		jobID, StateRunning); err != nil {
		return apperr.Wrap(err, "runner.errors.cancel")
	}
	return nil
}

func (s *Store) Expire(ctx context.Context, claimTimeout time.Duration) (int64, error) {
	now := s.now()
	out, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET state = $1, exit_code = $2, finished_at = $3, spec = '{}'
		WHERE (state = $4 AND lease_until < $3) OR (state = $5 AND created_at < $6)`,
		StateLost, ExitLost, now, StateRunning, StateQueued, now.Add(-claimTimeout))
	if err != nil {
		return 0, apperr.Wrap(err, "runner.errors.expire")
	}
	return out.RowsAffected()
}

func (s *Store) Settled(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM runner_jobs
		WHERE state IN ($1, $2) AND reaped = FALSE ORDER BY finished_at`, StateFinished, StateLost)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.list_jobs")
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, apperr.Wrap(err, "runner.errors.list_jobs")
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *Store) MarkReaped(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET reaped = TRUE WHERE id = $1`, jobID)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.reap")
	}
	return nil
}

func (s *Store) GetJob(ctx context.Context, jobID string) (*Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM runner_jobs WHERE id = $1`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.NotFound("runner.errors.job_not_found", "id", jobID)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.get_job")
	}
	return j, nil
}

const jobCols = `id, runner_id, workspace_id, task_id, project_id, instance_id, agent_name, agent_type, engine, stage,
	launch_kind, spec, state, cancel, exit_code, result, created_at, claimed_at, finished_at, reaped`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var spec, result string
	var exit sql.NullInt64
	var claimed, finished sql.NullTime
	if err := row.Scan(&j.ID, &j.RunnerID, &j.WorkspaceID, &j.TaskID, &j.ProjectID, &j.InstanceID, &j.AgentName,
		&j.AgentType, &j.Engine, &j.Stage, &j.LaunchKind, &spec, &j.State, &j.Cancel, &exit, &result,
		&j.CreatedAt, &claimed, &finished, &j.Reaped); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spec), &j.Spec); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.decode_job", "id", j.ID)
	}
	if err := json.Unmarshal([]byte(result), &j.Result); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.decode_job", "id", j.ID)
	}
	if exit.Valid {
		code := int(exit.Int64)
		j.ExitCode = &code
	}
	if claimed.Valid {
		t := claimed.Time.UTC()
		j.ClaimedAt = &t
	}
	if finished.Valid {
		t := finished.Time.UTC()
		j.FinishedAt = &t
	}
	return &j, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
