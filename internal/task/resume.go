package task

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Unlike ContinueStage, the run's end is audited and may advance. The kind travels on the run's
// label so Reconcile after a restart finishes it the same way.
func (d *Dispatcher) ResumeStage(ctx context.Context, t *Task, message string, kind LaunchKind) error {
	return d.resumeStage(ctx, t, message, kind, nil)
}

// onAbort runs if the async launch gives up before a container starts; release any claim there,
// since a nil error only means the launch was handed off.
func (d *Dispatcher) resumeStage(ctx context.Context, t *Task, message string, kind LaunchKind, onAbort func()) error {
	if t == nil {
		return apperr.Internal("task.errors.nil_task")
	}
	if kind != LaunchAnswerResume && kind != LaunchChangesResume {
		return apperr.Invalid("task.sessions.errors.not_a_resume_kind", "kind", kind)
	}
	if strings.TrimSpace(message) == "" {
		return apperr.Invalid("task.errors.empty_message")
	}
	if t.Status == StatusRunning && t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		return apperr.Conflict("task.errors.run_in_progress")
	}
	agentName, session := d.stageResumeTarget(t)
	if agentName == "" {
		return apperr.Precondition("task.sessions.errors.no_agent_to_resume", "stage", d.stageNote(t, t.Stage))
	}
	agentType := d.launcher.FindAgentType(t.WorkspaceID, agentName)
	if agentType == nil {
		return apperr.NotFound("task.errors.agent_not_found", "agent", agentName)
	}
	if info, _ := d.stagesFor(t).Get(t.Stage); len(t.ProjectIDs) > 0 && !info.Initial {
		t = d.ensureWorktrees(ctx, t)
	}
	go d.launchStage(ctx, t, t.Stage, agentName, agentType, launchOpts{
		Kind:    kind,
		Session: session,
		Message: message,
		onAbort: onAbort,
	})
	return nil
}

func (d *Dispatcher) resumeFailed(ctx context.Context, t *Task, why Note, err error) {
	if !why.Empty() {
		d.systemComment(ctx, t.ID, why)
	}
	if apperr.KindOf(err) == apperr.KindConflict {
		return
	}
	d.setStatus(ctx, t.ID, StatusResumeFailed)
}

func (d *Dispatcher) stageResumeTarget(t *Task) (agentName, session string) {
	sessionStage := Stage(t.SessionStage)
	if sessionStage == "" {
		sessionStage = t.Stage
	}
	if t.SessionID != "" && t.SessionAgent != "" && sessionStage == t.Stage &&
		d.launcher.FindAgentType(t.WorkspaceID, t.SessionAgent) != nil {
		return t.SessionAgent, t.SessionID
	}
	return t.AssignedAgent(t.Stage), ""
}

type StalledTask struct {
	TaskID    string    `json:"task_id"`
	TaskTitle string    `json:"task_title"`
	TaskStage string    `json:"task_stage"`
	BoardID   string    `json:"board_id"`
	Status    RunStatus `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) ListStalledTasks(ctx context.Context, workspaceID string, unrestricted bool) ([]StalledTask, error) {
	q := `SELECT id, title, stage, board_id, status, updated_at FROM tasks
	       WHERE archived_at IS NULL AND status IN (` + placeholderList(len(stalledStatuses)) + `)`
	args := make([]any, 0, len(stalledStatuses)+1)
	for _, st := range stalledStatuses {
		args = append(args, string(st))
	}
	if !unrestricted {
		q += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(q+` ORDER BY updated_at ASC, id ASC LIMIT `+strconv.Itoa(InboxLimit)), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_stalled")
	}
	defer rows.Close()
	out := []StalledTask{}
	for rows.Next() {
		var st StalledTask
		if err := rows.Scan(&st.TaskID, &st.TaskTitle, &st.TaskStage, &st.BoardID, &st.Status, &st.UpdatedAt); err != nil {
			return nil, apperr.Wrap(err, "task.errors.list_stalled")
		}
		st.UpdatedAt = st.UpdatedAt.UTC()
		out = append(out, st)
	}
	return out, rows.Err()
}
