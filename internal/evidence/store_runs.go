package evidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

//i18n:ignore SQL column list
const batchCols = `b.id, b.workspace_id, b.environment_id, b.trigger, b.trigger_ref, b.status, b.total, b.passed,
	b.failed, b.blocked, b.errored, b.canceled, b.runner, b.error, b.created_by, b.created_at, b.started_at, b.finished_at,
	b.base_url, b.source_branch, b.source_commit, b.sandbox_status, b.sandbox_phase, b.sandbox_error,
	b.sandbox_reason, b.sandbox_reason_args, b.locale`

func scanBatch(r rowScanner, extra ...any) (Batch, error) {
	var b Batch
	var started, finished sql.NullTime
	var reasonArgs string
	dest := append([]any{&b.ID, &b.WorkspaceID, &b.EnvironmentID, &b.Trigger, &b.TriggerRef, &b.Status, &b.Total,
		&b.Passed, &b.Failed, &b.Blocked, &b.Errored, &b.Canceled, &b.Runner, &b.Error, &b.CreatedBy,
		&b.CreatedAt, &started, &finished, &b.BaseURL, &b.SourceBranch, &b.SourceCommit, &b.SandboxStatus,
		&b.SandboxPhase, &b.SandboxError, &b.SandboxReason, &reasonArgs, &b.Locale}, extra...)
	err := r.Scan(dest...)
	b.CreatedAt = b.CreatedAt.UTC()
	b.StartedAt, b.FinishedAt = timePtr(started), timePtr(finished)
	if reasonArgs != "" && json.Unmarshal([]byte(reasonArgs), &b.reasonArgs) != nil {
		b.reasonArgs = nil
	}
	return b, err
}

func (b *Batch) runLocale() string {
	if b.Locale != "" {
		return b.Locale
	}
	return i18n.DaemonLocale()
}

func (b *Batch) localize(ctx context.Context) {
	loc := i18n.FromContext(ctx)
	b.ErrorCause = RunnerCause(b.Error)
	if b.ErrorCause != "" {
		b.Error = LocalizeRunnerError(b.Error, loc)
	}
	if b.SandboxReason == "" {
		return
	}
	reason := Reason{Code: b.SandboxReason, Args: b.reasonArgs}
	stored, why := reason.String(), reason.Text(loc)
	if b.SandboxError != "" {
		b.SandboxError = why
	}
	if b.Status == BatchError {
		b.Error = i18n.T(loc, "evidence.reasons.sandbox_failed", "reason", why)
	}
	for i := range b.Runs {
		if b.Runs[i].Status == RunCanceled && b.Runs[i].Error == stored {
			b.Runs[i].Error = why
		}
	}
}

type BatchSpec struct {
	EnvironmentID     string   `json:"environment_id"`
	CaseIDs           []string `json:"case_ids,omitempty"`
	ProjectID         string   `json:"project_id,omitempty"`
	IncludeUnassigned bool     `json:"include_unassigned,omitempty"`
	Tag               string   `json:"tag,omitempty"`
	Branch            string   `json:"branch,omitempty"`
	Trigger           string   `json:"-"`
	TriggerRef        string   `json:"-"`
	Locale            string   `json:"-"`
}

// Must agree with CaseRunsOn. Columns are unqualified (evidence_cases).
func eligibleCases(env *Environment) (string, []any) {
	switch env.Kind {
	case KindProject:
		return "project_id IN ('', ?)", []any{env.ProjectID}
	case KindSandbox:
		return "project_id = ?", []any{env.ProjectID}
	}
	return "", nil
}

// Each run copies the take, so re-recording a case does not change a replay already queued.
func (s *Store) CreateBatch(ctx context.Context, ws, author string, spec BatchSpec) (*Batch, error) {
	env, err := s.GetEnvironment(ctx, ws, spec.EnvironmentID)
	if err != nil {
		return nil, err
	}
	b := Batch{ID: newID(), WorkspaceID: ws, EnvironmentID: env.ID, Trigger: spec.Trigger, TriggerRef: spec.TriggerRef,
		Status: BatchQueued, CreatedBy: author, CreatedAt: time.Now().UTC(), BaseURL: env.BaseURL, Locale: spec.Locale}
	if b.Trigger == "" {
		b.Trigger = TriggerManual
	}
	switch {
	case env.Kind == KindSandbox:
		if b.SourceBranch, err = sandboxBranch(spec.Branch, env.DefaultBranch); err != nil {
			return nil, err
		}
		b.SandboxStatus = SandboxPending
	case strings.TrimSpace(spec.Branch) != "":
		return nil, apperr.Invalid("evidence.errors.branch_sandbox_only")
	case env.BaseURL == "":
		return nil, apperr.Precondition("evidence.errors.env_no_url", "env", env.Label())
	}
	//i18n:ignore SQL fragment
	where := []string{"workspace_id = ?", "take IS NOT NULL"}
	args := []any{ws}
	if len(spec.CaseIDs) > 0 {
		cond, ids := caseRefs(spec.CaseIDs)
		if eligErr := s.checkEligible(ctx, ws, env, cond, ids); eligErr != nil {
			return nil, eligErr
		}
		where = append(where, cond)
		args = append(args, ids...)
	}
	if cond, cargs := projectCond("project_id", spec.ProjectID, spec.IncludeUnassigned); cond != "" {
		where = append(where, cond)
		args = append(args, cargs...)
	}
	if cond, cargs := eligibleCases(env); cond != "" {
		where = append(where, cond)
		args = append(args, cargs...)
	}
	if spec.Tag != "" {
		where = append(where, "tags @> CAST(? AS jsonb)")
		args = append(args, jsonText([]string{spec.Tag}))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.begin")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, s.rb(`INSERT INTO evidence_batches (id, workspace_id, environment_id, trigger,
		trigger_ref, status, created_by, created_at, base_url, source_branch, sandbox_status, locale)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
		b.ID, ws, env.ID, b.Trigger, b.TriggerRef, BatchQueued, author, b.CreatedAt, b.BaseURL, b.SourceBranch,
		b.SandboxStatus, b.Locale); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_batch")
	}
	res, err := tx.ExecContext(ctx, s.rb(`INSERT INTO evidence_runs (id, workspace_id, batch_id, case_id,
		environment_id, position, status, take, created_at)
		SELECT substr(md5(random()::text || id), 1, 16), workspace_id, ?, id, ?,
			ROW_NUMBER() OVER (ORDER BY CAST(SUBSTRING(key FROM '[0-9]+$') AS INTEGER) NULLS LAST, created_at),
			'queued', take, ?
		FROM evidence_cases WHERE `+strings.Join(where, " AND ")), append([]any{b.ID, env.ID, b.CreatedAt}, args...)...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_batch")
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, apperr.Precondition("evidence.errors.nothing_to_run")
	}
	b.Total = int(n)
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET total = ? WHERE id = ?`), b.Total, b.ID); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_batch")
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.commit")
	}
	return s.GetBatch(ctx, ws, b.ID)
}

func caseRefs(refs []string) (string, []any) {
	ph := placeholders(len(refs))
	args := make([]any, 0, 2*len(refs))
	for _, id := range refs {
		args = append(args, id)
	}
	for _, id := range refs {
		args = append(args, id)
	}
	//i18n:ignore SQL fragment
	return "(id IN (" + ph + ") OR key IN (" + ph + "))", args
}

func (s *Store) checkEligible(ctx context.Context, ws string, env *Environment, refCond string, refArgs []any) error {
	cond, cargs := eligibleCases(env)
	if cond == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT key FROM evidence_cases WHERE workspace_id = ? AND `+refCond+
		` AND NOT (`+cond+`) ORDER BY key`), append(append([]any{ws}, refArgs...), cargs...)...)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.insert_batch")
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err = rows.Scan(&k); err != nil {
			return apperr.Wrap(err, "evidence.errors.insert_batch")
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		return apperr.Wrap(err, "evidence.errors.insert_batch")
	}
	if len(keys) > 0 {
		return apperr.Conflict("evidence.errors.case_not_on_env", "keys", strings.Join(keys, ", "), "env", env.Label())
	}
	return nil
}

func sandboxBranch(asked, defaultBranch string) (string, error) {
	branch := strings.TrimSpace(asked)
	if branch == "" {
		branch = defaultBranch
	}
	if branch == "" {
		branch = "main"
	}
	if err := project.ValidateBranchName(branch); err != nil {
		return "", apperr.Invalid("evidence.errors.branch_invalid", "branch", branch)
	}
	return branch, nil
}

func (s *Store) GetBatch(ctx context.Context, ws, id string) (*Batch, error) {
	var log string
	b, err := scanBatch(s.db.QueryRowContext(ctx, s.rb(`SELECT `+batchCols+`, b.sandbox_log FROM evidence_batches b
		WHERE b.workspace_id = ? AND b.id = ?`), ws, id), &log)
	if err != nil {
		return nil, notFound(err, "evidence.errors.batch_not_found", "id", id)
	}
	b.SandboxLog = log
	if b.Runs, err = s.ListRuns(ctx, RunFilter{WorkspaceID: ws, BatchID: id, Limit: MaxListLimit}); err != nil {
		return nil, err
	}
	b.localize(ctx)
	return &b, nil
}

func (s *Store) BatchHeader(ctx context.Context, ws, id string) (*Batch, error) {
	b, err := scanBatch(s.db.QueryRowContext(ctx, s.rb(`SELECT `+batchCols+` FROM evidence_batches b
		WHERE b.workspace_id = ? AND b.id = ?`), ws, id))
	if err != nil {
		return nil, notFound(err, "evidence.errors.batch_not_found", "id", id)
	}
	return &b, nil
}

func (s *Store) batchByID(ctx context.Context, id string) (*Batch, error) {
	b, err := scanBatch(s.db.QueryRowContext(ctx, s.rb(`SELECT `+batchCols+` FROM evidence_batches b WHERE b.id = ?`), id))
	if err != nil {
		return nil, notFound(err, "evidence.errors.batch_not_found", "id", id)
	}
	return &b, nil
}

func (s *Store) ListBatches(ctx context.Context, ws, envID, projectID string, limit int) ([]Batch, error) {
	limit = clampLimit(limit)
	q := `SELECT ` + batchCols + ` FROM evidence_batches b WHERE b.workspace_id = ?`
	args := []any{ws}
	if envID != "" {
		q += ` AND b.environment_id = ?`
		args = append(args, envID)
	}
	if cond, cargs := projectCond("e.project_id", projectID, false); cond != "" {
		//i18n:ignore SQL fragment
		q += ` AND b.environment_id IN (SELECT e.id FROM evidence_environments e WHERE ` + cond + `)`
		args = append(args, cargs...)
	}
	q += ` ORDER BY b.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_batches")
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		b, scanErr := scanBatch(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_batches")
		}
		b.localize(ctx)
		out = append(out, b)
	}
	return out, rows.Err()
}

func clampLimit(n int) int {
	if n <= 0 {
		return DefaultListLimit
	}
	if n > MaxListLimit {
		return MaxListLimit
	}
	return n
}

func (s *Store) QueuedBatches(ctx context.Context) ([]Batch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+batchCols+` FROM evidence_batches b
		WHERE b.status = 'queued' ORDER BY b.created_at`)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_batches")
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		b, scanErr := scanBatch(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_batches")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) RunningBatches(ctx context.Context) ([]Batch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+batchCols+` FROM evidence_batches b WHERE b.status = 'running'`)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_batches")
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		b, scanErr := scanBatch(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_batches")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) StartBatch(ctx context.Context, id, runner string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET status = 'running', runner = ?,
		started_at = ? WHERE id = ? AND status = 'queued'`), runner, time.Now().UTC(), id)
	if err != nil {
		return false, apperr.Wrap(err, "evidence.errors.update_batch")
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CloseBatch(ctx context.Context, id, reason string) error {
	now := time.Now().UTC()
	msg := reason
	if msg == "" {
		msg = i18n.T(i18n.SourceLocale, "evidence.reasons.runner_lost")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_runs SET status = 'error', error = ?, finished_at = ?
		WHERE batch_id = ? AND status IN ('queued', 'running')`), msg, now, id); err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	if err := s.syncCasesOfBatch(ctx, id); err != nil {
		return err
	}
	if err := s.recount(ctx, id); err != nil {
		return err
	}
	// FailSandboxBatch may have closed the batch before the runner exits; keep its status and
	// error.
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET
		status = CASE WHEN status = 'canceled' OR sandbox_status = 'failed' OR finished_at IS NOT NULL THEN status
			WHEN ? <> '' AND passed + failed + blocked = 0 THEN 'error' ELSE 'done' END,
		error = CASE WHEN status = 'canceled' OR sandbox_status = 'failed' OR finished_at IS NOT NULL THEN error ELSE ? END,
		finished_at = COALESCE(finished_at, ?) WHERE id = ?`), reason, reason, now, id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func (s *Store) CancelBatch(ctx context.Context, ws, id string) (*Batch, error) {
	b, err := s.GetBatch(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if b.Status != BatchQueued && b.Status != BatchRunning {
		return nil, apperr.Conflict("evidence.errors.batch_closed")
	}
	now := time.Now().UTC()
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_runs SET status = 'canceled', finished_at = ?
		WHERE batch_id = ? AND status = 'queued'`), now, id); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_batch")
	}
	if recountErr := s.recount(ctx, id); recountErr != nil {
		return nil, recountErr
	}
	// A running batch keeps finished_at empty: its runner finishes the current run, gets 204, and
	// CloseBatch stamps it.
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET status = 'canceled',
		finished_at = CASE WHEN started_at IS NULL THEN ? ELSE finished_at END WHERE id = ?`), now, id); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return s.GetBatch(ctx, ws, id)
}

func (s *Store) finishCanceled(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET finished_at = COALESCE(finished_at, ?)
		WHERE id = ? AND status = 'canceled'`), time.Now().UTC(), id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func (s *Store) MarkSandboxStarting(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET sandbox_status = 'starting', sandbox_phase = ''
		WHERE id = ? AND sandbox_status = 'pending'`), id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func (s *Store) MarkSandboxPhase(ctx context.Context, id, phase string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET sandbox_phase = ?
		WHERE id = ? AND sandbox_status = 'starting'`), phase, id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func (s *Store) MarkSandboxReady(ctx context.Context, id, baseURL, commit string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET sandbox_status = 'ready', sandbox_phase = '',
		base_url = ?, source_commit = ? WHERE id = ? AND sandbox_status = 'starting' AND status = 'running'`),
		strings.TrimRight(baseURL, "/"), commit, id)
	if err != nil {
		return false, apperr.Wrap(err, "evidence.errors.update_batch")
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) MarkSandboxReleased(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET sandbox_status = 'released', sandbox_phase = ''
		WHERE id = ? AND sandbox_status IN ('pending', 'starting', 'ready')`), id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

const sandboxLogMax = 3000

func (s *Store) FailSandboxBatch(ctx context.Context, id string, reason Reason, log string) error {
	now := time.Now().UTC()
	why := reason.String()
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_runs SET status = 'canceled', error = ?, finished_at = ?
		WHERE batch_id = ? AND status IN ('queued', 'running')`), why, now, id); err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	if err := s.recount(ctx, id); err != nil {
		return err
	}
	args := ""
	if len(reason.Args) > 0 {
		raw, err := json.Marshal(reason.Args)
		if err != nil {
			return apperr.Wrap(err, "evidence.errors.update_batch")
		}
		args = string(raw)
	}
	msg := i18n.T(i18n.SourceLocale, "evidence.reasons.sandbox_failed", "reason", why)
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches SET
		status = CASE WHEN status = 'canceled' THEN status ELSE 'error' END,
		error = CASE WHEN status = 'canceled' THEN error ELSE ? END,
		sandbox_status = CASE WHEN sandbox_status = '' THEN '' ELSE 'failed' END, sandbox_phase = '',
		sandbox_error = CASE WHEN sandbox_status = '' THEN '' ELSE ? END,
		sandbox_reason = ?, sandbox_reason_args = ?,
		sandbox_log = CASE WHEN sandbox_status = '' THEN '' ELSE ? END,
		finished_at = COALESCE(finished_at, ?) WHERE id = ?`), msg, why, reason.Code, args,
		logTail(log, sandboxLogMax), now, id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func logTail(log string, maxBytes int) string {
	if len(log) <= maxBytes {
		return log
	}
	tail := log[len(log)-maxBytes:]
	for i := 0; i < len(tail) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(tail[i]) {
			return tail[i:]
		}
	}
	return tail
}

func (s *Store) recount(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_batches b SET
		passed = c.passed, failed = c.failed, blocked = c.blocked, errored = c.errored, canceled = c.canceled
		FROM (SELECT
			COUNT(*) FILTER (WHERE status = 'pass') AS passed,
			COUNT(*) FILTER (WHERE status = 'fail') AS failed,
			COUNT(*) FILTER (WHERE status = 'blocked') AS blocked,
			COUNT(*) FILTER (WHERE status = 'error') AS errored,
			COUNT(*) FILTER (WHERE status = 'canceled') AS canceled
			FROM evidence_runs WHERE batch_id = ?) c
		WHERE b.id = ?`), id, id)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_batch")
	}
	return nil
}

func (s *Store) syncCasesOfBatch(ctx context.Context, batchID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases c SET status = r.status, last_run_at = r.finished_at
		FROM (SELECT DISTINCT ON (case_id) case_id, status, finished_at FROM evidence_runs
			WHERE case_id IN (SELECT case_id FROM evidence_runs WHERE batch_id = ?)
			AND status IN ('pass', 'fail', 'blocked', 'error')
			ORDER BY case_id, finished_at DESC NULLS LAST) r
		WHERE c.id = r.case_id`), batchID)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_case")
	}
	return nil
}

//i18n:ignore SQL column list
const runCols = `r.id, r.workspace_id, r.batch_id, r.case_id, COALESCE(c.key, ''), COALESCE(c.title, ''),
	r.environment_id, r.position, r.status, r.checks::text, r.failures::text, r.error, r.duration_ms, r.video_id,
	r.take_id, r.chapters_id, r.poster_id, r.shot_ids::text, COALESCE(b.trigger, ''), COALESCE(b.trigger_ref, ''),
	r.created_at, r.started_at, r.finished_at`

const runFrom = ` FROM evidence_runs r LEFT JOIN evidence_cases c ON c.id = r.case_id
	LEFT JOIN evidence_batches b ON b.id = r.batch_id`

func (r *Run) localize(ctx context.Context) {
	if r.ErrorCause != "" {
		r.Error = LocalizeRunnerError(r.Error, i18n.FromContext(ctx))
	}
}

func scanRun(rs rowScanner, extra ...any) (Run, error) {
	var r Run
	var checks, failures, shots string
	var started, finished sql.NullTime
	dest := append([]any{&r.ID, &r.WorkspaceID, &r.BatchID, &r.CaseID, &r.CaseKey, &r.CaseTitle, &r.EnvironmentID,
		&r.Position, &r.Status, &checks, &failures, &r.Error, &r.DurationMS, &r.VideoID, &r.TakeID, &r.ChaptersID,
		&r.PosterID, &shots, &r.Trigger, &r.TriggerRef, &r.CreatedAt, &started, &finished}, extra...)
	if err := rs.Scan(dest...); err != nil {
		return r, err
	}
	for _, f := range []struct {
		raw string
		dst any
	}{{checks, &r.Checks}, {failures, &r.Failures}, {shots, &r.ShotIDs}} {
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return r, err
		}
	}
	if r.Checks == nil {
		r.Checks = []Check{}
	}
	if r.Failures == nil {
		r.Failures = []Failure{}
	}
	if r.ShotIDs == nil {
		r.ShotIDs = []string{}
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.StartedAt, r.FinishedAt = timePtr(started), timePtr(finished)
	r.ErrorCause = RunnerCause(r.Error)
	return r, nil
}

func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	where := []string{"r.workspace_id = ?"}
	args := []any{f.WorkspaceID}
	order := " ORDER BY r.created_at DESC, r.position DESC"
	if f.BatchID != "" {
		where = append(where, "r.batch_id = ?")
		args = append(args, f.BatchID)
		order = " ORDER BY r.position"
	}
	if f.CaseID != "" {
		where = append(where, "r.case_id = ?")
		args = append(args, f.CaseID)
	}
	if f.EnvironmentID != "" {
		where = append(where, "r.environment_id = ?")
		args = append(args, f.EnvironmentID)
	}
	if len(f.Statuses) > 0 {
		//i18n:ignore SQL fragment
		where = append(where, "r.status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")+")")
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+runCols+runFrom+` WHERE `+strings.Join(where, " AND ")+order+` LIMIT ?`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_runs")
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, scanErr := scanRun(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_runs")
		}
		r.localize(ctx)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRun(ctx context.Context, ws, id string) (*Run, error) {
	var take sql.NullString
	var report string
	//i18n:ignore SQL fragment
	r, err := scanRun(s.db.QueryRowContext(ctx, s.rb(`SELECT `+runCols+`, r.take::text, r.report_md`+runFrom+
		` WHERE r.workspace_id = ? AND r.id = ?`), ws, id), &take, &report)
	if err != nil {
		return nil, notFound(err, "evidence.errors.run_not_found", "id", id)
	}
	r.ReportMD = report
	r.localize(ctx)
	if take.Valid {
		r.Take = json.RawMessage(take.String)
	}
	if r.Artifacts, err = s.ListArtifacts(ctx, ws, id); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ClaimNextRun(ctx context.Context, ws, batchID string) (*Run, error) {
	b, err := s.batchByID(ctx, batchID)
	if err != nil || b.WorkspaceID != ws {
		return nil, apperr.NotFound("evidence.errors.batch_not_found", "id", batchID)
	}
	if b.Status != BatchRunning {
		return nil, nil
	}
	var id string
	err = s.db.QueryRowContext(ctx, s.rb(`UPDATE evidence_runs SET status = 'running', started_at = ?
		WHERE id = (SELECT id FROM evidence_runs WHERE batch_id = ? AND status = 'queued'
			ORDER BY position LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`), time.Now().UTC(), batchID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.claim_run")
	}
	return s.GetRun(ctx, ws, id)
}

func (s *Store) runInBatch(ctx context.Context, ws, batchID, runID string) (status string, err error) {
	err = s.db.QueryRowContext(ctx, s.rb(`SELECT status FROM evidence_runs
		WHERE id = ? AND batch_id = ? AND workspace_id = ?`), runID, batchID, ws).Scan(&status)
	if err != nil {
		return "", notFound(err, "evidence.errors.run_not_found", "id", runID)
	}
	return status, nil
}

func (s *Store) FinishRun(ctx context.Context, ws, batchID, runID string, res RunResult) (*Run, error) {
	status, err := s.runInBatch(ctx, ws, batchID, runID)
	if err != nil {
		return nil, err
	}
	if status != RunRunning {
		return nil, apperr.Conflict("evidence.errors.run_not_running")
	}
	switch res.Status {
	case RunPass, RunFail, RunBlocked, RunError:
	default:
		return nil, apperr.Invalid("evidence.errors.result_status", "status", res.Status)
	}
	ids := append([]string{res.VideoID, res.TakeID, res.ChaptersID, res.PosterID}, res.ShotIDs...)
	for _, aid := range ids {
		if aid == "" {
			continue
		}
		var n int
		if err = s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM evidence_artifacts WHERE id = ? AND run_id = ?`),
			aid, runID).Scan(&n); err != nil || n == 0 {
			return nil, apperr.Invalid("evidence.errors.artifact_not_of_run", "id", aid)
		}
	}
	if len(res.ReportMD) > maxReportBytes {
		res.ReportMD = res.ReportMD[:maxReportBytes]
	}
	if res.ShotIDs == nil {
		res.ShotIDs = []string{}
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_runs SET status = ?, checks = CAST(? AS jsonb),
		failures = CAST(? AS jsonb), error = ?, duration_ms = ?, report_md = ?, video_id = ?, take_id = ?,
		chapters_id = ?, poster_id = ?, shot_ids = CAST(? AS jsonb), finished_at = ? WHERE id = ?`),
		res.Status, jsonText(res.Checks), jsonText(res.Failures), res.Error, res.DurationMS, res.ReportMD,
		res.VideoID, res.TakeID, res.ChaptersID, res.PosterID, jsonText(res.ShotIDs), now, runID)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.finish_run")
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases SET status = ?, last_run_at = ?
		WHERE id = (SELECT case_id FROM evidence_runs WHERE id = ?)`), res.Status, now, runID); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_case")
	}
	if err := s.recount(ctx, batchID); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, ws, runID)
}

const artifactCols = `id, workspace_id, run_id, kind, filename, mime_type, size, path, created_at`

func scanArtifact(r rowScanner) (Artifact, error) {
	var a Artifact
	err := r.Scan(&a.ID, &a.WorkspaceID, &a.RunID, &a.Kind, &a.Filename, &a.MimeType, &a.Size, &a.Path, &a.CreatedAt)
	a.CreatedAt = a.CreatedAt.UTC()
	return a, err
}

func (s *Store) RunAcceptsArtifacts(ctx context.Context, ws, batchID, runID string) error {
	status, err := s.runInBatch(ctx, ws, batchID, runID)
	if err != nil {
		return err
	}
	if status != RunRunning {
		return apperr.Conflict("evidence.errors.run_not_running")
	}
	return nil
}

func (s *Store) AddArtifact(ctx context.Context, a Artifact) (*Artifact, error) {
	a.ID, a.CreatedAt = newID(), time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_artifacts (`+artifactCols+`) VALUES (?,?,?,?,?,?,?,?,?)`),
		a.ID, a.WorkspaceID, a.RunID, a.Kind, a.Filename, a.MimeType, a.Size, a.Path, a.CreatedAt)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_artifact")
	}
	return &a, nil
}

func (s *Store) GetArtifact(ctx context.Context, ws, id string) (*Artifact, error) {
	a, err := scanArtifact(s.db.QueryRowContext(ctx, s.rb(`SELECT `+artifactCols+` FROM evidence_artifacts
		WHERE workspace_id = ? AND id = ?`), ws, id))
	if err != nil {
		return nil, notFound(err, "evidence.errors.artifact_not_found", "id", id)
	}
	return &a, nil
}

func (s *Store) ListArtifacts(ctx context.Context, ws, runID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+artifactCols+` FROM evidence_artifacts
		WHERE workspace_id = ? AND run_id = ? ORDER BY created_at, id`), ws, runID)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_artifacts")
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		a, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_artifacts")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) StorageBytes(ctx context.Context, ws string) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT SUM(size) FROM evidence_artifacts WHERE workspace_id = ?`), ws).Scan(&n)
	return n.Int64, err
}

const recordingCols = `id, workspace_id, case_id, environment_id, chat_id, status, created_by, created_at, saved_at`

func scanRecording(r rowScanner) (Recording, error) {
	var rec Recording
	var saved sql.NullTime
	err := r.Scan(&rec.ID, &rec.WorkspaceID, &rec.CaseID, &rec.EnvironmentID, &rec.ChatID, &rec.Status,
		&rec.CreatedBy, &rec.CreatedAt, &saved)
	rec.CreatedAt, rec.SavedAt = rec.CreatedAt.UTC(), timePtr(saved)
	return rec, err
}

func (s *Store) CreateRecording(ctx context.Context, ws, caseID, envID, chatID, author string) (*Recording, error) {
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_recordings SET status = 'closed'
		WHERE workspace_id = ? AND case_id = ? AND status = 'open'`), ws, caseID); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_recording")
	}
	rec := Recording{ID: newID(), WorkspaceID: ws, CaseID: caseID, EnvironmentID: envID, ChatID: chatID,
		Status: RecordingOpen, CreatedBy: author, CreatedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_recordings (`+recordingCols+`) VALUES (?,?,?,?,?,?,?,?,?)`),
		rec.ID, ws, caseID, envID, chatID, rec.Status, author, rec.CreatedAt, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_recording")
	}
	return &rec, nil
}

func (s *Store) OpenRecordingForChat(ctx context.Context, ws, chatID string) (*Recording, error) {
	rec, err := scanRecording(s.db.QueryRowContext(ctx, s.rb(`SELECT `+recordingCols+` FROM evidence_recordings
		WHERE workspace_id = ? AND chat_id = ? AND status = 'open' ORDER BY created_at DESC LIMIT 1`), ws, chatID))
	if err != nil {
		return nil, notFound(err, "evidence.errors.no_recording")
	}
	return &rec, nil
}

func (s *Store) IsRecordingChat(ctx context.Context, chatID string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM evidence_recordings WHERE chat_id = ? AND status = 'open'`),
		chatID).Scan(&n)
	return err == nil && n > 0
}

func (s *Store) MarkRecordingSaved(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_recordings SET status = 'saved', saved_at = ? WHERE id = ?`),
		time.Now().UTC(), id)
	return err
}

func (s *Store) ListRecordings(ctx context.Context, ws, caseID string) ([]Recording, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+recordingCols+` FROM evidence_recordings
		WHERE workspace_id = ? AND case_id = ? ORDER BY created_at DESC LIMIT 20`), ws, caseID)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_recordings")
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		rec, scanErr := scanRecording(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_recordings")
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) Summary(ctx context.Context, ws, projectID string, withUnassigned bool) (*Summary, error) {
	sum := &Summary{Counts: map[string]int{}}
	where, args := "workspace_id = ?", []any{ws}
	if cond, cargs := projectCond("project_id", projectID, withUnassigned); cond != "" {
		where += " AND " + cond
		args = append(args, cargs...)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT status, COUNT(*), COUNT(*) FILTER (WHERE take IS NOT NULL)
		FROM evidence_cases WHERE `+where+` GROUP BY status`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.summary")
	}
	if scanErr := scanSummaryCounts(rows, sum); scanErr != nil {
		return nil, apperr.Wrap(scanErr, "evidence.errors.summary")
	}
	if err = s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM evidence_runs WHERE workspace_id = ?
		AND status IN ('queued', 'running') AND case_id IN (SELECT id FROM evidence_cases WHERE `+where+`)`),
		append([]any{ws}, args...)...).Scan(&sum.Running); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.summary")
	}
	batchProject := projectID
	if withUnassigned {
		batchProject = ""
	}
	bs, err := s.ListBatches(ctx, ws, "", batchProject, 1)
	if err != nil {
		return nil, err
	}
	if len(bs) > 0 {
		sum.LastBatch = &bs[0]
	}
	return sum, nil
}

func scanSummaryCounts(rows *sql.Rows, sum *Summary) error {
	defer rows.Close()
	for rows.Next() {
		var st string
		var n, rec int
		if err := rows.Scan(&st, &n, &rec); err != nil {
			return err
		}
		sum.Counts[st] = n
		sum.Cases += n
		sum.Recorded += rec
	}
	return rows.Err()
}
