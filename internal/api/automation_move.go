package api

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// MoveTask is the automation move step. It follows the rules of a release batch: the stage's exit
// gate holds (GateAutomation is not administrative), a move that would jump a stage asking for an
// approval is held the same way, and a running, archived or other-board task is refused. A move that
// wakes the destination stage's agent asks req.BeforeAgent first (the recipe's daily budget), and
// the task.stage_changed it causes carries the automation and its chain depth.
func (a *automationTasks) MoveTask(ctx context.Context, req automation.MoveRequest) (automation.MoveResult, error) {
	t, err := a.taskInWorkspace(ctx, req.WorkspaceID, req.TaskID)
	if err != nil {
		return automation.MoveResult{}, err
	}
	to := task.Stage(req.Stage)
	res := automation.MoveResult{TaskID: t.ID, Title: t.Title, From: string(t.Stage), To: req.Stage}
	if req.BoardID != "" && t.BoardID != req.BoardID {
		return res, apperr.Conflict("api.automations.errors.move_other_board", "task", t.ID, "board", req.BoardID, "actual", t.BoardID)
	}
	if t.Stage == to {
		return res, nil
	}
	if refusal := movable(t); refusal != nil {
		return res, refusal
	}
	if jumped := task.JumpedRequirements(a.s.stagesFor(t), t.TypeID, t.Stage, to); len(jumped) > 0 {
		res.Gated, res.Approvals = true, jumped
		return res, nil
	}
	agent := a.wakes(t, to)
	if agent != "" && req.BeforeAgent != nil {
		if refusal := req.BeforeAgent(agent); refusal != nil {
			return res, refusal
		}
	}
	extra := map[string]any{automation.PayloadAutomationID: req.AutomationID}
	if req.Note != "" {
		extra["note"] = req.Note
	}
	_, v, err := a.s.moveTaskStage(ctx, t, stageMove{To: to, Cause: task.GateAutomation, Via: task.ViaAutomation,
		Actor: "automation", Extra: extra, EventExtra: map[string]any{
			automation.PayloadAutomationID: req.AutomationID,
			automation.PayloadChainDepth:   req.ChainDepth,
		}})
	switch {
	case task.IsStageChanged(err):
		return res, apperr.Conflict("api.automations.errors.move_raced", "task", t.ID)
	case err != nil:
		return res, err
	case !v.Passed:
		res.Gated = true
		for i := range v.Blocking {
			res.Approvals = append(res.Approvals, v.Blocking[i].Label)
		}
		return res, nil
	}
	res.Moved, res.Agent = true, agent
	if req.Note != "" {
		stage := a.s.taskDispatcher.StageNote(t, to)
		n := task.NoteOf("api.automations.comments.moved", "stage", stage, "note", req.Note)
		if _, err := a.s.taskStore.AddNote(ctx, t.ID, "", task.SourceAutomation, "automation", n); err != nil {
			logx.Warn(err, "api.automations.log.move_note", "task", t.ID)
		}
	}
	return res, nil
}

// wakes is the agent the dispatcher launches when t enters stage: its assignment there, unless the
// stage is terminal (HandleStageEntered).
func (a *automationTasks) wakes(t *task.Task, stage task.Stage) string {
	if info, ok := a.s.stagesFor(t).Get(stage); !ok || info.Terminal {
		return ""
	}
	return t.AssignedAgent(stage)
}

func movable(t *task.Task) error {
	switch {
	case t.ArchivedAt != nil:
		return apperr.Conflict("api.automations.errors.move_archived", "task", t.ID)
	case t.Status == task.StatusRunning:
		// Moving it would orphan the agent working on it.
		return apperr.Conflict("api.automations.errors.move_running", "task", t.ID)
	}
	return nil
}
