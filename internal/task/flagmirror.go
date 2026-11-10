package task

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Rollback compatibility: the previous release reads task_flags, so every write of a stage-less
// approval is mirrored there
// and syncFlagsBack imports what that release wrote. Drop with task_flags in the contract
// release (docs/upgrade-guide.md).

// Migration 45's INSERT skips rows already copied, so syncFlagsBack can re-run it.
const flagsCopyMigration = 45

func flagStatusOf(st ApprovalStatus) string {
	switch st {
	case ApprovalChangesRequested:
		return string(FlagRejected)
	case ApprovalWaived, ApprovalApproved:
		return string(FlagApproved)
	default:
		return string(FlagPending)
	}
}

// Errors are logged, not returned: approvals are the source of truth and the mirror only serves a
// rollback.
func (s *Store) mirrorFlag(ctx context.Context, ex execer, a *Approval) {
	if a.Stage != "" {
		return
	}
	payload := string(a.Payload)
	if payload == "" {
		payload = "{}"
	}
	created := a.CreatedAt.UTC()
	var resolved any
	if a.DecidedAt != nil && a.Status != ApprovalPending {
		resolved = a.DecidedAt.UTC()
	}
	_, err := ex.ExecContext(ctx, s.rb(`INSERT INTO task_flags
		(id, task_id, kind, label, status, payload, created_by, resolved_by, resolved_note, created_at, resolved_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET label = EXCLUDED.label, status = EXCLUDED.status,
			resolved_by = EXCLUDED.resolved_by, resolved_note = EXCLUDED.resolved_note,
			resolved_at = EXCLUDED.resolved_at`),
		a.ID, a.TaskID, a.Key, a.Label, flagStatusOf(a.Status), payload, a.CreatedBy,
		a.DecidedBy, a.DecidedNote, created, resolved)
	logx.Warn(err, "task.approvals.log.mirror_flag", "approval", a.ID)
}

func (s *Store) mirrorFlagByID(ctx context.Context, id string) {
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return
	}
	s.mirrorFlag(ctx, s.db, a)
}

func (s *Store) unmirrorFlag(ctx context.Context, id string) {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM task_flags WHERE id = ?`), id)
	logx.Warn(err, "task.approvals.log.mirror_flag", "approval", id)
}

func (s *Store) syncFlagsBack(ctx context.Context) {
	for _, m := range approvalMigrations {
		if m.ID == flagsCopyMigration {
			_, err := s.db.ExecContext(ctx, m.SQL)
			logx.Warn(err, "task.approvals.log.sync_flags", "step", "copy")
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE task_approvals a
		   SET label = f.label,
		       status = CASE WHEN f.status = 'rejected' THEN 'changes_requested' ELSE f.status END,
		       decided_by = f.resolved_by, decided_note = f.resolved_note,
		       decided_at = f.resolved_at AT TIME ZONE 'UTC',
		       approved_at_ref = CASE WHEN f.status = 'approved' THEN a.approved_at_ref ELSE '' END
		  FROM task_flags f
		 WHERE a.id = f.id AND a.stage = ''
		   AND (a.label <> f.label
		        OR CASE WHEN a.status = 'changes_requested' THEN 'rejected'
		                WHEN a.status = 'waived' THEN 'approved'
		                ELSE a.status END <> f.status)`)
	logx.Warn(err, "task.approvals.log.sync_flags", "step", "update")
}
