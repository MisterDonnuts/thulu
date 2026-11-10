package task

import (
	"context"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func (s *Store) ImportTask(ctx context.Context, t *Task) (bool, error) {
	if t.ID == "" {
		return false, apperr.Invalid("task.errors.import_task_id")
	}
	if _, err := s.Get(ctx, t.ID); err == nil {
		return false, nil
	}
	created, updated := t.CreatedAt, t.UpdatedAt
	if t.Status == StatusAwaitingInput || t.Status == StatusQueued {
		// Questions and the in-memory run queue don't travel in the bundle, so nothing here could
		// resume these; land them idle.
		t.Status = StatusIdle
	}
	if err := s.Create(ctx, t); err != nil {
		return false, apperr.Wrap(err, "task.errors.import_task", "id", t.ID)
	}
	if created.IsZero() {
		return true, nil
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE tasks SET created_at = ?, updated_at = ? WHERE id = ?`),
		created, nonZeroTime(updated, created), t.ID); err != nil {
		return true, apperr.Wrap(err, "task.errors.import_task_timestamps", "id", t.ID)
	}
	return true, nil
}

func nonZeroTime(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

func (s *Store) ImportComment(ctx context.Context, c Comment) error {
	if c.ID == "" || c.TaskID == "" {
		return apperr.Invalid("task.errors.import_comment_ids")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_comments (id, task_id, parent_id, source, author, body, stage, created_at, resolved_at, resolved_by,
		                           note_key, body_i18n)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`),
		c.ID, c.TaskID, c.ParentID, c.Source, c.Author, c.Body, c.Stage, c.CreatedAt, c.ResolvedAt, c.ResolvedBy,
		c.Key, encodeTexts(c.BodyI18n))
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_comment", "id", c.ID)
	}
	return nil
}

func (s *Store) ImportReport(ctx context.Context, r Report) error {
	if r.ID == "" || r.TaskID == "" {
		return apperr.Invalid("task.errors.import_report_ids")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_reports (id, task_id, source, author, stage, instance_id, kind, title, body, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`),
		r.ID, r.TaskID, r.Source, r.Author, r.Stage, r.InstanceID, r.Kind, r.Title, r.Body, r.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_report", "id", r.ID)
	}
	return nil
}

func (s *Store) ImportEvent(ctx context.Context, e WorkflowEvent) error {
	if e.ID == "" || e.TaskID == "" {
		return apperr.Invalid("task.errors.import_event_ids")
	}
	raw := string(e.Payload)
	if raw == "" {
		raw = "{}"
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workflow_events (id, task_id, kind, ref_id, actor, summary, payload, created_at)
		 VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`),
		e.ID, e.TaskID, e.Kind, e.RefID, e.Actor, e.Summary, raw, e.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_event", "id", e.ID)
	}
	return nil
}

// The caller must place the bytes at path first; a row without the file is a dangling reference.
func (s *Store) ImportAttachmentRow(ctx context.Context, a Attachment) error {
	if a.ID == "" || a.TaskID == "" {
		return apperr.Invalid("task.errors.import_attachment_ids")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_attachments (id, task_id, comment_id, author, filename, mime_type, size, path, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`),
		a.ID, a.TaskID, a.CommentID, a.Author, a.Filename, a.MimeType, a.Size, a.Path, a.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_attachment", "id", a.ID)
	}
	return nil
}
