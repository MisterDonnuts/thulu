package api

import (
	"context"
	"maps"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// resolveAssignments layers who runs each stage of a new task, lowest first: the column's default
// agent, the projects' stage templates, the task type's agent on that board (an empty name clears
// the stage), and what the request names. The type beats the project so a feature does not inherit
// the release agent a project template puts on the release column.
func resolveAssignments(b stagepkg.Boards, boardID, typeID string, templates, request map[task.Stage]string) map[task.Stage]string {
	columns, byType := stagepkg.BoardAgents(b, boardID, typeID)
	out := map[task.Stage]string{}
	maps.Copy(out, columns)
	for st, agent := range templates {
		if agent != "" {
			out[st] = agent
		}
	}
	for st, agent := range byType {
		if agent == "" {
			delete(out, st)
		} else {
			out[st] = agent
		}
	}
	maps.Copy(out, request)
	return out
}

// First project wins on a stage both name. The second result names a project the caller cannot
// see, reported exactly like a missing one.
func (s *Server) projectStageTemplates(r *http.Request, projectIDs []string) (map[task.Stage]string, string) {
	wid, unrestricted := s.callerWorkspace(r)
	out := map[task.Stage]string{}
	for _, pid := range projectIDs {
		proj, err := s.projectStore.Get(r.Context(), pid)
		if err != nil || !workspaceVisible(proj.WorkspaceID, wid, unrestricted) {
			return nil, pid
		}
		addTemplate(out, proj.StageTemplate)
	}
	return out, ""
}

func addTemplate(out map[task.Stage]string, template map[string]string) {
	for k, v := range template {
		if _, ok := out[task.Stage(k)]; !ok {
			out[task.Stage(k)] = v
		}
	}
}

// For a task that already exists: its projects were checked when they were attached.
func (s *Server) taskStageTemplates(ctx context.Context, projectIDs []string) map[task.Stage]string {
	out := map[task.Stage]string{}
	if s.projectStore == nil {
		return out
	}
	for _, pid := range projectIDs {
		if proj, err := s.projectStore.Get(ctx, pid); err == nil {
			addTemplate(out, proj.StageTemplate)
		}
	}
	return out
}

// retypeAssignments swaps a task's agents when its type changes on the same board: each stage where
// some type of the board sets its own agent, and where the task still has what the old type gave
// it, gets what the new type gives. An assignment someone changed by hand stays. A request that
// sends its own stage_assignments decides alone.
func (s *Server) retypeAssignments(ctx context.Context, prev *task.Task, p *task.Patch) {
	if p.TypeID == nil || *p.TypeID == prev.TypeID || p.StageAssignments != nil {
		return
	}
	board := effectiveBoard(prev, *p)
	if board != prev.BoardID {
		return
	}
	stages := stagepkg.TypeAgentStages(s.boards, board)
	if len(stages) == 0 {
		return
	}
	templates := s.taskStageTemplates(ctx, prev.ProjectIDs)
	before := resolveAssignments(s.boards, board, prev.TypeID, templates, nil)
	after := resolveAssignments(s.boards, board, *p.TypeID, templates, nil)
	out := maps.Clone(prev.StageAssignments)
	if out == nil {
		out = map[task.Stage]string{}
	}
	changed := false
	for st := range stages {
		if prev.StageAssignments[st] != before[st] || before[st] == after[st] {
			continue
		}
		changed = true
		if after[st] == "" {
			delete(out, st)
		} else {
			out[st] = after[st]
		}
	}
	if changed {
		p.StageAssignments = out
	}
}

// checkColumnAgent refuses a default agent the board's workspace does not have, or one that does
// not run in that stage. Empty clears the default and always passes.
func (s *Server) checkColumnAgent(workspaceID string, stage types.Stage, agent string) error {
	if agent == "" {
		return nil
	}
	at := types.FindAgent(s.agents(workspaceID), agent)
	if at == nil {
		return apperr.Invalid("api.boards.errors.agent_unknown", "agent", agent, "stage", stage)
	}
	if len(at.ValidStages) > 0 && !at.SupportsStage(stage) {
		return apperr.Invalid("api.boards.errors.agent_wrong_stage", "agent", agent, "stage", stage)
	}
	return nil
}

// checkTypeAgents checks the agents a type list sets that differ from what the board has now, so
// an agent deleted since does not block saving an unrelated change.
func (s *Server) checkTypeAgents(workspaceID string, incoming, current []stagepkg.TaskTypeInfo) error {
	had := make(map[string]map[types.Stage]string, len(current))
	for i := range current {
		had[current[i].ID] = current[i].Agents
	}
	for i := range incoming {
		t := &incoming[i]
		for st, agent := range t.Agents {
			if had[t.ID][st] == agent {
				continue
			}
			if err := s.checkColumnAgent(workspaceID, st, agent); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkStageAgents checks the default agents a stage list sets that differ from the board's.
func (s *Server) checkStageAgents(workspaceID string, incoming, current []types.StageInfo) error {
	had := make(map[types.Stage]string, len(current))
	for i := range current {
		had[current[i].ID] = current[i].DefaultAgent
	}
	for i := range incoming {
		st := &incoming[i]
		if st.DefaultAgent == had[st.ID] {
			continue
		}
		if err := s.checkColumnAgent(workspaceID, st.ID, st.DefaultAgent); err != nil {
			return err
		}
	}
	return nil
}
