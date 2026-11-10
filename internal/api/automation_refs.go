package api

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func (s *Server) checkAutomationRefs(ctx context.Context, ws string, spec *automation.Spec) error {
	if err := automation.CheckFilterFields(&spec.Trigger); err != nil {
		return err
	}
	if m := spec.Trigger.Metric; m != nil && !s.environmentInWorkspace(ctx, ws, m.Environment) {
		return apperr.Invalid("api.automations.errors.environment_not_found", "environment", m.Environment)
	}
	return s.checkStepRefs(ctx, ws, spec.Steps)
}

func (s *Server) checkStepRefs(ctx context.Context, ws string, steps []automation.Step) error {
	for i := range steps {
		st := &steps[i]
		var err error
		switch {
		case st.Task != nil:
			err = s.checkTaskStepRefs(ctx, ws, st.Task)
		case st.Mention != nil:
			err = s.checkAgentRef(ws, "agent", st.Mention.Agent)
		case st.Query != nil:
			err = s.checkQueryStepRefs(ctx, ws, st.Query)
		case st.Fanout != nil:
			err = s.checkStepRefs(ctx, ws, st.Steps)
		}
		if err != nil {
			if st.Fanout != nil {
				return err
			}
			return apperr.Wrap(err, "automation.errors.step_failed", "step", st.ID)
		}
	}
	return nil
}

func (s *Server) checkTaskStepRefs(ctx context.Context, ws string, t *automation.TaskStep) error {
	board, err := s.checkBoardRef(ws, "board", t.Board)
	if err != nil {
		return err
	}
	if err := s.checkStageRef(board, "stage", t.Stage); err != nil {
		return err
	}
	for stage, agent := range t.StageAssignments {
		if err := s.checkStageRef(board, "stage_assignments", stage); err != nil {
			return err
		}
		if err := s.checkAgentRef(ws, "stage_assignments", agent); err != nil {
			return err
		}
	}
	for _, pid := range t.ProjectIDs {
		if err := s.checkProjectRef(ctx, ws, "project_ids", pid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) checkQueryStepRefs(ctx context.Context, ws string, q *automation.QueryStep) error {
	if q.Source == "metric" {
		if err := literalRef("environment", q.Environment); err != nil {
			return err
		}
		if s.environmentInWorkspace(ctx, ws, q.Environment) {
			return nil
		}
		return apperr.Invalid("api.automations.errors.environment_not_found", "environment", q.Environment)
	}
	if q.Board != "" {
		board, err := s.checkBoardRef(ws, "board", q.Board)
		if err != nil {
			return err
		}
		if err := s.checkStageRef(board, "stage", q.Stage); err != nil {
			return err
		}
	}
	return s.checkProjectRef(ctx, ws, "project_id", q.ProjectID)
}

func (s *Server) checkBoardRef(ws, field, id string) (string, error) {
	if err := literalRef(field, id); err != nil {
		return "", err
	}
	if s.boards == nil {
		return "", nil
	}
	if id == "" {
		return s.boards.DefaultBoardIDFor(ws), nil
	}
	if b, ok := s.boardInfo(id); !ok || !workspaceVisible(b.WorkspaceID, ws, false) {
		return "", apperr.Invalid("api.automations.errors.unknown_board", "board", id)
	}
	return id, nil
}

func (s *Server) checkStageRef(board, field, stage string) error {
	if err := literalRef(field, stage); err != nil {
		return err
	}
	if board == "" || stage == "" {
		return nil
	}
	if _, ok := s.boards.Board(board).Get(types.Stage(stage)); !ok {
		return apperr.Invalid("api.automations.errors.unknown_stage", "stage", stage, "board", board)
	}
	return nil
}

func (s *Server) checkAgentRef(ws, field, agent string) error {
	if err := literalRef(field, agent); err != nil {
		return err
	}
	handle := strings.TrimPrefix(agent, "@")
	if handle == "" || !s.hasAgentSource() || s.findAgentByHandle(ws, handle) != nil {
		return nil
	}
	return apperr.Invalid("api.automations.errors.agent_not_found", "agent", handle)
}

func (s *Server) checkProjectRef(ctx context.Context, ws, field, id string) error {
	if err := literalRef(field, id); err != nil {
		return err
	}
	if id == "" || s.projectStore == nil {
		return nil
	}
	proj, err := s.projectStore.Get(ctx, id)
	if err != nil || !workspaceVisible(proj.WorkspaceID, ws, false) {
		return apperr.Invalid("api.automations.errors.project_not_found", "id", id)
	}
	return nil
}

func (s *Server) hasAgentSource() bool {
	return s.agentSource != nil || s.agentStore != nil || s.agentCatalog != nil
}

// The engine renders templates only in text fields (title, body, fingerprint, task_id, URLs,
// scripts, MCP args). Boards, stages, agents, projects and environments reach the task store as
// written, so a "{{ }}" in one of them would only surface at run time as "no such board".
func literalRef(field, v string) error {
	if strings.Contains(v, "{{") {
		return apperr.Invalid("api.automations.errors.ref_templated", "field", field, "value", v)
	}
	return nil
}
