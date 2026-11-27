package task

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/listpage"
)

// SetMergeSHA records the commit a merge left on projectID's base branch. It reads and writes the
// map inside one transaction: the webhooks of a two-repo task can land together, and a plain Update
// of the whole map would drop one of them. changed is false when the task already had this SHA.
func (s *Store) SetMergeSHA(ctx context.Context, id, projectID, sha string) (t *Task, changed bool, err error) {
	if projectID == "" || sha == "" {
		return nil, false, apperr.Invalid("task.errors.merge_sha_fields")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, apperr.Wrap(err, "task.errors.update_begin", "id", id)
	}
	defer func() { _ = tx.Rollback() }()
	t, err = s.getRawTx(ctx, tx, id)
	if err != nil {
		return nil, false, err
	}
	if t.MergeSHAs[projectID] == sha {
		return t, false, nil
	}
	if t.MergeSHAs == nil {
		t.MergeSHAs = map[string]string{}
	}
	t.MergeSHAs[projectID] = sha
	cols, args, err := Patch{MergeSHAs: t.MergeSHAs}.assignments(time.Now().UTC())
	if err != nil {
		return nil, false, err
	}
	set := make([]string, len(cols))
	for i, c := range cols {
		set[i] = c + "=?"
	}
	// set holds only the constant column names from assignments; values travel as bind arguments.
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET `+strings.Join(set, ", ")+` WHERE id=?`), append(args, id)...); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, apperr.Wrap(err, "task.errors.update_commit", "id", id)
	}
	s.notify(ChangeUpdated, t)
	return t, true, nil
}

// FindByPRURL returns the workspace's task whose pr_urls hold url, or nil. Only workspaceID's
// tasks: pr_urls is written by agents, so a URL on another tenant's card proves nothing.
func (s *Store) FindByPRURL(ctx context.Context, workspaceID, url string) (*Task, error) {
	if workspaceID == "" || url == "" {
		return nil, nil
	}
	tasks, err := s.listWhere(ctx, `WHERE workspace_id = ?
		  AND EXISTS (SELECT 1 FROM jsonb_each_text(COALESCE(NULLIF(pr_urls, ''), '{}')::jsonb) e WHERE e.value = ?)`+
		cardOrder+` LIMIT 1`, workspaceID, url)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return &tasks[0], nil
}

// ListProjectTasksInStage lists the top-level, unarchived tasks of workspaceID linked to projectID
// that sit in stage, newest first, at most limit (0 means all).
func (s *Store) ListProjectTasksInStage(ctx context.Context, workspaceID, projectID string, stage Stage, limit int) ([]Task, error) {
	tasks, _, err := s.listCards(ctx, `workspace_id = ? AND stage = ? AND parent_id = '' AND archived_at IS NULL
		AND EXISTS (SELECT 1 FROM task_projects tp WHERE tp.task_id = tasks.id AND tp.project_id = ?)`,
		[]any{workspaceID, string(stage), projectID}, listpage.Request{Limit: limit})
	return tasks, err
}

// IsStageChanged reports whether err is UpdateIfStage finding the task in another stage.
func IsStageChanged(err error) bool {
	return errors.Is(err, ErrStageChanged)
}
