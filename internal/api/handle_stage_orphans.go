package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type PruneStagesResponse struct {
	Stages []string `json:"stages"`
	Files  []string `json:"files"`
}

func (s *Server) handlePruneAgentStages(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var dir string
	if store == nil {
		var err error
		if dir, err = s.agentDir(name); err != nil {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
	}
	settings, err := s.agentSettings(tenant, name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	pruneFiles := func() []string {
		if store != nil {
			return s.pruneStoredStageFiles(r, store, tenant, name)
		}
		return s.pruneAgentStageFiles(dir)
	}

	orphans := settings.UnknownStages
	resp := PruneStagesResponse{Stages: []string{}, Files: []string{}}
	if len(orphans) == 0 {
		// Files can outlive the labels, so the file sweep runs even with no orphan stages.
		resp.Files = pruneFiles()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Stages = orphans

	unknown := map[string]bool{}
	for _, id := range orphans {
		unknown[id] = true
	}
	settings.ValidStages = dropStageIDs(settings.ValidStages, unknown)
	settings.AuditorStages = dropStageIDs(settings.AuditorStages, unknown)
	for _, m := range []map[string]string{
		settings.TurnsByStage, settings.AuditorTurnsByStage, settings.AuditorModelByStage,
		settings.AuditorEngineByStage,
	} {
		dropStageKeys(m, unknown)
	}
	for _, m := range []map[string][]string{
		settings.SkillsByStage, settings.MCPByStage, settings.AuditorSkillsByStage,
		settings.SystemSkillsOff,
	} {
		dropStageKeys(m, unknown)
	}
	settings.UnknownStages = nil

	if store != nil {
		if err := s.putAgentSettingsRow(r.Context(), store, tenant, name, settings, actorID(r)); err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		s.invalidateCatalog(tenant)
	} else if status, err := writeAgentSettings(dir, name, settings); err != nil {
		writeError(w, r, status, err)
		return
	}
	resp.Files = pruneFiles()

	s.emit(r, audit.KindStagesPruned, "", name, "agent",
		i18n.Text("api.stages.audit.pruned_agent_config", "stages", strings.Join(orphans, ", ")), nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) pruneAgentStageFiles(dir string) []string {
	removed := []string{}
	for _, set := range agentPromptDirs {
		for _, base := range s.orphanStageFiles(filepath.Join(dir, set.sub), ".md", set.fallback) {
			rel := set.sub + "/" + base
			if err := os.Remove(filepath.Join(dir, set.sub, base)); err == nil {
				removed = append(removed, rel)
			}
		}
	}
	sort.Strings(removed)
	return removed
}

func (s *Server) pruneStoredStageFiles(r *http.Request, store *agentstore.Store, tenant, name string) []string {
	removed := []string{}
	files, err := store.ListOwner(r.Context(), tenant, agentstore.KindAgent, name)
	if err != nil {
		return removed
	}
	for i := range files {
		if !s.orphanStagePath(files[i].Path) {
			continue
		}
		if err := store.DeleteFile(r.Context(), tenant, agentstore.KindAgent, name, files[i].Path, actorID(r)); err == nil {
			removed = append(removed, files[i].Path)
		}
	}
	return removed
}

func (s *Server) orphanStagePath(p string) bool {
	for _, set := range agentPromptDirs {
		base, ok := strings.CutPrefix(p, set.sub+"/")
		if !ok || strings.Contains(base, "/") || !strings.HasSuffix(base, ".md") {
			continue
		}
		stem := strings.TrimSuffix(base, ".md")
		return stem != set.fallback && types.ValidStageID(stem) && !s.catalogHas(types.Stage(stem))
	}
	return false
}

// "main" and "default" are valid stage slugs; without the reserved fallback they would read as
// orphan stages.
var agentPromptDirs = []struct{ sub, fallback string }{
	{"prompts", "main"},
	{"auditor", "default"},
}

func (s *Server) orphanStageFiles(dir, ext, fallback string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ext)
		if stem == fallback || !types.ValidStageID(stem) || s.catalogHas(types.Stage(stem)) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

func (s *Server) handlePruneProjectStageFiles(w http.ResponseWriter, r *http.Request) {
	stageDir, err := s.projectStageDir(r, r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	resp := PruneStagesResponse{Stages: []string{}, Files: []string{}}
	seen := map[string]bool{}
	for _, kind := range []struct{ sub, ext string }{{"prompts", ".md"}, {"hooks", ".sh"}} {
		for _, base := range s.orphanStageFiles(filepath.Join(stageDir, kind.sub), kind.ext, "") {
			if err := os.Remove(filepath.Join(stageDir, kind.sub, base)); err != nil {
				continue
			}
			resp.Files = append(resp.Files, kind.sub+"/"+base)
			if stem := strings.TrimSuffix(base, kind.ext); !seen[stem] {
				seen[stem] = true
				resp.Stages = append(resp.Stages, stem)
			}
		}
	}
	sort.Strings(resp.Files)
	sort.Strings(resp.Stages)
	if len(resp.Files) > 0 {
		s.emit(r, audit.KindStagesPruned, "", r.PathValue("id"), "project",
			i18n.Text("api.stages.audit.pruned_project_files", "files", strings.Join(resp.Files, ", ")), nil)
	}
	writeJSON(w, http.StatusOK, resp)
}

func dropStageIDs(ids []string, unknown map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !unknown[id] {
			out = append(out, id)
		}
	}
	return out
}

func dropStageKeys[V any](m map[string]V, unknown map[string]bool) {
	for id := range m {
		if unknown[id] {
			delete(m, id)
		}
	}
}
