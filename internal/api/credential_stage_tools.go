package api

import (
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// nil means unrestricted; an empty non-nil list allows nothing. The run key's agent and stage
// (including mention, chat) win over the task's current stage, which may have moved on.
func (s *Server) credentialStageTools(scope auth.RunScope, t *task.Task) []string {
	if s.taskDispatcher == nil || t == nil {
		return nil
	}
	agentName, stage := scope.Agent, scope.Stage
	if agentName == "" {
		agentName, stage = t.AssignedAgent(t.Stage), string(t.Stage)
	}
	if stage == "" {
		stage = string(t.Stage)
	}
	if agentName == "" {
		return nil
	}
	at := s.taskDispatcher.FindAgentTypeForRelaunch(t.WorkspaceID, agentName)
	if at == nil {
		return []string{}
	}
	entries := at.MCPByStage[stage]
	if entries == nil {
		return []string{}
	}
	return entries
}
