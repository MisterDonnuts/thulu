package task

// Approvals with stage "" (copied from flags, or created via the flags alias and
// env-mapping/dashboard proposals)
// belong to no stage and only gate stages flagged blocked_by_flags; stagegate.go relies on this.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type ApprovalSource string

const (
	ApprovalFromStage  ApprovalSource = "stage"
	ApprovalFromAgent  ApprovalSource = "agent"
	ApprovalFromSystem ApprovalSource = "system"
	ApprovalFromPerson ApprovalSource = "person"
)

type ApprovalStatus string

const (
	ApprovalPending          ApprovalStatus = "pending"
	ApprovalApproved         ApprovalStatus = "approved"
	ApprovalChangesRequested ApprovalStatus = "changes_requested"
	ApprovalWaived           ApprovalStatus = "waived"
)

func (st ApprovalStatus) Settled() bool { return st == ApprovalApproved || st == ApprovalWaived }

func (st ApprovalStatus) IsDecision() bool {
	return st == ApprovalApproved || st == ApprovalChangesRequested || st == ApprovalWaived
}

const (
	maxApprovalLabelRunes  = 200
	maxApprovalReasonRunes = 4000
	maxApprovalNoteRunes   = 4000
)

type Approval struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	Key           string          `json:"key,omitempty"`
	Label         string          `json:"label"`
	Stage         Stage           `json:"stage,omitempty"`
	Source        ApprovalSource  `json:"source"`
	Status        ApprovalStatus  `json:"status"`
	Reason        string          `json:"reason,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	CreatedBy     string          `json:"created_by"`
	DecidedBy     string          `json:"decided_by,omitempty"`
	DecidedNote   string          `json:"decided_note,omitempty"`
	ApprovedAtRef string          `json:"approved_at_ref,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	DecidedAt     *time.Time      `json:"decided_at,omitempty"`
}

// 43 creates the table and unique index in one step so the index lands on an empty table.
// task_flags stays until the
// contract step two minors later. 45 keeps flag ids: env mappings and dashboards reference their
// review flag by id.
var approvalMigrations = []migrate.Migration{
	{ID: 43, SQL: `CREATE TABLE IF NOT EXISTS task_approvals (
			id              TEXT PRIMARY KEY,
			task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			key             TEXT NOT NULL DEFAULT '',
			label           TEXT NOT NULL,
			stage           TEXT NOT NULL DEFAULT '',
			source          TEXT NOT NULL DEFAULT 'person',
			status          TEXT NOT NULL DEFAULT 'pending',
			reason          TEXT NOT NULL DEFAULT '',
			payload         TEXT NOT NULL DEFAULT '{}',
			created_by      TEXT NOT NULL DEFAULT '',
			decided_by      TEXT NOT NULL DEFAULT '',
			decided_note    TEXT NOT NULL DEFAULT '',
			approved_at_ref TEXT NOT NULL DEFAULT '',
			created_at      TIMESTAMPTZ NOT NULL,
			decided_at      TIMESTAMPTZ
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_approvals_stage_key
			ON task_approvals(task_id, stage, key) WHERE source = 'stage'`},
	{ID: 44, SQL: `CREATE INDEX IF NOT EXISTS idx_approvals_task ON task_approvals(task_id, created_at);
		CREATE INDEX IF NOT EXISTS idx_approvals_status ON task_approvals(status, task_id)`},
	{ID: 45, SQL: `INSERT INTO task_approvals (id, task_id, key, label, stage, source, status, reason, payload,
			created_by, decided_by, decided_note, approved_at_ref, created_at, decided_at)
		SELECT f.id, f.task_id, f.kind, f.label, '',
		       CASE WHEN f.kind = 'infra_impact' AND f.payload LIKE '%"environment_id"%' THEN 'system'
		            WHEN f.created_by LIKE 'agent:%' THEN 'agent'
		            ELSE 'person' END,
		       CASE WHEN f.status = 'rejected' THEN 'changes_requested' ELSE f.status END,
		       '', f.payload, f.created_by, f.resolved_by, f.resolved_note, '',
		       f.created_at AT TIME ZONE 'UTC', f.resolved_at AT TIME ZONE 'UTC'
		  FROM task_flags f
		 WHERE f.task_id IN (SELECT id FROM tasks)
		ON CONFLICT (id) DO NOTHING`},
}

const approvalCols = `id, task_id, key, label, stage, source, status, reason, payload,
	created_by, decided_by, decided_note, approved_at_ref, created_at, decided_at`

func scanApproval(r rowScanner) (*Approval, error) {
	a := &Approval{}
	var payload string
	var decidedAt sql.NullTime
	if err := r.Scan(&a.ID, &a.TaskID, &a.Key, &a.Label, &a.Stage, &a.Source, &a.Status, &a.Reason, &payload,
		&a.CreatedBy, &a.DecidedBy, &a.DecidedNote, &a.ApprovedAtRef, &a.CreatedAt, &decidedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("task.approvals.errors.not_found")
		}
		return nil, apperr.Wrap(err, "task.approvals.errors.scan")
	}
	if payload != "" && payload != "{}" {
		a.Payload = json.RawMessage(payload)
	}
	a.CreatedAt = a.CreatedAt.UTC()
	if decidedAt.Valid {
		t := decidedAt.Time.UTC()
		a.DecidedAt = &t
	}
	return a, nil
}

func (s *Store) queryApprovals(ctx context.Context, where string, args ...any) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+approvalCols+` FROM task_approvals WHERE `+where+
		` ORDER BY created_at ASC, id ASC`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.approvals.errors.list")
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for i := range out {
		s.mirrorFlag(ctx, s.db, &out[i])
	}
	return out, nil
}

func (s *Store) ListApprovals(ctx context.Context, taskID string) ([]Approval, error) {
	return s.queryApprovals(ctx, `task_id = ?`, taskID)
}

func (s *Store) GetApproval(ctx context.Context, id string) (*Approval, error) {
	return scanApproval(s.db.QueryRowContext(ctx, s.rb(`SELECT `+approvalCols+` FROM task_approvals WHERE id = ?`), id))
}

// Set at boot, before the store is used concurrently; there is no lock.
func (s *Store) WithApprovalHook(fn func(ctx context.Context, a Approval)) *Store {
	s.onApproval = fn
	return s
}

func checkApprovalText(a *Approval) error {
	a.Label = strings.TrimSpace(a.Label)
	if a.Label == "" {
		return apperr.Invalid("task.approvals.errors.label_required")
	}
	if utf8.RuneCountInString(a.Label) > maxApprovalLabelRunes {
		return apperr.Invalid("task.approvals.errors.label_too_long", "max", maxApprovalLabelRunes)
	}
	if utf8.RuneCountInString(a.Reason) > maxApprovalReasonRunes {
		return apperr.Invalid("task.approvals.errors.reason_too_long", "max", maxApprovalReasonRunes)
	}
	if a.Key != "" && !types.ValidStageID(a.Key) {
		return apperr.Invalid("task.approvals.errors.key_invalid", "key", a.Key)
	}
	return nil
}

// No timeline event here: AddApproval records one and the flags alias records its own flag event.
func (s *Store) insertApproval(ctx context.Context, a *Approval) error {
	if err := checkApprovalText(a); err != nil {
		return err
	}
	if a.TaskID == "" {
		return apperr.Invalid("task.approvals.errors.task_required")
	}
	if a.ID == "" {
		a.ID = mustGenerateID()
	}
	if a.Status == "" {
		a.Status = ApprovalPending
	}
	if a.Source == "" {
		a.Source = ApprovalFromPerson
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	payload := string(a.Payload)
	if payload == "" {
		payload = "{}"
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO task_approvals (`+approvalCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		a.ID, a.TaskID, a.Key, a.Label, string(a.Stage), string(a.Source), string(a.Status), a.Reason, payload,
		a.CreatedBy, a.DecidedBy, a.DecidedNote, a.ApprovedAtRef, a.CreatedAt, a.DecidedAt); err != nil {
		return apperr.Wrap(err, "task.approvals.errors.insert", "task", a.TaskID)
	}
	s.mirrorFlag(ctx, s.db, a)
	s.approvalOpened(ctx, *a)
	return nil
}

func (s *Store) approvalOpened(ctx context.Context, a Approval) {
	if s.onApproval != nil {
		s.onApproval(ctx, a)
	}
	s.notifyTask(ctx, a.TaskID)
}

func (s *Store) AddApproval(ctx context.Context, a *Approval) error {
	if err := s.insertApproval(ctx, a); err != nil {
		return err
	}
	s.recordApprovalEvent(ctx, EventApprovalRequested, a, a.CreatedBy, "task.approvals.events.requested")
	return nil
}

func (s *Store) recordApprovalEvent(ctx context.Context, kind EventKind, a *Approval, actor, summaryKey string) {
	payload := map[string]any{
		"approval_id": a.ID, "key": a.Key, "label": a.Label, "stage": string(a.Stage),
		"source": string(a.Source), "status": string(a.Status),
	}
	if a.DecidedNote != "" {
		payload["note"] = a.DecidedNote
	}
	logx.Warn(s.RecordEvent(ctx, a.TaskID, kind, a.ID, actor,
		i18n.Text(summaryKey, "label", a.Label), payload),
		"task.approvals.log.record_event", "task", a.TaskID)
}

func (s *Store) RecordApprovalEvent(ctx context.Context, kind EventKind, a *Approval, actor, summaryKey string) {
	s.recordApprovalEvent(ctx, kind, a, actor, summaryKey)
}

func requirementApplies(req types.ApprovalRequirement, typeID string) bool {
	return len(req.TaskTypes) == 0 || slices.Contains(req.TaskTypes, typeID)
}

func (s *Store) EnsureStageApprovals(ctx context.Context, t *Task, info types.StageInfo) ([]Approval, error) {
	if t == nil || t.ID == "" {
		return nil, nil
	}
	var opened []Approval
	for _, req := range info.RequiredApprovals {
		if !requirementApplies(req, t.TypeID) {
			continue
		}
		a := Approval{
			ID: mustGenerateID(), TaskID: t.ID, Key: req.Key, Label: req.Label, Stage: info.ID,
			Source: ApprovalFromStage, Status: ApprovalPending, CreatedBy: "octopus",
			CreatedAt: time.Now().UTC(),
		}
		res, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO task_approvals (`+approvalCols+`)
			VALUES (?,?,?,?,?,?,?,'','{}',?,'','','',?,NULL)
			ON CONFLICT (task_id, stage, key) WHERE source = 'stage' DO NOTHING`),
			a.ID, a.TaskID, a.Key, a.Label, string(a.Stage), string(a.Source), string(a.Status),
			a.CreatedBy, a.CreatedAt)
		if err != nil {
			return opened, apperr.Wrap(err, "task.approvals.errors.insert", "task", t.ID)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		opened = append(opened, a)
		s.recordApprovalEvent(ctx, EventApprovalRequested, &a, "octopus", "task.approvals.events.requested")
		s.approvalOpened(ctx, a)
	}
	return opened, nil
}

// Reopening to pending keeps the caller's by/note: the flags alias depends on a reopened flag
// keeping its note.
func (s *Store) SetApprovalStatus(ctx context.Context, id string, status ApprovalStatus, by, note, ref string) (*Approval, error) {
	if status != ApprovalPending && !status.IsDecision() {
		return nil, apperr.Invalid("task.approvals.errors.bad_decision", "decision", status)
	}
	if utf8.RuneCountInString(note) > maxApprovalNoteRunes {
		return nil, apperr.Invalid("task.approvals.errors.note_too_long", "max", maxApprovalNoteRunes)
	}
	var decidedAt *time.Time
	if status != ApprovalPending {
		now := time.Now().UTC()
		decidedAt = &now
	}
	if status != ApprovalApproved {
		ref = ""
	}
	a, err := scanApproval(s.db.QueryRowContext(ctx, s.rb(`UPDATE task_approvals
		SET status = ?, decided_by = ?, decided_note = ?, decided_at = ?, approved_at_ref = ?
		WHERE id = ? RETURNING `+approvalCols),
		string(status), by, note, decidedAt, ref, id))
	if err != nil {
		return nil, err
	}
	s.mirrorFlag(ctx, s.db, a)
	s.notifyTask(ctx, a.TaskID)
	return a, nil
}

func (s *Store) SetApprovalLabel(ctx context.Context, id, label string) error {
	probe := Approval{Label: label}
	if err := checkApprovalText(&probe); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE task_approvals SET label = ? WHERE id = ?`), probe.Label, id)
	if err != nil {
		return apperr.Wrap(err, "task.approvals.errors.update", "id", id)
	}
	s.mirrorFlagByID(ctx, id)
	return nil
}

func (s *Store) DeleteApproval(ctx context.Context, id string) error {
	var taskID string
	err := s.db.QueryRowContext(ctx, s.rb(`DELETE FROM task_approvals WHERE id = ? RETURNING task_id`), id).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("task.approvals.errors.not_found")
	}
	if err != nil {
		return apperr.Wrap(err, "task.approvals.errors.delete", "id", id)
	}
	s.unmirrorFlag(ctx, id)
	s.notifyTask(ctx, taskID)
	return nil
}

func (s *Store) reopenWhere(ctx context.Context, where string, args ...any) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`UPDATE task_approvals
		SET status = 'pending', decided_by = '', decided_note = '', decided_at = NULL, approved_at_ref = ''
		WHERE `+where+` RETURNING `+approvalCols), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.approvals.errors.reset")
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) ReopenChangesRequested(ctx context.Context, taskID string, stage Stage, decidedBefore time.Time) ([]Approval, error) {
	if stage == "" {
		return nil, nil
	}
	where, args := `task_id = ? AND stage = ? AND status = 'changes_requested'`, []any{taskID, string(stage)}
	if !decidedBefore.IsZero() {
		where += ` AND decided_at < ?`
		args = append(args, decidedBefore)
	}
	out, err := s.reopenWhere(ctx, where, args...)
	if len(out) > 0 {
		s.notifyTask(ctx, taskID)
	}
	return out, err
}

func (s *Store) ResetStaleApprovals(
	ctx context.Context, taskID string, stage Stage, reqs []types.ApprovalRequirement, ref string,
) ([]Approval, error) {
	if ref == "" || stage == "" {
		return nil, nil
	}
	approved, err := s.queryApprovals(ctx, `task_id = ? AND stage = ? AND status = 'approved' AND approved_at_ref <> ?`,
		taskID, string(stage), ref)
	if err != nil {
		return nil, err
	}
	var ids []any
	for i := range approved {
		if resetsOnChange(approved[i], reqs) {
			ids = append(ids, approved[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := s.reopenWhere(ctx, `id IN (`+placeholderList(len(ids))+`)`, ids...) //i18n:ignore SQL
	if len(out) > 0 {
		s.notifyTask(ctx, taskID)
	}
	return out, err
}

func resetsOnChange(a Approval, reqs []types.ApprovalRequirement) bool {
	if a.Source != ApprovalFromStage {
		return true
	}
	for _, req := range reqs {
		if req.Key == a.Key {
			return req.ResetOnChange
		}
	}
	return true
}

const InboxLimit = 500

type PendingApproval struct {
	Approval
	TaskTitle string `json:"task_title"`
	TaskStage Stage  `json:"task_stage"`
	BoardID   string `json:"board_id"`
}

func (s *Store) ListPendingApprovals(ctx context.Context, workspaceID string, unrestricted bool) ([]PendingApproval, error) {
	q := `SELECT a.id, a.task_id, a.key, a.label, a.stage, a.source, a.status, a.reason, a.payload,
	             a.created_by, a.decided_by, a.decided_note, a.approved_at_ref, a.created_at, a.decided_at,
	             t.title, t.stage, t.board_id
	        FROM task_approvals a JOIN tasks t ON t.id = a.task_id
	       WHERE a.status IN (?, ?) AND t.archived_at IS NULL`
	args := []any{string(ApprovalPending), string(ApprovalChangesRequested)}
	if !unrestricted {
		q += ` AND t.workspace_id = ?`
		args = append(args, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(q+` ORDER BY a.created_at ASC, a.id ASC LIMIT `+strconv.Itoa(InboxLimit)), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.approvals.errors.list")
	}
	defer rows.Close()
	out := []PendingApproval{}
	for rows.Next() {
		var p PendingApproval
		var payload string
		var decidedAt sql.NullTime
		if err := rows.Scan(&p.ID, &p.TaskID, &p.Key, &p.Label, &p.Stage, &p.Source, &p.Status, &p.Reason, &payload,
			&p.CreatedBy, &p.DecidedBy, &p.DecidedNote, &p.ApprovedAtRef, &p.CreatedAt, &decidedAt,
			&p.TaskTitle, &p.TaskStage, &p.BoardID); err != nil {
			return nil, apperr.Wrap(err, "task.approvals.errors.scan")
		}
		if payload != "" && payload != "{}" {
			p.Payload = json.RawMessage(payload)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) decorateApprovalCounts(ctx context.Context, out []Task) {
	if len(out) == 0 {
		return
	}
	ids := taskIDsOf(out)
	q := `SELECT task_id, COUNT(*) FROM task_approvals
	       WHERE status <> ? AND status <> ? AND task_id IN (` + placeholderList(len(ids)) + `) GROUP BY task_id`
	args := append([]any{string(ApprovalApproved), string(ApprovalWaived)}, ids...)
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		logx.Warn(err, "task.approvals.log.decorate_counts")
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			logx.Warn(err, "task.approvals.log.decorate_counts")
			return
		}
		counts[id] = n
	}
	logx.Warn(rows.Err(), "task.approvals.log.decorate_counts")
	for i := range out {
		out[i].PendingApprovals = counts[out[i].ID]
	}
}

// Creating the fork already opened its stage approvals; delete them first or the source's copies
// collide on the stage unique key.
func (s *Store) insertForkApprovals(ctx context.Context, tx *sql.Tx, dstID string, list []Approval) error {
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM task_approvals WHERE task_id = ?`), dstID); err != nil {
		return apperr.Wrap(err, "task.approvals.errors.fork_insert")
	}
	for i := range list {
		a := list[i]
		payload := string(a.Payload)
		if payload == "" {
			payload = "{}"
		}
		a.ID, a.TaskID = mustGenerateID(), dstID
		if _, err := tx.ExecContext(ctx, s.rb(`INSERT INTO task_approvals (`+approvalCols+`)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			a.ID, a.TaskID, a.Key, a.Label, string(a.Stage), string(a.Source), string(a.Status), a.Reason,
			payload, a.CreatedBy, a.DecidedBy, a.DecidedNote, a.ApprovedAtRef, a.CreatedAt, a.DecidedAt); err != nil {
			return apperr.Wrap(err, "task.approvals.errors.fork_insert")
		}
		s.mirrorFlag(ctx, tx, &a)
	}
	return nil
}

func (s *Store) ImportApproval(ctx context.Context, a Approval) error {
	if a.ID == "" || a.TaskID == "" {
		return apperr.Invalid("task.approvals.errors.import_ids")
	}
	payload := string(a.Payload)
	if payload == "" {
		payload = "{}"
	}
	if a.Source == "" {
		a.Source = ApprovalFromPerson
	}
	if a.Status == "" {
		a.Status = ApprovalPending
	}
	if a.Source == ApprovalFromStage {
		// Importing the task already opened its stage approvals; the bundle's row must replace that
		// one or it collides on the stage unique key.
		if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM task_approvals
			WHERE task_id = ? AND stage = ? AND key = ? AND source = ? AND id <> ?`),
			a.TaskID, string(a.Stage), a.Key, string(ApprovalFromStage), a.ID); err != nil {
			return apperr.Wrap(err, "task.approvals.errors.import", "id", a.ID)
		}
	}
	res, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO task_approvals (`+approvalCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`),
		a.ID, a.TaskID, a.Key, a.Label, string(a.Stage), string(a.Source), string(a.Status), a.Reason, payload,
		a.CreatedBy, a.DecidedBy, a.DecidedNote, a.ApprovedAtRef, a.CreatedAt, a.DecidedAt)
	if err != nil {
		return apperr.Wrap(err, "task.approvals.errors.import", "id", a.ID)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.mirrorFlag(ctx, s.db, &a)
	}
	return nil
}
