package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

// stageMove is one stage change made by the daemon or a person. Every move outside the
// dispatcher goes through moveTaskStage or, for the PATCH handler that runs its own checks,
// stageMoved, so each one leaves the same trail.
type stageMove struct {
	To    task.Stage
	Cause task.GateCause
	// Recorded as the stage_entered payload's "via"; the send-back count reads it.
	Via string
	// Who moved it, as the timeline shows it: "github:ana", "automation", a person's label.
	Actor string
	// Only a person's move resets the board's send-back count.
	ByPerson bool
	// Merged into the stage_entered payload: merge_sha, deployed_sha, pr_url…
	Extra map[string]any
	// Merged into the task.stage_changed automation event: an automation's move stamps its id and
	// chain depth there, or two recipes that move a task back and forth never stop.
	EventExtra map[string]any
	// For an administrative cause: the note on each approval the move waives. Empty uses the
	// generic one.
	WaiveNote string
	// The task was just created and this move places it: like POST /tasks with a stage, it
	// publishes no task.stage_changed and has no questions to cancel.
	Placement bool
	// Refuse with task.ErrRunInFlight while a run is starting or running on the task, instead of
	// moving it out from under that run.
	Fenced bool
	// Set only by the PATCH handler: the audit entry and the question cancel name the caller.
	req *http.Request
}

// moveTaskStage validates the destination, applies the stage-exit gate with m.Cause and moves the
// task only if it is still in the stage it was read in. A closed gate is not an error: it returns
// the task unmoved with the verdict, for the caller to report.
func (s *Server) moveTaskStage(ctx context.Context, t *task.Task, m stageMove) (*task.Task, task.GateVerdict, error) {
	pass := task.GateVerdict{Passed: true}
	if t == nil || t.Stage == m.To {
		return t, pass, nil
	}
	if !s.boards.Board(t.BoardID).Valid(m.To) {
		return t, pass, apperr.Invalid("api.tasks.errors.invalid_stage", "stage", m.To)
	}
	if !s.stagesFor(t).Valid(m.To) {
		return t, pass, apperr.Conflict("api.tasks.errors.stage_skipped_by_type", "stage", m.To, "type", t.TypeID)
	}
	from := t.Stage
	v, err := s.taskStore.StageExitGate(ctx, t, from, m.To, m.Cause)
	if err != nil {
		return t, v, err
	}
	if !v.Passed {
		return t, v, nil
	}
	if m.Cause.Administrative() {
		// Must run before the move: afterwards the task is no longer in from.
		s.recordSkippedApprovalsNote(ctx, t, from, m.To, m.Cause, m.Actor, m.WaiveNote)
	}
	idle := task.StatusIdle
	patch := task.Patch{Stage: &m.To, Status: &idle, By: m.Actor}
	var updated *task.Task
	if m.Fenced && s.taskDispatcher != nil {
		updated, err = s.taskDispatcher.UpdateIfStageFenced(ctx, t.ID, from, patch)
	} else {
		updated, err = s.taskStore.UpdateIfStage(ctx, t.ID, from, patch)
	}
	if err != nil {
		return t, v, err
	}
	s.stageMoved(ctx, t, updated, m)
	return updated, v, nil
}

// stageMoved leaves the trail of a move already written: the timeline event, the audit entry and
// webhook, the automation event, the canceled questions, and the next stage's launch.
func (s *Server) stageMoved(ctx context.Context, prev, updated *task.Task, m stageMove) {
	id := updated.ID
	byPerson := "false"
	if m.ByPerson {
		byPerson = "true"
	}
	payload := map[string]any{"from": string(prev.Stage), "to": string(updated.Stage), "via": m.Via, "by_person": byPerson}
	for k, v := range m.Extra {
		payload[k] = v
	}
	s.recordEvent(ctx, id, task.EventStageEntered, "", m.Actor, fmt.Sprintf("%s → %s", prev.Stage, updated.Stage), payload)
	launch := context.WithoutCancel(ctx)
	if m.req != nil {
		launch = launchCtx(ctx)
	}
	if !m.Placement {
		s.auditStageChange(ctx, prev, updated, m)
		event := map[string]any{"from": string(prev.Stage), "to": string(updated.Stage)}
		for k, v := range m.EventExtra {
			event[k] = v
		}
		s.publishTaskAutomationEvent(context.WithoutCancel(ctx), automation.EventTaskStageChanged, updated, event)
		s.cancelQuestionsOnMove(ctx, updated, m)
	}
	s.taskDispatcher.HandleStageEntered(launch, updated, prev.Stage)
}

func (s *Server) auditStageChange(ctx context.Context, prev, updated *task.Task, m stageMove) {
	summary := i18n.Text("api.tasks.audit.task_stage_changed", "task", updated.ID, "from", prev.Stage, "to", updated.Stage)
	data := map[string]string{"task_id": updated.ID, "from": string(prev.Stage), "to": string(updated.Stage)}
	if m.req != nil {
		s.emit(m.req, audit.KindTaskStageChanged, webhook.EventTaskStageChanged, updated.ID, "task", summary, data)
		return
	}
	data["via"] = m.Via
	s.emitSystem(ctx, updated.WorkspaceID, audit.KindTaskStageChanged, webhook.EventTaskStageChanged, updated.ID, "task", summary, data)
}

func (s *Server) cancelQuestionsOnMove(ctx context.Context, t *task.Task, m stageMove) {
	if m.req != nil {
		s.cancelQuestionsFor(m.req, t, "task.questions.reply.canceled_moved")
		return
	}
	if s.taskDispatcher == nil {
		return
	}
	closed := s.taskDispatcher.CancelOpenQuestions(context.WithoutCancel(ctx), t.ID, m.Actor, "task.questions.reply.canceled_moved")
	for i := range closed {
		q := &closed[i]
		s.emitSystem(ctx, t.WorkspaceID, audit.KindQuestionCanceled, webhook.EventQuestionCanceled, q.ID, "question",
			i18n.Text("api.questions.audit.canceled", "task", t.ID, "agent", q.Agent), questionHookData(t, q))
	}
}
