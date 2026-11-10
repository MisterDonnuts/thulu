package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type SkillGrant struct {
	Agent         string   `json:"agent"`
	Stages        []string `json:"stages,omitempty"`
	AuditorStages []string `json:"auditor_stages,omitempty"`
}

func skillGrants(agents []agentstore.Agent, name string) []SkillGrant {
	out := []SkillGrant{}
	for i := range agents {
		at := &agents[i].AgentType
		g := SkillGrant{Agent: at.Name, Stages: stagesGranting(at.SkillsByStage, name)}
		if at.Auditor != nil {
			g.AuditorStages = stagesGranting(at.Auditor.SkillsByStage, name)
		}
		if len(g.Stages) > 0 || len(g.AuditorStages) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func stagesGranting(byStage map[string][]string, name string) []string {
	var out []string
	for stage, names := range byStage {
		if slices.ContainsFunc(names, func(n string) bool { return strings.TrimSpace(n) == name }) {
			out = append(out, stage)
		}
	}
	slices.Sort(out)
	return out
}

func revokeSkill(at *types.AgentType, name string) bool {
	changed := dropFromStages(at.SkillsByStage, name)
	if at.Auditor != nil && dropFromStages(at.Auditor.SkillsByStage, name) {
		changed = true
	}
	return changed
}

func dropFromStages(byStage map[string][]string, name string) bool {
	changed := false
	for stage, names := range byStage {
		kept := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return strings.TrimSpace(n) == name })
		if len(kept) != len(names) {
			byStage[stage] = kept
			changed = true
		}
	}
	return changed
}

func (s *Server) handleSkillUsage(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	agents, err := store.ListAgents(r.Context(), tenant)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, skillGrants(agents, r.PathValue("name")))
}
