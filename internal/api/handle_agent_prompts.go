package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type PromptEntry struct {
	Stage    string `json:"stage"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Fallback string `json:"fallback,omitempty"`
	Orphan   bool   `json:"orphan,omitempty"`
}

// prompts/{stage|main}.md is read by prompt-init.sh; auditor/{stage|default}.md by auditor.sh.
// Change them together.
type PromptSet struct {
	Kind           string        `json:"kind"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Dir            string        `json:"dir"`
	FallbackName   string        `json:"fallback_name"`
	FallbackPath   string        `json:"fallback_path"`
	FallbackExists bool          `json:"fallback_exists"`
	Stages         []PromptEntry `json:"stages"`
}

type PromptsResponse struct {
	Sets  []PromptSet `json:"sets"`
	Other []string    `json:"other"`
}

func (s *Server) handleListAgentPrompts(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var (
		exists func(rel string) bool
		paths  []string
	)
	if store != nil {
		if _, err := store.GetAgent(r.Context(), tenant, name); err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		files, err := store.ListOwner(r.Context(), tenant, agentstore.KindAgent, name)
		if err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		have := make(map[string]bool, len(files))
		for i := range files {
			have[files[i].Path] = true
			paths = append(paths, files[i].Path)
		}
		exists = func(rel string) bool { return have[rel] }
	} else {
		dir, err := s.agentDir(name)
		if err != nil {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		exists = func(rel string) bool {
			_, statErr := os.Stat(filepath.Join(dir, rel))
			return statErr == nil
		}
		if paths, err = collectAgentFiles(dir); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}

	// Use only the caller's boards: another tenant's column labels must not leak. mention and chat
	// are on no board but load prompts/mention.md and prompts/chat.md.
	board := s.visibleCatalog(r)
	ws := tenant
	if store == nil {
		ws, _ = s.callerWorkspace(r)
	}
	agentSlots, boardSlots := s.promptSlots(board, ws, name)

	sets := []PromptSet{
		buildPromptSet(agentSlots, board, paths, exists, "agent",
			i18n.TCtx(r.Context(), "api.agents.labels.agent_prompts"),
			i18n.TCtx(r.Context(), "api.agents.hints.agent_prompts"),
			"prompts", "main.md"),
		buildPromptSet(boardSlots, board, paths, exists, "auditor",
			i18n.TCtx(r.Context(), "api.agents.labels.auditor_prompts"),
			i18n.TCtx(r.Context(), "api.agents.hints.auditor_prompts"),
			"auditor", "default.md"),
	}

	covered := map[string]bool{}
	for i := range sets {
		covered[sets[i].FallbackPath] = true
		for _, e := range sets[i].Stages {
			covered[e.Path] = true
		}
	}
	other := []string{}
	for _, rel := range paths {
		if editableFile(rel) && !covered[rel] {
			other = append(other, rel)
		}
	}
	sort.Strings(other)
	writeJSON(w, http.StatusOK, PromptsResponse{Sets: sets, Other: other})
}

func collectAgentFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			if isRuntimeDir(filepath.Base(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

func (s *Server) promptSlots(board []types.StageInfo, ws, name string) (agentSlots, auditorSlots []types.StageInfo) {
	var declared []types.Stage
	if at, found := s.agentTypeByName(ws, name); found {
		declared = at.ValidStages
	}
	auditorSlots = agentBoardStages(board, declared)
	virtual := agentBoardStages(stagepkg.Virtuals(), declared)
	agentSlots = make([]types.StageInfo, 0, len(auditorSlots)+len(virtual))
	agentSlots = append(agentSlots, auditorSlots...)
	agentSlots = append(agentSlots, virtual...)
	return agentSlots, auditorSlots
}

// Must match types.AgentType.AnswersMentions: an agent declaring no stages runs on every stage.
func agentBoardStages(board []types.StageInfo, declared []types.Stage) []types.StageInfo {
	if len(declared) == 0 {
		return board
	}
	out := make([]types.StageInfo, 0, len(board))
	for i := range board {
		if slices.Contains(declared, board[i].ID) {
			out = append(out, board[i])
		}
	}
	return out
}

func buildPromptSet(
	slots, board []types.StageInfo, paths []string, exists func(rel string) bool,
	kind, title, desc, sub, fallbackName string,
) PromptSet {
	fallbackRel := sub + "/" + fallbackName
	fallbackExists := exists(fallbackRel)

	stages := make([]PromptEntry, 0, len(slots))
	slotted := make(map[string]bool, len(slots))
	for i := range slots {
		st := &slots[i]
		rel := sub + "/" + string(st.ID) + ".md"
		slotted[rel] = true
		has := exists(rel)
		entry := PromptEntry{
			Stage:  string(st.ID),
			Label:  st.Label,
			Path:   rel,
			Exists: has,
		}
		if !has && fallbackExists {
			entry.Fallback = fallbackRel
		}
		stages = append(stages, entry)
	}

	return PromptSet{
		Kind:           kind,
		Title:          title,
		Description:    desc,
		Dir:            sub,
		FallbackName:   fallbackName,
		FallbackPath:   fallbackRel,
		FallbackExists: fallbackExists,
		Stages:         append(stages, orphanPrompts(board, paths, sub, fallbackRel, slotted)...),
	}
}

func orphanPrompts(board []types.StageInfo, paths []string, sub, fallbackRel string, slotted map[string]bool) []PromptEntry {
	labels := make(map[string]string, len(board))
	for i := range board {
		labels[string(board[i].ID)] = board[i].Label
	}
	var out []PromptEntry
	for _, rel := range paths {
		name, inSub := strings.CutPrefix(rel, sub+"/")
		id, isMD := strings.CutSuffix(name, ".md")
		if !inSub || !isMD || rel == fallbackRel || slotted[rel] || !types.ValidStageID(id) {
			continue
		}
		label := labels[id]
		if label == "" {
			label = id
		}
		out = append(out, PromptEntry{Stage: id, Label: label, Path: rel, Exists: true, Orphan: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
