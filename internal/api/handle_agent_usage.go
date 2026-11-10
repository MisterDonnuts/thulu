package api

import (
	"net/http"
	"sort"
)

type agentUsage struct {
	Tasks    int                 `json:"tasks"`
	Projects []agentUsageProject `json:"projects"`
}

type agentUsageProject struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Stages []string `json:"stages"`
}

func (s *Server) handleAgentUsage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	wid, unrestricted := s.callerWorkspace(r)
	out := agentUsage{Projects: []agentUsageProject{}}
	if s.taskStore != nil {
		n, err := s.taskStore.CountAssignedTo(r.Context(), wid, unrestricted, name)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		out.Tasks = n
	}
	if s.projectStore != nil {
		projects, err := s.projectStore.List(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		for i := range projects {
			p := &projects[i]
			if !workspaceVisible(p.WorkspaceID, wid, unrestricted) {
				continue
			}
			if stages := templateStagesFor(p.StageTemplate, name); len(stages) > 0 {
				out.Projects = append(out.Projects, agentUsageProject{ID: p.ID, Name: p.Name, Stages: stages})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func templateStagesFor(tpl map[string]string, agent string) []string {
	var stages []string
	for stage, a := range tpl {
		if a == agent {
			stages = append(stages, stage)
		}
	}
	sort.Strings(stages)
	return stages
}
