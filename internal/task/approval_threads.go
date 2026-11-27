package task

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Each approval opens a thread (kind approval) when it is requested; its decisions are the replies.
// The thread is found by the approval id in its meta, so task_approvals carries no comment id.

func (s *Store) openApprovalThread(ctx context.Context, a Approval) {
	n := NoteOf("task.approvals.comments.requested", "approval", a.Label)
	if r := strings.TrimSpace(a.Reason); r != "" {
		n = n.Add("task.approvals.comments.requested_reason", "reason", quoteNote(r))
	}
	_, err := s.PostComment(ctx, NewComment{
		TaskID: a.TaskID, Source: SourceSystem, Author: "octopus", Note: &n,
		Kind: CommentApproval, Meta: &CommentMeta{ApprovalID: a.ID},
	})
	logx.Warn(err, "task.approvals.log.open_thread", "task", a.TaskID, "approval", a.ID)
}

// approvalThread is the root of the thread approval id opened; "" when it has none (an approval
// older than threads, or one imported from a bundle).
func (s *Store) approvalThread(ctx context.Context, taskID, approvalID string) (string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, meta FROM task_comments WHERE task_id = ? AND kind = ? AND parent_id = ''
		  ORDER BY created_at ASC`), taskID, string(CommentApproval))
	if err != nil {
		return "", apperr.Wrap(err, "task.approvals.errors.thread", "approval", approvalID)
	}
	defer rows.Close()
	for rows.Next() {
		var id, meta string
		if err := rows.Scan(&id, &meta); err != nil {
			return "", apperr.Wrap(err, "task.approvals.errors.thread", "approval", approvalID)
		}
		if m := decodeMeta(meta); m != nil && m.ApprovalID == approvalID {
			return id, nil
		}
	}
	return "", rows.Err()
}

func decisionActionOf(st ApprovalStatus) (action, key string) {
	switch st {
	case ApprovalApproved:
		return DecisionApprove, "task.approvals.comments.decided_approved"
	case ApprovalChangesRequested:
		return DecisionRequestChanges, "task.approvals.comments.decided_changes"
	case ApprovalWaived:
		return DecisionWaiveApproval, "task.approvals.comments.decided_waived"
	case ApprovalPending:
	}
	return "", ""
}

// PostApprovalDecision answers the approval's thread with its decision (kind decision) as the
// person or agent who decided it, and resolves the thread once the approval is settled. An approval
// with no thread gets one first, so the decision always lands in a thread.
func (s *Store) PostApprovalDecision(ctx context.Context, a *Approval, src Source, author string) (*Comment, error) {
	action, key := decisionActionOf(a.Status)
	if action == "" {
		return nil, nil
	}
	root, err := s.approvalThread(ctx, a.TaskID, a.ID)
	if err != nil {
		return nil, err
	}
	if root == "" {
		s.openApprovalThread(ctx, *a)
		if root, err = s.approvalThread(ctx, a.TaskID, a.ID); err != nil {
			return nil, err
		}
	}
	n := NoteOf(key, "approval", a.Label)
	if note := strings.TrimSpace(a.DecidedNote); note != "" {
		n = n.Add("task.approvals.comments.decided_note", "note", quoteNote(note))
	}
	c, err := s.PostComment(ctx, NewComment{
		TaskID: a.TaskID, ThreadID: root, Source: src, Author: author, Note: &n, Kind: CommentDecision,
		Meta: &CommentMeta{Action: action, ApprovalID: a.ID},
	})
	if err != nil {
		return nil, err
	}
	if a.Status.Settled() && root != "" {
		logx.Warn(s.ResolveThread(ctx, a.TaskID, root, author, true), "task.approvals.log.resolve_thread",
			"task", a.TaskID, "approval", a.ID)
	}
	return c, nil
}

// reopenApprovalThreads opens again the threads of approvals sent back to pending, so the thread
// reads as waiting while the approval does.
func (s *Store) reopenApprovalThreads(ctx context.Context, list []Approval) {
	for i := range list {
		root, err := s.approvalThread(ctx, list[i].TaskID, list[i].ID)
		if err != nil || root == "" {
			logx.Warn(err, "task.approvals.log.reopen_thread", "approval", list[i].ID)
			continue
		}
		s.reopenThread(ctx, list[i].TaskID, root)
	}
}

func (s *Store) resolveApprovalThread(ctx context.Context, taskID, approvalID, by string) {
	root, err := s.approvalThread(ctx, taskID, approvalID)
	if err != nil || root == "" {
		logx.Warn(err, "task.approvals.log.resolve_thread", "task", taskID, "approval", approvalID)
		return
	}
	logx.Warn(s.ResolveThread(ctx, taskID, root, by, true), "task.approvals.log.resolve_thread",
		"task", taskID, "approval", approvalID)
}

// decorateApprovalThreads fills ThreadID on approvals from the task's comments (oldest first).
func decorateApprovalThreads(approvals []Approval, comments []Comment) {
	for i := range approvals {
		approvals[i].ThreadID = ApprovalThreadOf(comments, approvals[i].ID)
	}
}

// dropImportedStageApproval deletes the stage approval the import opened in place of the bundle's
// row a, with the thread that approval opened.
func (s *Store) dropImportedStageApproval(ctx context.Context, a Approval) error {
	ids, err := s.replacedStageApprovals(ctx, a)
	if err != nil {
		return apperr.Wrap(err, "task.approvals.errors.import", "id", a.ID)
	}
	for _, id := range ids {
		root, err := s.approvalThread(ctx, a.TaskID, id)
		if err != nil {
			return err
		}
		if root != "" {
			if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM task_comments WHERE task_id = ? AND (id = ? OR parent_id = ?)`),
				a.TaskID, root, root); err != nil {
				return apperr.Wrap(err, "task.approvals.errors.import", "id", a.ID)
			}
		}
		if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM task_approvals WHERE id = ?`), id); err != nil {
			return apperr.Wrap(err, "task.approvals.errors.import", "id", a.ID)
		}
	}
	return nil
}

// replacedStageApprovals lists the stage approvals of a's task, stage and key other than a itself.
func (s *Store) replacedStageApprovals(ctx context.Context, a Approval) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM task_approvals
		WHERE task_id = ? AND stage = ? AND key = ? AND source = ? AND id <> ?`),
		a.TaskID, string(a.Stage), a.Key, string(ApprovalFromStage), a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
