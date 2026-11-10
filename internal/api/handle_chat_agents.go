package api

import (
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type chatAgentView struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// False when the agent has neither prompts/chat.md nor prompts/main.md; a chat turn refuses
	// that up front.
	PromptReady bool     `json:"prompt_ready"`
	LLMReady    bool     `json:"llm_ready"`
	LLMError    string   `json:"llm_error,omitempty"`
	MCP         []string `json:"mcp"`
}

func (s *Server) handleListChatAgents(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	wid := tenant
	if wid == "" {
		if wid, ok = s.targetWorkspace(w, r); !ok {
			return
		}
	}
	all, err := s.agents(tenant).All()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	stage := string(types.StageChat)
	out := []chatAgentView{} // never null: the form iterates unconditionally
	for i := range all {
		at := all[i]
		if !at.SupportsChat() {
			continue
		}
		v := chatAgentView{
			Type: at.Type, Name: at.Name, Description: at.Description,
			PromptReady: s.agentHasPrompt(r.Context(), tenant, at.Name, stage),
			LLMReady:    true,
			MCP:         nonNil(at.MCPByStage[stage]),
		}
		if s.taskDispatcher != nil {
			if err := s.taskDispatcher.LLMCredentialReady(r.Context(), wid, &at, at.Name); err != nil {
				v.LLMReady = false
				v.LLMError = apperr.Localize(err, i18n.RequestLocale(r))
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}
