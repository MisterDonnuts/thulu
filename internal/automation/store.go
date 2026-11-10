package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Missing and other-workspace rows are deliberately indistinguishable.
var ErrNotFound error = apperr.NotFound("automation.errors.not_found")

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DB() *sql.DB { return s.db }

// An empty WorkspaceID reaches nothing; the all-tenant wildcard must be asked for explicitly via
// All.
// Treating empty as wildcard let an unattributed webhook fire every tenant's recipes.
type Scope struct {
	WorkspaceID string
	All         bool
}

func InWorkspace(workspaceID string) Scope { return Scope{WorkspaceID: workspaceID} }

var AllWorkspaces = Scope{All: true}

func (sc Scope) clause() (string, []any) {
	switch {
	case sc.All:
		return "TRUE", nil
	case sc.WorkspaceID == "":
		return "FALSE", nil
	default:
		return "workspace_id = ?", []any{sc.WorkspaceID}
	}
}

// Never change: databases that already ran these migrations under this name would rerun them all.
const MigrationComponent = "automation"

// Published migrations never change; internal/schema reads this list for the migrations lock and
// lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS automations (
				id             TEXT PRIMARY KEY,
				workspace_id   TEXT NOT NULL DEFAULT '',
				name           TEXT NOT NULL,
				description    TEXT NOT NULL DEFAULT '',
				enabled        BOOLEAN NOT NULL DEFAULT TRUE,
				spec           TEXT NOT NULL,
				trigger_kind   TEXT NOT NULL DEFAULT 'manual',
				trigger_event  TEXT NOT NULL DEFAULT '',
				cron           TEXT NOT NULL DEFAULT '',
				tz             TEXT NOT NULL DEFAULT '',
				next_run_at    TIMESTAMP,
				last_run_at    TIMESTAMP,
				last_status    TEXT NOT NULL DEFAULT '',
				created_at     TIMESTAMP NOT NULL,
				updated_at     TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_automations_ws ON automations(workspace_id);
			CREATE INDEX IF NOT EXISTS idx_automations_due
				ON automations(enabled, trigger_kind, next_run_at);
			CREATE INDEX IF NOT EXISTS idx_automations_event
				ON automations(enabled, trigger_event);

			CREATE TABLE IF NOT EXISTS automation_runs (
				id             TEXT PRIMARY KEY,
				automation_id  TEXT NOT NULL,
				workspace_id   TEXT NOT NULL DEFAULT '',
				status         TEXT NOT NULL,
				trigger        TEXT NOT NULL DEFAULT '',
				reason         TEXT NOT NULL DEFAULT '',
				started_at     TIMESTAMP NOT NULL,
				finished_at    TIMESTAMP,
				error          TEXT NOT NULL DEFAULT '',
				agent_runs     INTEGER NOT NULL DEFAULT 0,
				items_emitted  INTEGER NOT NULL DEFAULT 0,
				payload        TEXT NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_automation_runs_list
				ON automation_runs(automation_id, started_at DESC);
			CREATE INDEX IF NOT EXISTS idx_automation_runs_live
				ON automation_runs(automation_id, status);

			CREATE TABLE IF NOT EXISTS automation_run_steps (
				id           TEXT PRIMARY KEY,
				run_id       TEXT NOT NULL,
				seq          INTEGER NOT NULL,
				step_id      TEXT NOT NULL,
				kind         TEXT NOT NULL,
				status       TEXT NOT NULL,
				started_at   TIMESTAMP NOT NULL,
				finished_at  TIMESTAMP,
				output       TEXT NOT NULL DEFAULT '',
				error        TEXT NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_automation_run_steps ON automation_run_steps(run_id, seq);

			CREATE TABLE IF NOT EXISTS automation_fingerprints (
				automation_id TEXT NOT NULL,
				fingerprint   TEXT NOT NULL,
				workspace_id  TEXT NOT NULL DEFAULT '',
				task_id       TEXT NOT NULL,
				created_at    TIMESTAMP NOT NULL,
				PRIMARY KEY (automation_id, fingerprint)
			);
			CREATE INDEX IF NOT EXISTS idx_automation_fp_task ON automation_fingerprints(task_id);

			CREATE TABLE IF NOT EXISTS automation_events (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL DEFAULT '',
				kind         TEXT NOT NULL,
				payload      TEXT NOT NULL DEFAULT '{}',
				dedupe_key   TEXT NOT NULL DEFAULT '',
				created_at   TIMESTAMP NOT NULL,
				claimed_at   TIMESTAMP,
				processed_at TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_automation_events_pending
				ON automation_events(processed_at, created_at);`},
		{ID: 2, SQL: `
			ALTER TABLE automations ADD COLUMN IF NOT EXISTS trigger_firing BOOLEAN NOT NULL DEFAULT FALSE;
			ALTER TABLE automations ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMP`},
		{ID: 3, SQL: `
			ALTER TABLE automation_runs ADD COLUMN IF NOT EXISTS dedupe_key TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_auto_runs_dedupe
			    ON automation_runs (automation_id, dedupe_key, started_at DESC)`},
		// The plain reason/error/output columns stay populated for older readers.
		{ID: 4, SQL: `
			ALTER TABLE automation_runs ADD COLUMN IF NOT EXISTS reason_i18n TEXT NOT NULL DEFAULT '';
			ALTER TABLE automation_runs ADD COLUMN IF NOT EXISTS error_i18n TEXT NOT NULL DEFAULT '';
			ALTER TABLE automation_run_steps ADD COLUMN IF NOT EXISTS output_i18n TEXT NOT NULL DEFAULT '';
			ALTER TABLE automation_run_steps ADD COLUMN IF NOT EXISTS error_i18n TEXT NOT NULL DEFAULT ''`},
		{ID: 5, SQL: `
			CREATE TABLE IF NOT EXISTS automation_debounce (
				automation_id TEXT NOT NULL,
				dedupe_key    TEXT NOT NULL,
				workspace_id  TEXT NOT NULL DEFAULT '',
				payload       JSONB NOT NULL DEFAULT '{}',
				events        JSONB NOT NULL DEFAULT '[]',
				event_count   INTEGER NOT NULL DEFAULT 0,
				first_at      TIMESTAMP NOT NULL,
				fire_at       TIMESTAMP NOT NULL,
				PRIMARY KEY (automation_id, dedupe_key)
			);
			CREATE INDEX IF NOT EXISTS idx_automation_debounce_due ON automation_debounce(fire_at)`},
		{ID: 6, SQL: `
			CREATE TABLE IF NOT EXISTS automation_task_origins (
				task_id       TEXT PRIMARY KEY,
				automation_id TEXT NOT NULL,
				workspace_id  TEXT NOT NULL DEFAULT '',
				depth         INTEGER NOT NULL DEFAULT 1,
				created_at    TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_automation_task_origins_ws ON automation_task_origins(workspace_id)`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "automation.errors.schema")
	}
	return &Store{db: db}, nil
}

const automationCols = `id, workspace_id, name, description, enabled, spec,
	trigger_kind, trigger_event, cron, tz, next_run_at, last_run_at, last_status,
	created_at, updated_at`

func (s *Store) Create(ctx context.Context, workspaceID, spec string, enabled bool) (*Automation, error) {
	parsed, err := ParseSpec(spec)
	if err != nil {
		return nil, err
	}
	a := &Automation{
		ID:          generateID(),
		WorkspaceID: workspaceID,
		Enabled:     enabled,
		Spec:        spec,
		Parsed:      parsed,
		CreatedAt:   time.Now().UTC(),
	}
	a.UpdatedAt = a.CreatedAt
	applySpec(a, parsed)
	if nextErr := s.computeNextRun(a, a.CreatedAt); nextErr != nil {
		return nil, nextErr
	}
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automations (`+automationCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		a.ID, a.WorkspaceID, a.Name, a.Description, a.Enabled, a.Spec,
		a.TriggerKind, a.Event, a.Cron, a.TZ, a.NextRunAt, a.LastRunAt, a.LastStatus,
		a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) Update(ctx context.Context, id string, scope Scope, spec string, enabled bool) (*Automation, error) {
	cur, err := s.Get(ctx, id, scope)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSpec(spec)
	if err != nil {
		return nil, err
	}
	cur.Spec = spec
	cur.Parsed = parsed
	cur.Enabled = enabled
	cur.UpdatedAt = time.Now().UTC()
	applySpec(cur, parsed)
	if nextErr := s.computeNextRun(cur, cur.UpdatedAt); nextErr != nil {
		return nil, nextErr
	}
	where, scopeArgs := scope.clause()
	args := append([]any{cur.Name, cur.Description, cur.Enabled, cur.Spec,
		cur.TriggerKind, cur.Event, cur.Cron, cur.TZ, cur.NextRunAt,
		cur.UpdatedAt, id}, scopeArgs...)
	_, err = s.db.ExecContext(ctx, s.rb(`
		UPDATE automations SET name=?, description=?, enabled=?, spec=?,
		    trigger_kind=?, trigger_event=?, cron=?, tz=?, next_run_at=?,
		    trigger_firing=FALSE, updated_at=?
		WHERE id=? AND `+where), args...)
	if err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) SetEnabled(ctx context.Context, id string, scope Scope, enabled bool) (*Automation, error) {
	cur, err := s.Get(ctx, id, scope)
	if err != nil {
		return nil, err
	}
	cur.Enabled = enabled
	cur.UpdatedAt = time.Now().UTC()
	if enabled {
		if nextErr := s.computeNextRun(cur, cur.UpdatedAt); nextErr != nil {
			return nil, nextErr
		}
	} else {
		cur.NextRunAt = nil
	}
	_, err = s.db.ExecContext(ctx, s.rb(`
		UPDATE automations SET enabled=?, next_run_at=?, updated_at=? WHERE id=?`),
		cur.Enabled, cur.NextRunAt, cur.UpdatedAt, id)
	return cur, err
}

func applySpec(a *Automation, spec *Spec) {
	a.Name = spec.Name
	a.Description = spec.Description
	a.TriggerKind = spec.Trigger.Kind()
	a.Event = spec.Trigger.Event
	a.Cron = spec.Trigger.Schedule
	a.TZ = spec.Trigger.TZ
}

// next_run_at is stored, not recomputed per tick, so a restart neither loses nor duplicates a
// firing.
func (s *Store) computeNextRun(a *Automation, from time.Time) error {
	a.NextRunAt = nil
	if !a.Enabled || a.Parsed == nil {
		return nil
	}
	switch a.TriggerKind {
	case TriggerSchedule:
		sched, err := ParseCron(a.Cron, a.TZ)
		if err != nil {
			return err
		}
		next, err := sched.Next(from)
		if err != nil {
			return err
		}
		utc := next.UTC()
		a.NextRunAt = &utc
	case TriggerMetric:
		next := from.Add(a.Parsed.Trigger.Metric.Every.Duration())
		a.NextRunAt = &next
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string, scope Scope) (*Automation, error) {
	where, args := scope.clause()
	row := s.db.QueryRowContext(ctx, s.rb(`
		SELECT `+automationCols+` FROM automations
		WHERE id = ? AND `+where), append([]any{id}, args...)...)
	return scanAutomation(row.Scan)
}

func (s *Store) List(ctx context.Context, scope Scope) ([]Automation, error) {
	where, args := scope.clause()
	q := `SELECT ` + automationCols + ` FROM automations WHERE ` + where + ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Automation
	for rows.Next() {
		a, err := scanAutomation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string, scope Scope) error {
	where, args := scope.clause()
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM automations WHERE id = ? AND `+where), append([]any{id}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM automation_run_steps WHERE run_id IN (SELECT id FROM automation_runs WHERE automation_id = ?)`,
		`DELETE FROM automation_runs WHERE automation_id = ?`,
		`DELETE FROM automation_fingerprints WHERE automation_id = ?`,
		`DELETE FROM automation_debounce WHERE automation_id = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, s.rb(q), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	ids, err := dbpkg.CollectStrings(ctx, s.db,
		`SELECT id FROM automations WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if delErr := s.Delete(ctx, id, InWorkspace(workspaceID)); delErr != nil && !errors.Is(delErr, ErrNotFound) {
			return delErr
		}
	}
	for _, q := range []string{
		`DELETE FROM automation_events WHERE workspace_id = ?`,
		`DELETE FROM automation_task_origins WHERE workspace_id = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, s.rb(q), workspaceID); err != nil {
			return err
		}
	}
	return nil
}

type scanner func(dest ...any) error

func scanAutomation(scan scanner) (*Automation, error) {
	var a Automation
	var nextAt, lastAt sql.NullTime
	err := scan(&a.ID, &a.WorkspaceID, &a.Name, &a.Description, &a.Enabled, &a.Spec,
		&a.TriggerKind, &a.Event, &a.Cron, &a.TZ, &nextAt, &lastAt, &a.LastStatus,
		&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if nextAt.Valid {
		t := nextAt.Time.UTC()
		a.NextRunAt = &t
	}
	if lastAt.Valid {
		t := lastAt.Time.UTC()
		a.LastRunAt = &t
	}
	// An unparseable spec leaves Parsed nil instead of failing, so one bad row cannot break List.
	if spec, err := ParseSpec(a.Spec); err == nil {
		a.Parsed = spec
	}
	return &a, nil
}

// The conditional UPDATE advancing next_run_at is the claim: a concurrent worker matches zero rows
// and skips.
// This is what makes multiple replicas safe.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]Automation, error) {
	candidates, err := s.dueCandidates(ctx, now, limit)
	if err != nil {
		return nil, err
	}

	claimed := make([]Automation, 0, len(candidates))
	for i := range candidates {
		a := candidates[i]
		due := *a.NextRunAt
		if err := s.computeNextRun(&a, now); err != nil {
			// Clear next_run_at so a row whose cron stopped parsing does not spin on every tick.
			a.NextRunAt = nil
		}
		res, execErr := s.db.ExecContext(ctx, s.rb(`
			UPDATE automations SET next_run_at = ?, last_checked_at = ?
			WHERE id = ? AND next_run_at = ?`),
			a.NextRunAt, now.UTC(), a.ID, due)
		if execErr != nil {
			return nil, execErr
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		a.DueAt = due
		claimed = append(claimed, a)
	}
	return claimed, nil
}

// Both the event and the automation must carry the same non-empty workspace; empty on either side
// matches nothing (tenancy boundary).
func (s *Store) ListByEvent(ctx context.Context, kind, workspaceID string) ([]Automation, error) {
	if workspaceID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT `+automationCols+` FROM automations
		WHERE enabled = TRUE AND trigger_event = ? AND workspace_id = ?`),
		kind, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Automation
	for rows.Next() {
		a, err := scanAutomation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) SetFiring(ctx context.Context, id string, firing bool) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE automations SET trigger_firing = ? WHERE id = ?`), firing, id)
	return err
}

func (s *Store) IsFiring(ctx context.Context, id string) (bool, error) {
	var firing bool
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT trigger_firing FROM automations WHERE id = ?`), id).Scan(&firing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return firing, err
}

const runCols = `id, automation_id, workspace_id, status, trigger, reason,
	started_at, finished_at, error, agent_runs, items_emitted, payload, dedupe_key,
	reason_i18n, error_i18n`

func (s *Store) StartRun(ctx context.Context, a *Automation, f Firing, payload string) (*Run, error) {
	r := &Run{
		ID:           generateID(),
		AutomationID: a.ID,
		WorkspaceID:  a.WorkspaceID,
		Status:       RunRunning,
		Trigger:      f.Trigger,
		DedupeKey:    f.DedupeKey,
		StartedAt:    time.Now().UTC(),
		Payload:      payload,
	}
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		r.ID, r.AutomationID, r.WorkspaceID, r.Status, r.Trigger, "",
		r.StartedAt, nil, "", 0, 0, r.Payload, r.DedupeKey, "", "")
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`UPDATE automations SET last_run_at = ?, last_status = ? WHERE id = ?`),
		r.StartedAt, RunRunning, a.ID)
	return r, err
}

func (s *Store) RecordSkip(ctx context.Context, a *Automation, trigger string, reason Msg) (*Run, error) {
	now := time.Now().UTC()
	r := &Run{
		ID: generateID(), AutomationID: a.ID, WorkspaceID: a.WorkspaceID,
		Status: RunSkipped, Trigger: trigger, Reason: reason.Text(), ReasonMsg: reason,
		StartedAt: now, FinishedAt: &now,
	}
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		r.ID, r.AutomationID, r.WorkspaceID, r.Status, r.Trigger, r.Reason,
		r.StartedAt, r.FinishedAt, "", 0, 0, "", "", reason.encode(), "")
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`UPDATE automations SET last_run_at = ?, last_status = ? WHERE id = ?`), now, RunSkipped, a.ID)
	return r, err
}

func (s *Store) FinishRun(ctx context.Context, r *Run) error {
	now := time.Now().UTC()
	r.FinishedAt = &now
	if r.ReasonMsg != nil {
		r.Reason = r.ReasonMsg.Text()
	}
	if r.ErrorMsg != nil {
		r.Error = r.ErrorMsg.Text()
	}
	_, err := s.db.ExecContext(ctx, s.rb(`
		UPDATE automation_runs SET status=?, error=?, agent_runs=?, items_emitted=?, finished_at=?, reason=?,
		    reason_i18n=?, error_i18n=?
		WHERE id=?`),
		r.Status, r.Error, r.AgentRuns, r.ItemsEmitted, now, r.Reason,
		r.ReasonMsg.encode(), r.ErrorMsg.encode(), r.ID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`UPDATE automations SET last_status = ?, last_run_at = ? WHERE id = ?`),
		r.Status, r.StartedAt, r.AutomationID)
	return err
}

func (s *Store) RunningCount(ctx context.Context, automationID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM automation_runs WHERE automation_id = ? AND status = ?`),
		automationID, RunRunning).Scan(&n)
	return n, err
}

func (s *Store) RunningRuns(ctx context.Context, automationID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT id FROM automation_runs WHERE automation_id = ? AND status = 'running'`, automationID)
}

// Durable record only: the engine stops the run through its own cancel channel.
func (s *Store) CancelRun(ctx context.Context, runID string) error {
	now := time.Now().UTC()
	reason := msgText("automation.runs.replaced")
	_, err := s.db.ExecContext(ctx, s.rb(`
		UPDATE automation_runs SET status=?, finished_at=?, reason=?, reason_i18n=?
		WHERE id=? AND status=?`), RunCanceled, now, reason.Text(), reason.encode(), runID, RunRunning)
	return err
}

func (s *Store) AgentRunsSince(ctx context.Context, automationID string, since time.Time) (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.rb(`
		SELECT sum(agent_runs) FROM automation_runs
		WHERE automation_id = ? AND started_at >= ?`), automationID, since.UTC()).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

func (s *Store) ListRuns(ctx context.Context, automationID string, scope Scope, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where, args := scope.clause()
	args = append(append([]any{automationID}, args...), limit)
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT `+runCols+` FROM automation_runs
		WHERE automation_id = ? AND `+where+`
		ORDER BY started_at DESC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) GetRun(ctx context.Context, runID string, scope Scope) (*Run, error) {
	where, args := scope.clause()
	row := s.db.QueryRowContext(ctx, s.rb(`
		SELECT `+runCols+` FROM automation_runs WHERE id = ? AND `+where),
		append([]any{runID}, args...)...)
	r, err := scanRun(row.Scan)
	if err != nil {
		return nil, err
	}
	steps, err := s.listRunSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	r.Steps = steps
	return r, nil
}

func scanRun(scan scanner) (*Run, error) {
	var r Run
	var finished sql.NullTime
	var reasonI18n, errorI18n string
	err := scan(&r.ID, &r.AutomationID, &r.WorkspaceID, &r.Status, &r.Trigger, &r.Reason,
		&r.StartedAt, &finished, &r.Error, &r.AgentRuns, &r.ItemsEmitted, &r.Payload, &r.DedupeKey,
		&reasonI18n, &errorI18n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if finished.Valid {
		t := finished.Time.UTC()
		r.FinishedAt = &t
	}
	r.ReasonMsg = decodeMsg(reasonI18n)
	r.ErrorMsg = decodeMsg(errorI18n)
	return &r, nil
}

func (s *Store) listRunSteps(ctx context.Context, runID string) ([]RunStep, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT id, run_id, seq, step_id, kind, status, started_at, finished_at, output, error,
		       output_i18n, error_i18n
		FROM automation_run_steps WHERE run_id = ? ORDER BY seq`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunStep
	for rows.Next() {
		var st RunStep
		var finished sql.NullTime
		var outputI18n, errorI18n string
		if err := rows.Scan(&st.ID, &st.RunID, &st.Seq, &st.StepID, &st.Kind, &st.Status,
			&st.StartedAt, &finished, &st.Output, &st.Error, &outputI18n, &errorI18n); err != nil {
			return nil, err
		}
		st.OutputMsg = decodeMsg(outputI18n)
		st.ErrorMsg = decodeMsg(errorI18n)
		if finished.Valid {
			t := finished.Time.UTC()
			st.FinishedAt = &t
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) RecordStep(ctx context.Context, st *RunStep) error {
	if st.ID == "" {
		st.ID = generateID()
	}
	if st.OutputMsg != nil {
		st.Output = st.OutputMsg.Text()
	}
	if st.ErrorMsg != nil {
		st.Error = st.ErrorMsg.Text()
	}
	if len(st.Output) > MaxStepOutput {
		st.Output = st.Output[:MaxStepOutput] + "\n… truncated"
	}
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_run_steps (id, run_id, seq, step_id, kind, status, started_at, finished_at, output, error,
		                                  output_i18n, error_i18n)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
		st.ID, st.RunID, st.Seq, st.StepID, st.Kind, st.Status, st.StartedAt, st.FinishedAt, st.Output, st.Error,
		st.OutputMsg.encode(), st.ErrorMsg.encode())
	return err
}

func (s *Store) TrimHistory(ctx context.Context, automationID string, keep int) error {
	if keep <= 0 {
		keep = 200
	}
	ids, err := dbpkg.CollectStrings(ctx, s.db, `
		SELECT id FROM automation_runs WHERE automation_id = ?
		ORDER BY started_at DESC OFFSET ?`, automationID, keep)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM automation_run_steps WHERE run_id = ?`), id); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM automation_runs WHERE id = ?`), id); err != nil {
			return err
		}
	}
	return nil
}

// Must run at startup: a ghost `running` row would hold the concurrency slot forever.
func (s *Store) ReconcileRunning(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	msg := msgText("automation.runs.daemon_restarted")
	res, err := s.db.ExecContext(ctx, s.rb(`
		UPDATE automation_runs SET status = ?, finished_at = ?, error = ?, error_i18n = ?
		WHERE status = ?`), RunFailed, now, msg.Text(), msg.encode(), RunRunning)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// The insert makes the check atomic: two concurrent runs cannot both claim the fingerprint.
func (s *Store) ClaimFingerprint(
	ctx context.Context, automationID, workspaceID, fingerprint, taskID string,
) (existing string, claimed bool, err error) {
	res, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_fingerprints (automation_id, fingerprint, workspace_id, task_id, created_at)
		VALUES (?,?,?,?,?) ON CONFLICT (automation_id, fingerprint) DO NOTHING`),
		automationID, fingerprint, workspaceID, taskID, time.Now().UTC())
	if err != nil {
		return "", false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return "", true, nil
	}
	err = s.db.QueryRowContext(ctx, s.rb(`
		SELECT task_id FROM automation_fingerprints WHERE automation_id = ? AND fingerprint = ?`),
		automationID, fingerprint).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		// Raced with a release; report unclaimed so the caller retries.
		return "", false, nil
	}
	return existing, false, err
}

func (s *Store) ReleaseFingerprint(ctx context.Context, automationID, fingerprint string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM automation_fingerprints WHERE automation_id = ? AND fingerprint = ?`),
		automationID, fingerprint)
	return err
}

func (s *Store) PointFingerprint(ctx context.Context, automationID, fingerprint, taskID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE automation_fingerprints SET task_id = ? WHERE automation_id = ? AND fingerprint = ?`),
		taskID, automationID, fingerprint)
	return err
}

// Not deleted with the recipe: the task stays on the board and its events must still count toward
// the chain.
func (s *Store) RecordTaskOrigin(ctx context.Context, taskID, automationID, workspaceID string, depth int) error {
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_task_origins (task_id, automation_id, workspace_id, depth, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (task_id) DO NOTHING`),
		taskID, automationID, workspaceID, depth, time.Now().UTC())
	return err
}

func (s *Store) TaskOrigin(ctx context.Context, taskID string) (automationID string, depth int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT automation_id, depth FROM automation_task_origins WHERE task_id = ?`), taskID).
		Scan(&automationID, &depth)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return automationID, depth, true, nil
}

// Persist before matching so a delivery is not lost when matching outlasts GitHub's webhook
// timeout.
func (s *Store) PublishEvent(ctx context.Context, e Event) (string, error) {
	if e.ID == "" {
		e.ID = generateID()
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_events (id, workspace_id, kind, payload, dedupe_key, created_at)
		VALUES (?,?,?,?,?,?)`),
		e.ID, e.WorkspaceID, e.Kind, string(payload), e.DedupeKey, time.Now().UTC())
	return e.ID, err
}

// Kept apart from ClaimDue so the cursor is closed before the per-row claim UPDATEs; otherwise it
// pins a connection for the whole batch.
func (s *Store) dueCandidates(ctx context.Context, now time.Time, limit int) ([]Automation, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT `+automationCols+` FROM automations
		WHERE enabled = TRUE AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at
		LIMIT ?`), now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Automation
	for rows.Next() {
		a, scanErr := scanAutomation(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) unprocessedEvents(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT id, workspace_id, kind, payload, dedupe_key FROM automation_events
		WHERE processed_at IS NULL ORDER BY created_at LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload string
		if scanErr := rows.Scan(&e.ID, &e.WorkspaceID, &e.Kind, &payload, &e.DedupeKey); scanErr != nil {
			return nil, scanErr
		}
		if json.Unmarshal([]byte(payload), &e.Payload) != nil {
			e.Payload = map[string]any{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ClaimEvents(ctx context.Context, limit int) ([]Event, error) {
	candidates, err := s.unprocessedEvents(ctx, limit)
	if err != nil {
		return nil, err
	}

	claimed := make([]Event, 0, len(candidates))
	now := time.Now().UTC()
	for _, e := range candidates {
		res, err := s.db.ExecContext(ctx, s.rb(`
			UPDATE automation_events SET processed_at = ? WHERE id = ? AND processed_at IS NULL`), now, e.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			claimed = append(claimed, e)
		}
	}
	return claimed, nil
}

// Bounds row growth under a webhook burst; event_count keeps counting past it.
const MaxDebouncedEvents = 50

// The window runs from the first event and later events do not extend it, so a busy PR still wakes
// its agent every `debounce`.
func (s *Store) BufferEvent(ctx context.Context, a *Automation, e Event, window time.Duration) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_debounce
		    (automation_id, dedupe_key, workspace_id, payload, events, event_count, first_at, fire_at)
		VALUES (?, ?, ?, ?::jsonb, jsonb_build_array(?::jsonb), 1, ?, ?)
		ON CONFLICT (automation_id, dedupe_key) DO UPDATE SET
		    payload = EXCLUDED.payload,
		    events = CASE WHEN jsonb_array_length(automation_debounce.events) < ?
		                  THEN automation_debounce.events || EXCLUDED.events
		                  ELSE automation_debounce.events END,
		    event_count = automation_debounce.event_count + 1`),
		a.ID, e.DedupeKey, a.WorkspaceID, string(payload), string(payload), now, now.Add(window),
		MaxDebouncedEvents)
	return err
}

type Debounced struct {
	AutomationID string
	WorkspaceID  string
	DedupeKey    string
	Latest       map[string]any
	Events       []any
	Count        int
	FirstAt      time.Time
}

func (d Debounced) TriggerPayload() map[string]any {
	out := make(map[string]any, len(d.Latest)+2)
	for k, v := range d.Latest {
		out[k] = v
	}
	out["events"] = d.Events
	out["event_count"] = float64(d.Count)
	return out
}

// The DELETE is the claim (SKIP LOCKED): two schedulers cannot fire the same window.
func (s *Store) ClaimDebounced(ctx context.Context, now time.Time, limit int) ([]Debounced, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		DELETE FROM automation_debounce
		WHERE (automation_id, dedupe_key) IN (
		    SELECT automation_id, dedupe_key FROM automation_debounce
		    WHERE fire_at <= ? ORDER BY fire_at LIMIT ? FOR UPDATE SKIP LOCKED)
		RETURNING automation_id, workspace_id, dedupe_key, payload, events, event_count, first_at`),
		now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Debounced
	for rows.Next() {
		var d Debounced
		var payload, events []byte
		if err := rows.Scan(&d.AutomationID, &d.WorkspaceID, &d.DedupeKey, &payload, &events, &d.Count, &d.FirstAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(payload, &d.Latest) != nil || d.Latest == nil {
			d.Latest = map[string]any{}
		}
		if json.Unmarshal(events, &d.Events) != nil {
			d.Events = nil
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RequeueDebounced(ctx context.Context, d Debounced, fireAt time.Time) error {
	payload, err := json.Marshal(d.Latest)
	if err != nil {
		return err
	}
	events, err := json.Marshal(d.Events)
	if err != nil {
		return err
	}
	if d.Events == nil {
		events = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO automation_debounce
		    (automation_id, dedupe_key, workspace_id, payload, events, event_count, first_at, fire_at)
		VALUES (?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?)
		ON CONFLICT (automation_id, dedupe_key) DO UPDATE SET
		    events = (SELECT COALESCE(jsonb_agg(e ORDER BY i), '[]'::jsonb)
		              FROM jsonb_array_elements(EXCLUDED.events || automation_debounce.events)
		                   WITH ORDINALITY AS x(e, i)
		              WHERE i <= ?),
		    event_count = EXCLUDED.event_count + automation_debounce.event_count,
		    first_at = LEAST(EXCLUDED.first_at, automation_debounce.first_at),
		    fire_at = LEAST(EXCLUDED.fire_at, automation_debounce.fire_at)`),
		d.AutomationID, d.DedupeKey, d.WorkspaceID, string(payload), string(events), d.Count,
		d.FirstAt.UTC(), fireAt.UTC(), MaxDebouncedEvents)
	return err
}

func (s *Store) PurgeEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM automation_events WHERE processed_at IS NOT NULL AND created_at < ?`), cutoff.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
