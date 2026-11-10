package evidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

// Recorded in schema_migrations. Never change it.
const MigrationComponent = "evidence"

// internal/secretbox lists this column so key rotation rewraps it.
const LabelSecrets = secretbox.LabelEvidenceSecrets

const migration1 = `
			CREATE TABLE IF NOT EXISTS evidence_environments (
				id                TEXT PRIMARY KEY,
				workspace_id      TEXT NOT NULL,
				name              TEXT NOT NULL,
				base_url          TEXT NOT NULL,
				allowed_hosts     JSONB NOT NULL DEFAULT '[]',
				secrets_sealed    TEXT NOT NULL DEFAULT '',
				secret_names      JSONB NOT NULL DEFAULT '[]',
				schedule          TEXT NOT NULL DEFAULT '',
				schedule_tz       TEXT NOT NULL DEFAULT 'UTC',
				deploy_repo       TEXT NOT NULL DEFAULT '',
				deploy_match      TEXT NOT NULL DEFAULT '',
				last_scheduled_at TIMESTAMPTZ,
				created_by        TEXT NOT NULL DEFAULT '',
				created_at        TIMESTAMPTZ NOT NULL,
				updated_at        TIMESTAMPTZ NOT NULL,
				UNIQUE (workspace_id, name)
			);
			CREATE INDEX IF NOT EXISTS idx_evidence_env_repo ON evidence_environments(deploy_repo) WHERE deploy_repo <> '';

			CREATE TABLE IF NOT EXISTS evidence_cases (
				id              TEXT PRIMARY KEY,
				workspace_id    TEXT NOT NULL,
				project_id      TEXT NOT NULL DEFAULT '',
				key             TEXT NOT NULL,
				title           TEXT NOT NULL,
				description     TEXT NOT NULL DEFAULT '',
				steps           TEXT NOT NULL DEFAULT '',
				expected        TEXT NOT NULL DEFAULT '',
				priority        TEXT NOT NULL DEFAULT 'medium',
				tags            JSONB NOT NULL DEFAULT '[]',
				take            JSONB,
				take_source     TEXT NOT NULL DEFAULT '',
				take_updated_at TIMESTAMPTZ,
				source_task_id  TEXT NOT NULL DEFAULT '',
				source_case_key TEXT NOT NULL DEFAULT '',
				status          TEXT NOT NULL DEFAULT 'draft',
				last_run_at     TIMESTAMPTZ,
				created_by      TEXT NOT NULL DEFAULT '',
				updated_by      TEXT NOT NULL DEFAULT '',
				created_at      TIMESTAMPTZ NOT NULL,
				updated_at      TIMESTAMPTZ NOT NULL,
				UNIQUE (workspace_id, key)
			);

			CREATE TABLE IF NOT EXISTS evidence_batches (
				id             TEXT PRIMARY KEY,
				workspace_id   TEXT NOT NULL,
				environment_id TEXT NOT NULL REFERENCES evidence_environments(id) ON DELETE CASCADE,
				trigger        TEXT NOT NULL,
				trigger_ref    TEXT NOT NULL DEFAULT '',
				status         TEXT NOT NULL DEFAULT 'queued',
				total          INTEGER NOT NULL DEFAULT 0,
				passed         INTEGER NOT NULL DEFAULT 0,
				failed         INTEGER NOT NULL DEFAULT 0,
				blocked        INTEGER NOT NULL DEFAULT 0,
				errored        INTEGER NOT NULL DEFAULT 0,
				canceled       INTEGER NOT NULL DEFAULT 0,
				runner         TEXT NOT NULL DEFAULT '',
				error          TEXT NOT NULL DEFAULT '',
				created_by     TEXT NOT NULL DEFAULT '',
				created_at     TIMESTAMPTZ NOT NULL,
				started_at     TIMESTAMPTZ,
				finished_at    TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_evidence_batches_ws ON evidence_batches(workspace_id, created_at DESC);
			CREATE INDEX IF NOT EXISTS idx_evidence_batches_open ON evidence_batches(status, created_at) WHERE status IN ('queued', 'running');

			CREATE TABLE IF NOT EXISTS evidence_runs (
				id             TEXT PRIMARY KEY,
				workspace_id   TEXT NOT NULL,
				batch_id       TEXT NOT NULL REFERENCES evidence_batches(id) ON DELETE CASCADE,
				case_id        TEXT NOT NULL REFERENCES evidence_cases(id) ON DELETE CASCADE,
				environment_id TEXT NOT NULL,
				position       INTEGER NOT NULL DEFAULT 0,
				status         TEXT NOT NULL DEFAULT 'queued',
				checks         JSONB NOT NULL DEFAULT '[]',
				failures       JSONB NOT NULL DEFAULT '[]',
				error          TEXT NOT NULL DEFAULT '',
				duration_ms    BIGINT NOT NULL DEFAULT 0,
				take           JSONB,
				report_md      TEXT NOT NULL DEFAULT '',
				video_id       TEXT NOT NULL DEFAULT '',
				take_id        TEXT NOT NULL DEFAULT '',
				chapters_id    TEXT NOT NULL DEFAULT '',
				poster_id      TEXT NOT NULL DEFAULT '',
				shot_ids       JSONB NOT NULL DEFAULT '[]',
				created_at     TIMESTAMPTZ NOT NULL,
				started_at     TIMESTAMPTZ,
				finished_at    TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_evidence_runs_batch ON evidence_runs(batch_id, position);
			CREATE INDEX IF NOT EXISTS idx_evidence_runs_case ON evidence_runs(case_id, environment_id, created_at DESC);
			CREATE INDEX IF NOT EXISTS idx_evidence_runs_ws ON evidence_runs(workspace_id, created_at DESC);

			CREATE TABLE IF NOT EXISTS evidence_artifacts (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				run_id       TEXT NOT NULL REFERENCES evidence_runs(id) ON DELETE CASCADE,
				kind         TEXT NOT NULL,
				filename     TEXT NOT NULL,
				mime_type    TEXT NOT NULL DEFAULT '',
				size         BIGINT NOT NULL DEFAULT 0,
				path         TEXT NOT NULL,
				created_at   TIMESTAMPTZ NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_evidence_artifacts_run ON evidence_artifacts(run_id);
			CREATE INDEX IF NOT EXISTS idx_evidence_artifacts_ws ON evidence_artifacts(workspace_id);

			CREATE TABLE IF NOT EXISTS evidence_recordings (
				id             TEXT PRIMARY KEY,
				workspace_id   TEXT NOT NULL,
				case_id        TEXT NOT NULL REFERENCES evidence_cases(id) ON DELETE CASCADE,
				environment_id TEXT NOT NULL REFERENCES evidence_environments(id) ON DELETE CASCADE,
				chat_id        TEXT NOT NULL,
				status         TEXT NOT NULL DEFAULT 'open',
				created_by     TEXT NOT NULL DEFAULT '',
				created_at     TIMESTAMPTZ NOT NULL,
				saved_at       TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_evidence_recordings_chat ON evidence_recordings(chat_id, status);`

// Expand only: existing rows default to standalone; deploy_repo/deploy_match stay for the previous
// build.
// Reads projects and project_environments, so Evidence must migrate after project.
//
//i18n:ignore SQL migration
const migration2 = `
			ALTER TABLE evidence_environments
				ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'standalone',
				ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS project_environment_id TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS is_production BOOLEAN NOT NULL DEFAULT FALSE,
				ADD COLUMN IF NOT EXISTS on_deploy BOOLEAN NOT NULL DEFAULT FALSE;
			ALTER TABLE evidence_environments DROP CONSTRAINT IF EXISTS evidence_environments_workspace_id_name_key;
			CREATE UNIQUE INDEX IF NOT EXISTS uq_evidence_env_standalone_name
				ON evidence_environments(workspace_id, name) WHERE kind = 'standalone';
			CREATE UNIQUE INDEX IF NOT EXISTS uq_evidence_env_project_env
				ON evidence_environments(project_environment_id) WHERE kind = 'project';
			CREATE UNIQUE INDEX IF NOT EXISTS uq_evidence_env_sandbox
				ON evidence_environments(project_id) WHERE kind = 'sandbox';
			CREATE INDEX IF NOT EXISTS idx_evidence_env_project ON evidence_environments(workspace_id, project_id);

			ALTER TABLE evidence_batches
				ADD COLUMN IF NOT EXISTS base_url TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS source_branch TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS source_commit TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_status TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_phase TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_error TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_reason TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_reason_args TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS sandbox_log TEXT NOT NULL DEFAULT '';

			INSERT INTO evidence_environments (id, workspace_id, kind, project_id, project_environment_id, name,
				base_url, created_by, created_at, updated_at)
			SELECT substr(md5(random()::text || pe.id), 1, 16), p.workspace_id, 'project', pe.project_id, pe.id,
				pe.name, '', 'migration', NOW(), NOW()
			FROM project_environments pe JOIN projects p ON p.id = pe.project_id
			WHERE p.workspace_id <> ''
			ON CONFLICT DO NOTHING;
			INSERT INTO evidence_environments (id, workspace_id, kind, project_id, project_environment_id, name,
				base_url, created_by, created_at, updated_at)
			SELECT substr(md5(random()::text || p.id), 1, 16), p.workspace_id, 'sandbox', p.id, '',
				'sandbox', '', 'migration', NOW(), NOW()
			FROM projects p
			WHERE p.workspace_id <> ''
			ON CONFLICT DO NOTHING;`

// A published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: migration1},
		{ID: 2, SQL: migration2, Contract: &migrate.Contract{
			StoppedUsingIn: "0.3.2",
			Reason:         "evidence tables are new in 0.3.3: no released binary reads evidence_environments", //i18n:ignore migration contract note
		}},
		{ID: 3, SQL: `ALTER TABLE evidence_environments
			ADD COLUMN IF NOT EXISTS schedule_project_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS schedule_tag TEXT NOT NULL DEFAULT ''`},
		{ID: 4, SQL: `ALTER TABLE evidence_batches ADD COLUMN IF NOT EXISTS locale TEXT NOT NULL DEFAULT ''`},
	}, nil
}

// Owns no files: callers delete the paths a delete returns.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

type rowScanner interface{ Scan(dest ...any) error }

func notFound(err error, key string, args ...any) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound(key, args...)
	}
	return err
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// Older daemons left rows with a take as 'draft' (a take saved again after removal).
//
//i18n:ignore SQL fragment
const caseStatus = `CASE WHEN c.take IS NOT NULL AND c.status = 'draft' THEN 'ready' ELSE c.status END`

//i18n:ignore SQL column list
const caseCols = `c.id, c.workspace_id, c.project_id, c.key, c.title, c.description, c.steps, c.expected,
	c.priority, c.tags::text, c.take IS NOT NULL, c.take_source, c.take_updated_at, c.source_task_id,
	c.source_case_key, ` + caseStatus + `, c.last_run_at, c.created_by, c.updated_by, c.created_at, c.updated_at`

func scanCase(r rowScanner, extra ...any) (Case, error) {
	var c Case
	var tags string
	var takeAt, lastRun sql.NullTime
	dest := append([]any{&c.ID, &c.WorkspaceID, &c.ProjectID, &c.Key, &c.Title, &c.Description, &c.Steps,
		&c.Expected, &c.Priority, &tags, &c.HasTake, &c.TakeSource, &takeAt, &c.SourceTaskID,
		&c.SourceCaseKey, &c.Status, &lastRun, &c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil {
		return c, err
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	c.TakeUpdatedAt, c.LastRunAt = timePtr(takeAt), timePtr(lastRun)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

func (s *Store) lockWorkspace(ctx context.Context, tx *sql.Tx, ws string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "evidence:"+ws)
	return err
}

// Call under lockWorkspace.
func (s *Store) nextKey(ctx context.Context, tx *sql.Tx, ws string) (string, error) {
	var n sql.NullInt64
	err := tx.QueryRowContext(ctx, s.rb(`
		SELECT MAX(CAST(SUBSTRING(key FROM 4) AS INTEGER)) FROM evidence_cases
		WHERE workspace_id = ? AND key ~ '^EV-[0-9]+$'`), ws).Scan(&n)
	if err != nil {
		return "", apperr.Wrap(err, "evidence.errors.next_key")
	}
	return "EV-" + strconv.FormatInt(n.Int64+1, 10), nil
}

// Take, when present, must have passed ValidateTake.
func (s *Store) CreateCase(ctx context.Context, ws, author string, in Case) (*Case, error) {
	title, err := normalizeTitle(in.Title)
	if err != nil {
		return nil, err
	}
	tags, err := normalizeTags(in.Tags)
	if err != nil {
		return nil, err
	}
	prio := in.Priority
	if prio == "" {
		prio = PriorityMedium
	}
	if !ValidPriority(prio) {
		return nil, apperr.Invalid("evidence.errors.priority_invalid", "priority", prio)
	}
	now := time.Now().UTC()
	c := Case{
		ID: newID(), WorkspaceID: ws, ProjectID: strings.TrimSpace(in.ProjectID), Title: title,
		Description: in.Description, Steps: in.Steps, Expected: in.Expected, Priority: prio, Tags: tags,
		SourceTaskID: in.SourceTaskID, SourceCaseKey: in.SourceCaseKey, Status: CaseDraft,
		CreatedBy: author, UpdatedBy: author, CreatedAt: now, UpdatedAt: now,
	}
	var take any
	if len(in.Take) > 0 {
		take, c.HasTake, c.TakeSource, c.TakeUpdatedAt, c.Status = string(in.Take), true, in.TakeSource, &now, CaseReady
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.begin")
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.lockWorkspace(ctx, tx, ws); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.lock")
	}
	if c.Key, err = s.nextKey(ctx, tx, ws); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, s.rb(`INSERT INTO evidence_cases (id, workspace_id, project_id, key, title,
		description, steps, expected, priority, tags, take, take_source, take_updated_at, source_task_id,
		source_case_key, status, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,CAST(? AS jsonb),CAST(? AS jsonb),?,?,?,?,?,?,?,?,?)`),
		c.ID, ws, c.ProjectID, c.Key, c.Title, c.Description, c.Steps, c.Expected, c.Priority, jsonText(c.Tags),
		take, c.TakeSource, c.TakeUpdatedAt, c.SourceTaskID, c.SourceCaseKey, c.Status, author, author, now, now)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_case")
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.commit")
	}
	return &c, nil
}

const caseRef = `c.workspace_id = ? AND (c.id = ? OR c.key = ?)`

func (s *Store) GetCase(ctx context.Context, ws, ref string, withTake bool) (*Case, error) {
	var take sql.NullString
	//i18n:ignore SQL fragment
	c, err := scanCase(s.db.QueryRowContext(ctx, s.rb(`SELECT `+caseCols+`, c.take::text FROM evidence_cases c WHERE `+caseRef),
		ws, ref, ref), &take)
	if err != nil {
		return nil, notFound(err, "evidence.errors.case_not_found", "ref", ref)
	}
	if withTake && take.Valid {
		c.Take = json.RawMessage(take.String)
	}
	return &c, nil
}

func (s *Store) ListCases(ctx context.Context, f CaseFilter) ([]Case, map[string]int, error) {
	if f.EnvironmentID != "" {
		return s.listCasesOn(ctx, f)
	}
	base, args := caseWhere(f)
	counts, err := s.caseCounts(ctx, base, args)
	if err != nil {
		return nil, nil, err
	}
	if len(f.Statuses) > 0 && f.EnvironmentID == "" {
		base += " AND " + caseStatus + " IN (" + placeholders(len(f.Statuses)) + ")"
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	out, err := s.queryCases(ctx, base, args)
	if err != nil || len(out) == 0 {
		return out, counts, err
	}
	if latestErr := s.attachLatest(ctx, f.WorkspaceID, out); latestErr != nil {
		return nil, nil, latestErr
	}
	return out, counts, nil
}

func (s *Store) listCasesOn(ctx context.Context, f CaseFilter) ([]Case, map[string]int, error) {
	env, err := s.GetEnvironment(ctx, f.WorkspaceID, f.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	base, args := caseWhere(f)
	all, err := s.queryCases(ctx, base, args)
	if err != nil {
		return nil, nil, err
	}
	eligible := make([]Case, 0, len(all))
	for i := range all {
		if CaseRunsOn(all[i].ProjectID, *env) {
			eligible = append(eligible, all[i])
		}
	}
	counts := map[string]int{}
	if len(eligible) == 0 {
		return eligible, counts, nil
	}
	if latestErr := s.attachLatest(ctx, f.WorkspaceID, eligible); latestErr != nil {
		return nil, nil, latestErr
	}
	out := make([]Case, 0, len(eligible))
	for i := range eligible {
		st := NotRunHere
		if b, ok := eligible[i].Latest[env.ID]; ok {
			st = b.Status
		}
		counts[st]++
		if len(f.Statuses) == 0 || containsStr(f.Statuses, st) {
			out = append(out, eligible[i])
		}
	}
	return out, counts, nil
}

func caseWhere(f CaseFilter) (string, []any) {
	where := []string{"c.workspace_id = ?"}
	args := []any{f.WorkspaceID}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q)) + "%"
		where = append(where, "(LOWER(c.title) LIKE ? OR LOWER(c.key) LIKE ? OR LOWER(c.description) LIKE ?)")
		args = append(args, like, like, like)
	}
	if cond, cargs := projectCond("c.project_id", f.ProjectID, f.IncludeUnassigned); cond != "" {
		where = append(where, cond)
		args = append(args, cargs...)
	}
	if tags := filterTags(f); len(tags) > 0 {
		where = append(where, "c.tags @> CAST(? AS jsonb)")
		args = append(args, jsonText(tags))
	}
	if len(f.Priorities) > 0 {
		//i18n:ignore SQL fragment
		where = append(where, "c.priority IN ("+placeholders(len(f.Priorities))+")")
		for _, p := range f.Priorities {
			args = append(args, p)
		}
	}
	return strings.Join(where, " AND "), args
}

func filterTags(f CaseFilter) []string {
	var out []string
	for _, tag := range append([]string{f.Tag}, f.Tags...) {
		if tag = strings.TrimSpace(tag); tag != "" && !containsStr(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

func (s *Store) CaseTags(ctx context.Context, ws, projectID string, withUnassigned bool) ([]string, error) {
	where, args := "c.workspace_id = ?", []any{ws}
	if cond, cargs := projectCond("c.project_id", projectID, withUnassigned); cond != "" {
		where += " AND " + cond
		args = append(args, cargs...)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT DISTINCT t FROM evidence_cases c,
		jsonb_array_elements_text(c.tags) AS t WHERE `+where+` ORDER BY t`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_cases")
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var tag string
		if scanErr := rows.Scan(&tag); scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_cases")
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func projectCond(col, projectID string, withUnassigned bool) (string, []any) {
	switch {
	case projectID == "":
		return "", nil
	case projectID == ProjectNone:
		return col + " = ''", nil
	case withUnassigned:
		return col + " IN ('', ?)", []any{projectID}
	}
	return col + " = ?", []any{projectID}
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func (s *Store) caseCounts(ctx context.Context, base string, args []any) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+caseStatus+`, COUNT(*) FROM evidence_cases c WHERE `+base+` GROUP BY 1`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_cases")
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.list_cases")
		}
		counts[st] = n
	}
	return counts, rows.Err()
}

func (s *Store) queryCases(ctx context.Context, base string, args []any) ([]Case, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+caseCols+` FROM evidence_cases c WHERE `+base+
		` ORDER BY CAST(SUBSTRING(c.key FROM '[0-9]+$') AS INTEGER) DESC NULLS LAST, c.created_at DESC`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_cases")
	}
	defer rows.Close()
	out := []Case{}
	for rows.Next() {
		c, scanErr := scanCase(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "evidence.errors.list_cases")
		}
		c.Latest = map[string]RunBrief{}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) attachLatest(ctx context.Context, ws string, cases []Case) error {
	index := make(map[string]int, len(cases))
	for i := range cases {
		index[cases[i].ID] = i
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT DISTINCT ON (case_id, environment_id) case_id, environment_id, id, status, finished_at, poster_id
		FROM evidence_runs WHERE workspace_id = ? AND status <> 'canceled'
		ORDER BY case_id, environment_id, created_at DESC`), ws)
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.list_cases")
	}
	defer rows.Close()
	for rows.Next() {
		var caseID, envID string
		var b RunBrief
		var fin sql.NullTime
		if err := rows.Scan(&caseID, &envID, &b.RunID, &b.Status, &fin, &b.PosterID); err != nil {
			return apperr.Wrap(err, "evidence.errors.list_cases")
		}
		b.FinishedAt = timePtr(fin)
		if i, ok := index[caseID]; ok {
			cases[i].Latest[envID] = b
		}
	}
	return rows.Err()
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Store) UpdateCase(ctx context.Context, ws, ref, author string, p CasePatch) (*Case, error) {
	c, err := s.GetCase(ctx, ws, ref, false)
	if err != nil {
		return nil, err
	}
	if p.Title != nil {
		if c.Title, err = normalizeTitle(*p.Title); err != nil {
			return nil, err
		}
	}
	if p.Description != nil {
		c.Description = *p.Description
	}
	if p.Steps != nil {
		c.Steps = *p.Steps
	}
	if p.Expected != nil {
		c.Expected = *p.Expected
	}
	if p.Priority != nil {
		if !ValidPriority(*p.Priority) {
			return nil, apperr.Invalid("evidence.errors.priority_invalid", "priority", *p.Priority)
		}
		c.Priority = *p.Priority
	}
	if p.Tags != nil {
		if c.Tags, err = normalizeTags(*p.Tags); err != nil {
			return nil, err
		}
	}
	if p.ProjectID != nil {
		c.ProjectID = strings.TrimSpace(*p.ProjectID)
	}
	c.UpdatedBy, c.UpdatedAt = author, time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases SET title = ?, description = ?, steps = ?,
		expected = ?, priority = ?, tags = CAST(? AS jsonb), project_id = ?, updated_by = ?, updated_at = ?
		WHERE id = ?`), c.Title, c.Description, c.Steps, c.Expected, c.Priority, jsonText(c.Tags), c.ProjectID,
		author, c.UpdatedAt, c.ID)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_case")
	}
	return c, nil
}

func (s *Store) SetTake(ctx context.Context, ws, ref, author string, take json.RawMessage, source string) (*Case, error) {
	c, err := s.GetCase(ctx, ws, ref, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if take == nil {
		_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases SET take = NULL, take_source = '',
			take_updated_at = NULL, status = 'draft', updated_by = ?, updated_at = ? WHERE id = ?`), author, now, c.ID)
	} else {
		_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases SET take = CAST(? AS jsonb), take_source = ?,
			take_updated_at = ?, status = CASE WHEN last_run_at IS NULL OR status = 'draft' THEN 'ready' ELSE status END,
			updated_by = ?, updated_at = ? WHERE id = ?`), string(take), source, now, author, now, c.ID)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_case")
	}
	return s.GetCase(ctx, ws, c.ID, true)
}

func (s *Store) DeleteCase(ctx context.Context, ws, ref string) (*Case, []string, error) {
	c, err := s.GetCase(ctx, ws, ref, false)
	if err != nil {
		return nil, nil, err
	}
	paths, err := s.artifactPaths(ctx, `a.run_id IN (SELECT id FROM evidence_runs WHERE case_id = ?)`, c.ID)
	if err != nil {
		return nil, nil, err
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM evidence_cases WHERE id = ?`), c.ID); err != nil {
		return nil, nil, apperr.Wrap(err, "evidence.errors.delete_case")
	}
	return c, paths, nil
}

func (s *Store) artifactPaths(ctx context.Context, where string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT a.path FROM evidence_artifacts a WHERE `+where), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_artifacts")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.list_artifacts")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

//i18n:ignore SQL fragment
const envFrom = ` FROM evidence_environments e
	LEFT JOIN project_environments pe ON e.kind = 'project' AND pe.id = e.project_environment_id
	LEFT JOIN projects p ON e.project_id <> '' AND p.id = e.project_id`

//i18n:ignore SQL column list
const envCols = `e.id, e.workspace_id, e.kind, e.project_id, COALESCE(p.name, ''), e.project_environment_id,
	COALESCE(pe.name, e.name), COALESCE(pe.display_name, ''),
	CASE e.kind WHEN 'project' THEN COALESCE(pe.url, '') WHEN 'sandbox' THEN '' ELSE e.base_url END,
	CASE e.kind WHEN 'project' THEN COALESCE(pe.is_production, FALSE) WHEN 'sandbox' THEN FALSE ELSE e.is_production END,
	e.allowed_hosts::text, e.secret_names::text, e.schedule, e.schedule_tz, e.on_deploy, e.last_scheduled_at,
	e.created_by, e.created_at, e.updated_at, COALESCE(p.repo_url, ''), COALESCE(p.default_branch, ''),
	e.schedule_project_id, e.schedule_tag`

func scanEnv(r rowScanner, extra ...any) (Environment, error) {
	var e Environment
	var hosts, names string
	var last sql.NullTime
	dest := append([]any{&e.ID, &e.WorkspaceID, &e.Kind, &e.ProjectID, &e.ProjectName, &e.ProjectEnvironmentID,
		&e.Name, &e.DisplayName, &e.BaseURL, &e.IsProduction, &hosts, &names, &e.Schedule, &e.ScheduleTZ,
		&e.OnDeploy, &last, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.RepoURL, &e.DefaultBranch,
		&e.ScheduleProjectID, &e.ScheduleTag}, extra...)
	if err := r.Scan(dest...); err != nil {
		return e, err
	}
	if err := json.Unmarshal([]byte(hosts), &e.AllowedHosts); err != nil {
		return e, err
	}
	if err := json.Unmarshal([]byte(names), &e.SecretNames); err != nil {
		return e, err
	}
	if e.AllowedHosts == nil {
		e.AllowedHosts = []string{}
	}
	if e.SecretNames == nil {
		e.SecretNames = []string{}
	}
	// The project page writes this URL too and does not trim it.
	e.BaseURL = strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
	e.LastScheduledAt = timePtr(last)
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	if e.Kind == KindProject {
		e.Deploy = &DeployInfo{Environment: e.Name}
		if repo, ok := GitHubRepo(e.RepoURL); ok {
			e.Deploy.Available, e.Deploy.Repo = true, repo
		} else {
			e.Deploy.Reason = ReasonRepoNotGitHub
		}
	}
	return e, nil
}

func GitHubRepo(repoURL string) (string, bool) {
	owner, name, ok := github.ParseRemote(repoURL)
	if !ok {
		return "", false
	}
	// The SSH form (git@host:owner/name) does not parse as a URL and its host may be an alias,
	// so only an http(s) URL is held to the GitHub host.
	if u, err := url.Parse(strings.TrimSpace(repoURL)); err == nil && u.Host != "" && !githubcfg.IsGitHubHost(u.Hostname()) {
		return "", false
	}
	return owner + "/" + name, true
}

// The caller must check the schedule's syntax; it owns the cron parser.
func normalizeEnv(e *Environment) error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return apperr.Invalid("evidence.errors.env_name_required")
	}
	if len([]rune(e.Name)) > maxEnvName {
		return apperr.Invalid("evidence.errors.env_name_too_long", "max", maxEnvName)
	}
	var err error
	if e.BaseURL, err = normalizeBaseURL(e.BaseURL); err != nil {
		return err
	}
	return normalizeTesting(e)
}

func normalizeTesting(e *Environment) error {
	var err error
	if e.AllowedHosts, err = normalizeHosts(e.AllowedHosts); err != nil {
		return err
	}
	e.Schedule = strings.TrimSpace(e.Schedule)
	if e.ScheduleTZ = strings.TrimSpace(e.ScheduleTZ); e.ScheduleTZ == "" {
		e.ScheduleTZ = "UTC"
	}
	return nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	//i18n:ignore Postgres error text matched, not shown
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}

func (s *Store) CreateEnvironment(ctx context.Context, ws, author string, in Environment) (*Environment, error) {
	e := Environment{Name: in.Name, BaseURL: in.BaseURL, IsProduction: in.IsProduction,
		AllowedHosts: in.AllowedHosts, Schedule: in.Schedule, ScheduleTZ: in.ScheduleTZ,
		ScheduleProjectID: in.ScheduleProjectID, ScheduleTag: in.ScheduleTag}
	if err := normalizeEnv(&e); err != nil {
		return nil, err
	}
	if err := s.checkScheduleScope(ctx, ws, &e); err != nil {
		return nil, err
	}
	id, now := newID(), time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_environments (id, workspace_id, kind, name, base_url,
		is_production, allowed_hosts, schedule, schedule_tz, schedule_project_id, schedule_tag, created_by,
		created_at, updated_at)
		VALUES (?,?,'standalone',?,?,?,CAST(? AS jsonb),?,?,?,?,?,?,?)`), id, ws, e.Name, e.BaseURL, e.IsProduction,
		jsonText(e.AllowedHosts), e.Schedule, e.ScheduleTZ, e.ScheduleProjectID, e.ScheduleTag, author, now, now)
	if isUniqueViolation(err) {
		return nil, apperr.Conflict("evidence.errors.env_name_taken", "name", e.Name)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.insert_env")
	}
	return s.GetEnvironment(ctx, ws, id)
}

type ProjectEnvironments interface {
	CreateEnvironment(ctx context.Context, projectID string, in project.EnvironmentInput) (*project.Environment, error)
	GetEnvironment(ctx context.Context, projectID, envID string) (*project.Environment, error)
	UpdateEnvironment(ctx context.Context, projectID, envID string, in project.EnvironmentInput) (*project.Environment, error)
	DeleteEnvironment(ctx context.Context, projectID, envID string) error
}

type ProjectEnvironmentInput struct {
	ProjectID    string
	Name         string
	DisplayName  string
	BaseURL      string
	IsProduction bool
	AllowedHosts []string
	Schedule     string
	ScheduleTZ   string
	OnDeploy     bool
}

func (s *Store) CreateProjectEnvironment(ctx context.Context, ws, author string, projects ProjectEnvironments,
	in ProjectEnvironmentInput) (*Environment, error) {
	if err := s.projectInWorkspace(ctx, ws, in.ProjectID); err != nil {
		return nil, err
	}
	base, err := normalizeBaseURL(in.BaseURL)
	if err != nil {
		return nil, err
	}
	t := Environment{AllowedHosts: in.AllowedHosts, Schedule: in.Schedule, ScheduleTZ: in.ScheduleTZ}
	if normErr := normalizeTesting(&t); normErr != nil {
		return nil, normErr
	}
	pe, err := projects.CreateEnvironment(ctx, in.ProjectID, project.EnvironmentInput{Name: in.Name,
		DisplayName: in.DisplayName, Provider: "other", IsProduction: in.IsProduction, URL: base})
	if err != nil {
		return nil, projectEnvError(err, in.Name)
	}
	id, err := s.ensureProjectRow(ctx, ws, author, in.ProjectID, pe.ID, pe.Name)
	if err == nil {
		now := time.Now().UTC()
		_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET allowed_hosts = CAST(? AS jsonb),
			schedule = ?, schedule_tz = ?, on_deploy = ?, base_url = ?, created_by = ?, updated_at = ? WHERE id = ?`),
			jsonText(t.AllowedHosts), t.Schedule, t.ScheduleTZ, in.OnDeploy, base, author, now, id)
	}
	if err != nil {
		logx.Warn(projects.DeleteEnvironment(context.WithoutCancel(ctx), in.ProjectID, pe.ID),
			"evidence.log.undo_failed", "env", pe.ID)
		return nil, apperr.Wrap(err, "evidence.errors.insert_env")
	}
	return s.GetEnvironment(ctx, ws, id)
}

// The project store returns a taken name as the raw database error.
func projectEnvError(err error, name string) error {
	if isUniqueViolation(err) {
		return apperr.Conflict("evidence.errors.env_name_taken", "name", name)
	}
	if apperr.KindOf(err) != apperr.KindInternal {
		return err
	}
	return apperr.Wrap(err, "evidence.errors.update_env")
}

func (s *Store) projectInWorkspace(ctx context.Context, ws, projectID string) error {
	var owner string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT workspace_id FROM projects WHERE id = ?`), projectID).Scan(&owner)
	if err != nil || owner != ws {
		return apperr.NotFound("evidence.errors.project_not_found", "id", projectID)
	}
	return nil
}

func (s *Store) ensureProjectRow(ctx context.Context, ws, author, projectID, projectEnvID, name string) (string, error) {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_environments (id, workspace_id, kind, project_id,
		project_environment_id, name, base_url, created_by, created_at, updated_at)
		VALUES (?,?,'project',?,?,?,'',?,?,?) ON CONFLICT DO NOTHING`),
		newID(), ws, projectID, projectEnvID, name, author, now, now); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT id FROM evidence_environments
		WHERE kind = 'project' AND project_environment_id = ?`), projectEnvID).Scan(&id)
	return id, err
}

func (s *Store) EnsureProjectRows(ctx context.Context, ws string) error {
	if _, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_environments (id, workspace_id, kind, project_id,
		project_environment_id, name, base_url, created_by, created_at, updated_at)
		SELECT substr(md5(random()::text || pe.id), 1, 16), p.workspace_id, 'project', pe.project_id, pe.id,
			pe.name, '', 'system', NOW(), NOW()
		FROM project_environments pe JOIN projects p ON p.id = pe.project_id
		WHERE p.workspace_id <> '' AND (? = '' OR p.workspace_id = ?)
		AND NOT EXISTS (SELECT 1 FROM evidence_environments e WHERE e.kind = 'project' AND e.project_environment_id = pe.id)
		ON CONFLICT DO NOTHING`), ws, ws); err != nil {
		return apperr.Wrap(err, "evidence.errors.insert_env")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO evidence_environments (id, workspace_id, kind, project_id,
		project_environment_id, name, base_url, created_by, created_at, updated_at)
		SELECT substr(md5(random()::text || p.id), 1, 16), p.workspace_id, 'sandbox', p.id, '',
			'sandbox', '', 'system', NOW(), NOW()
		FROM projects p
		WHERE p.workspace_id <> '' AND (? = '' OR p.workspace_id = ?)
		AND NOT EXISTS (SELECT 1 FROM evidence_environments e WHERE e.kind = 'sandbox' AND e.project_id = p.id)
		ON CONFLICT DO NOTHING`), ws, ws); err != nil {
		return apperr.Wrap(err, "evidence.errors.insert_env")
	}
	return s.healOrphans(ctx, ws)
}

func (s *Store) healOrphans(ctx context.Context, ws string) error {
	orphans, err := s.orphanRows(ctx, ws)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if !o.runs {
			if _, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM evidence_environments WHERE id = ?
				AND kind IN ('project', 'sandbox')
				AND NOT EXISTS (SELECT 1 FROM evidence_batches b WHERE b.environment_id = ?)`), o.id, o.id); err != nil {
				return apperr.Wrap(err, "evidence.errors.delete_env")
			}
			continue
		}
		for n := 1; n <= maxDetachTries; n++ {
			_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET kind = 'standalone', project_id = '',
				project_environment_id = '', name = ?, on_deploy = FALSE, updated_at = ?
				WHERE id = ? AND kind IN ('project', 'sandbox')`), detachedName(o.name, "", n), time.Now().UTC(), o.id)
			if !isUniqueViolation(err) {
				break
			}
		}
		if err != nil {
			return apperr.Wrap(err, "evidence.errors.update_env")
		}
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases c SET project_id = ''
		WHERE c.project_id <> '' AND (? = '' OR c.workspace_id = ?)
		AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id)`), ws, ws); err != nil {
		return apperr.Wrap(err, "evidence.errors.update_case")
	}
	return nil
}

type orphan struct {
	id, name string
	runs     bool
}

func (s *Store) orphanRows(ctx context.Context, ws string) ([]orphan, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT e.id, e.name,
			EXISTS (SELECT 1 FROM evidence_batches b WHERE b.environment_id = e.id)
		FROM evidence_environments e
		WHERE (? = '' OR e.workspace_id = ?) AND (
			(e.kind = 'project' AND NOT EXISTS (SELECT 1 FROM project_environments pe WHERE pe.id = e.project_environment_id))
			OR (e.kind = 'sandbox' AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = e.project_id)))`), ws, ws)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	defer rows.Close()
	var out []orphan
	for rows.Next() {
		var o orphan
		if err = rows.Scan(&o.id, &o.name, &o.runs); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.list_envs")
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) GetEnvironment(ctx context.Context, ws, id string) (*Environment, error) {
	e, err := scanEnv(s.db.QueryRowContext(ctx, s.rb(`SELECT `+envCols+envFrom+`
		WHERE e.workspace_id = ? AND e.id = ?`), ws, id))
	if err != nil {
		return nil, notFound(err, "evidence.errors.env_not_found", "id", id)
	}
	return &e, nil
}

func (s *Store) envByID(ctx context.Context, id string) (*Environment, error) {
	e, err := scanEnv(s.db.QueryRowContext(ctx, s.rb(`SELECT `+envCols+envFrom+` WHERE e.id = ?`), id))
	if err != nil {
		return nil, notFound(err, "evidence.errors.env_not_found", "id", id)
	}
	return &e, nil
}

func (s *Store) SandboxEnvironment(ctx context.Context, ws, projectID string) (*Environment, error) {
	if err := s.EnsureProjectRows(ctx, ws); err != nil {
		return nil, err
	}
	envs, err := s.envsWhere(ctx, `e.workspace_id = ? AND e.kind = 'sandbox' AND e.project_id = ?`, ws, projectID)
	if err != nil {
		return nil, err
	}
	if len(envs) == 0 {
		return nil, apperr.NotFound("evidence.errors.project_not_found", "id", projectID)
	}
	return &envs[0], nil
}

func (s *Store) ListEnvironments(ctx context.Context, ws, projectFilter string) ([]Environment, error) {
	where, args := `e.workspace_id = ?`, []any{ws}
	switch projectFilter {
	case "":
	case ProjectNone:
		where += ` AND e.kind = 'standalone'`
	default:
		where += ` AND e.project_id = ?`
		args = append(args, projectFilter)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+envCols+envFrom+` WHERE `+where+`
		ORDER BY CASE WHEN e.kind = 'standalone' THEN 1 ELSE 0 END, LOWER(COALESCE(p.name, '')),
			CASE WHEN e.kind = 'sandbox' THEN 1 ELSE 0 END,
			LOWER(COALESCE(NULLIF(pe.display_name, ''), pe.name, e.name))`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	out, err := scanEnvs(rows)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	for i := range out {
		bs, err := s.ListBatches(ctx, ws, out[i].ID, "", 1)
		if err != nil {
			return nil, err
		}
		if len(bs) > 0 {
			out[i].LastBatch = &bs[0]
		}
	}
	return out, nil
}

func (s *Store) UpdateEnvironment(ctx context.Context, ws, id string, p EnvironmentPatch, projects ProjectEnvironments) (*Environment, error) {
	e, err := s.GetEnvironment(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if checkErr := CheckPatch(e.Kind, p); checkErr != nil {
		return nil, checkErr
	}
	scheduleChanged := applyPatch(e, p)
	if e.Kind == KindStandalone {
		err = normalizeEnv(e)
	} else {
		err = normalizeTesting(e)
	}
	if err != nil {
		return nil, err
	}
	if scopeErr := s.checkScheduleScope(ctx, ws, e); scopeErr != nil {
		return nil, scopeErr
	}
	if e.Kind == KindProject && (p.DisplayName != nil || p.BaseURL != nil) {
		if writeErr := s.writeProjectSide(ctx, e, p, projects); writeErr != nil {
			return nil, writeErr
		}
	}
	e.UpdatedAt = time.Now().UTC()
	if scheduleChanged {
		// A new schedule counts from now, not from the last tick of the old one.
		e.LastScheduledAt = &e.UpdatedAt
	}
	// For a project row these hold a snapshot of the project environment, read only if the row is
	// detached.
	_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET name = ?, base_url = ?, is_production = ?,
		allowed_hosts = CAST(? AS jsonb), schedule = ?, schedule_tz = ?, on_deploy = ?, last_scheduled_at = ?,
		schedule_project_id = ?, schedule_tag = ?, updated_at = ? WHERE id = ?`), e.Name, e.BaseURL, e.IsProduction,
		jsonText(e.AllowedHosts), e.Schedule, e.ScheduleTZ, e.OnDeploy, e.LastScheduledAt, e.ScheduleProjectID,
		e.ScheduleTag, e.UpdatedAt, e.ID)
	if isUniqueViolation(err) {
		return nil, apperr.Conflict("evidence.errors.env_name_taken", "name", e.Name)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_env")
	}
	return s.GetEnvironment(ctx, ws, id)
}

func applyPatch(e *Environment, p EnvironmentPatch) bool {
	scheduleChanged := false
	if p.Name != nil {
		e.Name = *p.Name
	}
	if p.BaseURL != nil && e.Kind == KindStandalone {
		e.BaseURL = *p.BaseURL
	}
	if p.IsProduction != nil {
		e.IsProduction = *p.IsProduction
	}
	if p.AllowedHosts != nil {
		e.AllowedHosts = *p.AllowedHosts
	}
	if p.Schedule != nil {
		scheduleChanged = strings.TrimSpace(*p.Schedule) != e.Schedule
		e.Schedule = *p.Schedule
	}
	if p.ScheduleTZ != nil {
		scheduleChanged = scheduleChanged || strings.TrimSpace(*p.ScheduleTZ) != e.ScheduleTZ
		e.ScheduleTZ = *p.ScheduleTZ
	}
	if p.OnDeploy != nil {
		e.OnDeploy = *p.OnDeploy
	}
	if p.ScheduleProjectID != nil {
		e.ScheduleProjectID = strings.TrimSpace(*p.ScheduleProjectID)
	}
	if p.ScheduleTag != nil {
		e.ScheduleTag = strings.TrimSpace(*p.ScheduleTag)
	}
	return scheduleChanged
}

func (s *Store) checkScheduleScope(ctx context.Context, ws string, e *Environment) error {
	e.ScheduleProjectID, e.ScheduleTag = strings.TrimSpace(e.ScheduleProjectID), strings.TrimSpace(e.ScheduleTag)
	if e.ScheduleProjectID == "" {
		return nil
	}
	return s.projectInWorkspace(ctx, ws, e.ScheduleProjectID)
}

func (s *Store) writeProjectSide(ctx context.Context, e *Environment, p EnvironmentPatch, projects ProjectEnvironments) error {
	if projects == nil {
		return apperr.Unavailable("evidence.errors.projects_unavailable")
	}
	cur, err := projects.GetEnvironment(ctx, e.ProjectID, e.ProjectEnvironmentID)
	if err != nil {
		return err
	}
	in := project.EnvironmentInput{Name: cur.Name, DisplayName: cur.DisplayName, System: cur.System,
		Provider: cur.Provider, Region: cur.Region, Account: cur.Account, Selectors: cur.Selectors,
		IsProduction: cur.IsProduction, URL: cur.URL}
	if p.DisplayName != nil {
		in.DisplayName = *p.DisplayName
	}
	if p.BaseURL != nil {
		in.URL = strings.TrimSpace(*p.BaseURL)
		if in.URL != "" {
			if in.URL, err = normalizeBaseURL(in.URL); err != nil {
				return err
			}
		}
	}
	if _, err = projects.UpdateEnvironment(ctx, e.ProjectID, e.ProjectEnvironmentID, in); err != nil {
		return projectEnvError(err, in.Name)
	}
	e.DisplayName, e.BaseURL = in.DisplayName, in.URL
	return nil
}

func (s *Store) DeleteEnvironment(ctx context.Context, ws, id string) ([]string, error) {
	e, err := s.GetEnvironment(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	switch e.Kind {
	case KindProject:
		return nil, apperr.Conflict("evidence.errors.delete_on_project")
	case KindSandbox:
		return nil, apperr.Conflict("evidence.errors.sandbox_fixed")
	}
	var open int
	if err = s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM evidence_batches WHERE environment_id = ?
		AND status IN ('queued', 'running')`), id).Scan(&open); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.delete_env")
	}
	if open > 0 {
		return nil, apperr.Conflict("evidence.errors.env_busy")
	}
	return s.deleteEnvRows(ctx, `environment_id = ?`, `id = ?`, id)
}

func (s *Store) deleteEnvRows(ctx context.Context, runWhere, envWhere, arg string) ([]string, error) {
	//i18n:ignore SQL fragment
	paths, err := s.artifactPaths(ctx, `a.run_id IN (SELECT id FROM evidence_runs WHERE `+runWhere+`)`, arg)
	if err != nil {
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM evidence_runs WHERE `+runWhere), arg); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.delete_env")
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM evidence_environments WHERE `+envWhere), arg); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.delete_env")
	}
	return paths, nil
}

// Must be called before the project environment is deleted.
func (s *Store) DetachProjectEnvironment(ctx context.Context, projectEnvID string) error {
	ids, err := s.envIDs(ctx, `kind = 'project' AND project_environment_id = ?`, projectEnvID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if detachErr := s.detach(ctx, id); detachErr != nil {
			return detachErr
		}
	}
	return nil
}

// Must be called before the project is deleted.
func (s *Store) DetachProject(ctx context.Context, projectID string) ([]string, error) {
	ids, err := s.envIDs(ctx, `kind = 'project' AND project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if detachErr := s.detach(ctx, id); detachErr != nil {
			return nil, detachErr
		}
	}
	paths, err := s.deleteEnvRows(ctx,
		`environment_id IN (SELECT id FROM evidence_environments WHERE kind = 'sandbox' AND project_id = ?)`,
		`kind = 'sandbox' AND project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_cases SET project_id = '' WHERE project_id = ?`), projectID); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_case")
	}
	if _, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET schedule_project_id = ''
		WHERE schedule_project_id = ?`), projectID); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_env")
	}
	return paths, nil
}

func (s *Store) envIDs(ctx context.Context, where string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM evidence_environments WHERE `+where), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.list_envs")
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const maxDetachTries = 50

func (s *Store) detach(ctx context.Context, id string) error {
	e, err := s.envByID(ctx, id)
	if err != nil {
		return err
	}
	if e.Kind != KindProject {
		return nil
	}
	for n := 1; n <= maxDetachTries; n++ {
		_, err = s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET kind = 'standalone', project_id = '',
			project_environment_id = '', name = ?, base_url = ?, is_production = ?, on_deploy = FALSE, updated_at = ?
			WHERE id = ? AND kind = 'project'`), detachedName(e.Label(), e.ProjectName, n), e.BaseURL, e.IsProduction,
			time.Now().UTC(), id)
		if !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return apperr.Wrap(err, "evidence.errors.update_env")
	}
	return nil
}

func detachedName(label, projectName string, n int) string {
	name := label
	if projectName != "" {
		name += " (" + projectName + ")"
	}
	counter := ""
	if n > 1 {
		counter = " " + strconv.Itoa(n)
	}
	if r := []rune(name); len(r)+utf8.RuneCountInString(counter) > maxEnvName {
		name = strings.TrimSpace(string(r[:maxEnvName-utf8.RuneCountInString(counter)]))
	}
	return name + counter
}

func (s *Store) LinkEnvironment(ctx context.Context, ws, id, projectEnvID string) (*Environment, error) {
	e, err := s.GetEnvironment(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if e.Kind != KindStandalone {
		return nil, apperr.Conflict("evidence.errors.link_not_standalone")
	}
	var projectID string
	err = s.db.QueryRowContext(ctx, s.rb(`SELECT pe.project_id FROM project_environments pe
		JOIN projects p ON p.id = pe.project_id WHERE pe.id = ? AND p.workspace_id = ?`), projectEnvID, ws).Scan(&projectID)
	if err != nil {
		return nil, notFound(err, "evidence.errors.project_env_not_found", "id", projectEnvID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.begin")
	}
	defer func() { _ = tx.Rollback() }()
	var target string
	err = tx.QueryRowContext(ctx, s.rb(`SELECT id FROM evidence_environments
		WHERE kind = 'project' AND project_environment_id = ? FOR UPDATE`), projectEnvID).Scan(&target)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, apperr.Wrap(err, "evidence.errors.update_env")
	default:
		var batches int
		if err = tx.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM evidence_batches WHERE environment_id = ?`),
			target).Scan(&batches); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.update_env")
		}
		if batches > 0 {
			return nil, apperr.Conflict("evidence.errors.link_target_has_runs")
		}
		if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM evidence_environments WHERE id = ?`), target); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.update_env")
		}
	}
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET kind = 'project', project_id = ?,
		project_environment_id = ?, updated_at = ? WHERE id = ? AND kind = 'standalone'`),
		projectID, projectEnvID, time.Now().UTC(), id); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_env")
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.commit")
	}
	return s.GetEnvironment(ctx, ws, id)
}

func (s *Store) SetSecrets(ctx context.Context, ws, id string, in map[string]*string) (*Environment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.begin")
	}
	defer func() { _ = tx.Rollback() }()
	var sealed string
	err = tx.QueryRowContext(ctx, s.rb(`SELECT secrets_sealed FROM evidence_environments
		WHERE workspace_id = ? AND id = ? FOR UPDATE`), ws, id).Scan(&sealed)
	if err != nil {
		return nil, notFound(err, "evidence.errors.env_not_found", "id", id)
	}
	cur, err := openSecrets(sealed)
	if err != nil {
		return nil, err
	}
	for name, v := range in {
		if !ValidSecretName(name) {
			return nil, apperr.Invalid("evidence.errors.secret_name_invalid", "name", name)
		}
		if v == nil {
			delete(cur, name)
			continue
		}
		if len(*v) > maxSecretValue {
			return nil, apperr.Invalid("evidence.errors.secret_too_long", "name", name)
		}
		cur[name] = *v
	}
	if len(cur) > maxSecrets {
		return nil, apperr.Invalid("evidence.errors.too_many_secrets", "max", maxSecrets)
	}
	names := make([]string, 0, len(cur))
	for n := range cur {
		names = append(names, n)
	}
	sortStrings(names)
	newSealed := ""
	if len(cur) > 0 {
		plain, marshalErr := json.Marshal(cur)
		if marshalErr != nil {
			return nil, apperr.Wrap(marshalErr, "evidence.errors.seal_secrets")
		}
		if newSealed, err = secretbox.Encrypt(LabelSecrets, string(plain)); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.seal_secrets")
		}
	}
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET secrets_sealed = ?,
		secret_names = CAST(? AS jsonb), updated_at = ? WHERE id = ?`), newSealed, jsonText(names), time.Now().UTC(), id); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.update_env")
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.commit")
	}
	return s.GetEnvironment(ctx, ws, id)
}

// Only runners and recording browsers of the same workspace may receive these.
func (s *Store) Secrets(ctx context.Context, ws, id string) (map[string]string, error) {
	var sealed string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT secrets_sealed FROM evidence_environments
		WHERE workspace_id = ? AND id = ?`), ws, id).Scan(&sealed)
	if err != nil {
		return nil, notFound(err, "evidence.errors.env_not_found", "id", id)
	}
	return openSecrets(sealed)
}

func openSecrets(sealed string) (map[string]string, error) {
	out := map[string]string{}
	if sealed == "" {
		return out, nil
	}
	plain, err := secretbox.Decrypt(LabelSecrets, sealed)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.open_secrets")
	}
	if err = json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.open_secrets")
	}
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (s *Store) ScheduledEnvironments(ctx context.Context) ([]Environment, error) {
	envs, err := s.envsWhere(ctx, `e.schedule <> '' AND e.kind <> 'sandbox'`)
	if err != nil {
		return nil, err
	}
	out := envs[:0]
	for i := range envs {
		if envs[i].BaseURL != "" {
			out = append(out, envs[i])
		}
	}
	return out, nil
}

func (s *Store) DeployEnvironments(ctx context.Context, workspaces []string) ([]Environment, error) {
	if len(workspaces) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(workspaces))
	for _, ws := range workspaces {
		args = append(args, ws)
	}
	//i18n:ignore SQL fragment
	return s.envsWhere(ctx, `e.kind = 'project' AND e.on_deploy AND e.workspace_id IN (`+placeholders(len(workspaces))+`)`, args...)
}

func (s *Store) envsWhere(ctx context.Context, where string, args ...any) ([]Environment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+envCols+envFrom+` WHERE `+where), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	out, err := scanEnvs(rows)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_envs")
	}
	return out, nil
}

func (s *Store) MarkScheduled(ctx context.Context, id string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE evidence_environments SET last_scheduled_at = ? WHERE id = ?`), t.UTC(), id)
	return err
}

func scanEnvs(rows *sql.Rows) ([]Environment, error) {
	defer rows.Close()
	out := []Environment{}
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CaseCountsByProject(ctx context.Context, ws string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT project_id, COUNT(*) FROM evidence_cases
		WHERE workspace_id = ? GROUP BY project_id`), ws)
	if err != nil {
		return nil, apperr.Wrap(err, "evidence.errors.list_cases")
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err = rows.Scan(&p, &n); err != nil {
			return nil, apperr.Wrap(err, "evidence.errors.list_cases")
		}
		out[p] = n
	}
	return out, rows.Err()
}
