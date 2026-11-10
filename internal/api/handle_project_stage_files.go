package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Sidecar layout (prompts/<stage>.md, hooks/<stage>.sh) is mounted at
// /workspace/projects/<name>/.thulu/ and read by project-stage.sh; change both together.

type ProjectStageEntry struct {
	Stage  string `json:"stage"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type ProjectStageSet struct {
	Kind        string              `json:"kind"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Stages      []ProjectStageEntry `json:"stages"`
}

type ProjectStageFilesResponse struct {
	StageDir string            `json:"stage_dir"`
	Sets     []ProjectStageSet `json:"sets"`
	Orphans  []string          `json:"orphans,omitempty"`
}

// Only these two subdirs; callers never choose an arbitrary path inside the sidecar.
func validKind(kind string) bool {
	return kind == "prompts" || kind == "hooks"
}

// Stage check is shape-only (slug, cannot traverse) so files of a removed stage stay readable and
// deletable.
func stageFilePath(stageDir, kind, stage string) string {
	if !validKind(kind) || !types.ValidStageID(stage) {
		return ""
	}
	ext := ".md"
	if kind == "hooks" {
		ext = ".sh"
	}
	return filepath.Join(stageDir, kind, stage+ext)
}

// Out-of-scope projects must look exactly like missing ones.
func (s *Server) projectStageDir(r *http.Request, id string) (string, error) {
	proj, err := s.projectStore.Get(r.Context(), id)
	if err != nil {
		return "", err
	}
	if wid, unrestricted := s.callerWorkspace(r); !workspaceVisible(proj.WorkspaceID, wid, unrestricted) {
		return "", apperr.NotFound("api.projects.errors.project_not_found", "project", id)
	}
	return s.projectStore.EnsureStageDirs(proj)
}

func (s *Server) handleListProjectStageFiles(w http.ResponseWriter, r *http.Request) {
	stageDir, err := s.projectStageDir(r, r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}

	// Use the caller's columns, not the install's: other tenants' column labels must not leak.
	board := s.visibleCatalog(r)

	sets := []ProjectStageSet{
		s.buildStageSet(board, stageDir, "prompts",
			i18n.TCtx(r.Context(), "api.projects.labels.stage_prompts"),
			i18n.TCtx(r.Context(), "api.projects.hints.stage_prompts")),
		s.buildStageSet(board, stageDir, "hooks",
			i18n.TCtx(r.Context(), "api.projects.labels.pre_launch_hooks"),
			i18n.TCtx(r.Context(), "api.projects.hints.pre_launch_hooks")),
	}

	orphans := []string{}
	for _, kind := range []struct{ sub, ext string }{{"prompts", ".md"}, {"hooks", ".sh"}} {
		for _, base := range s.orphanStageFiles(filepath.Join(stageDir, kind.sub), kind.ext, "") {
			orphans = append(orphans, kind.sub+"/"+base)
		}
	}
	sort.Strings(orphans)

	writeJSON(w, http.StatusOK, ProjectStageFilesResponse{
		StageDir: stageDir,
		Sets:     sets,
		Orphans:  orphans,
	})
}

func (s *Server) buildStageSet(
	board []types.StageInfo, stageDir, kind, title, desc string,
) ProjectStageSet {
	stages := make([]ProjectStageEntry, 0, len(board))
	for i := range board {
		st := &board[i]
		path := stageFilePath(stageDir, kind, string(st.ID))
		exists := false
		if _, err := os.Stat(path); err == nil {
			exists = true
		}
		ext := ".md"
		if kind == "hooks" {
			ext = ".sh"
		}
		stages = append(stages, ProjectStageEntry{
			Stage:  string(st.ID),
			Label:  st.Label,
			Path:   kind + "/" + string(st.ID) + ext,
			Exists: exists,
		})
	}
	return ProjectStageSet{Kind: kind, Title: title, Description: desc, Stages: stages}
}

func (s *Server) handleReadProjectStageFile(w http.ResponseWriter, r *http.Request) {
	stageDir, err := s.projectStageDir(r, r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	full := stageFilePath(stageDir, r.PathValue("kind"), r.PathValue("stage"))
	if full == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.invalid_kind_or_stage")
		return
	}
	rel, relErr := safepath.Rel(stageDir, full)
	if relErr != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.invalid_path")
		return
	}
	data, err := safepath.ReadFile(stageDir, rel)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8") //i18n:ignore HTTP header value
			w.WriteHeader(http.StatusOK)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8") //i18n:ignore HTTP header value
	w.Write(data)
}

func (s *Server) handleWriteProjectStageFile(w http.ResponseWriter, r *http.Request) {
	stageDir, err := s.projectStageDir(r, r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	kind := r.PathValue("kind")
	stageID := r.PathValue("stage")
	full := stageFilePath(stageDir, kind, stageID)
	if full == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.invalid_kind_or_stage")
		return
	}
	// Reads and deletes tolerate orphaned stages; writes require the stage to exist.
	if !s.catalogHas(types.Stage(stageID)) {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.unknown_stage", "stage", stageID)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	mode := os.FileMode(0o644)
	if kind == "hooks" {
		mode = 0o750
	}
	if err := os.WriteFile(full, body, mode); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"size": len(body)})
}

func (s *Server) handleDeleteProjectStageFile(w http.ResponseWriter, r *http.Request) {
	stageDir, err := s.projectStageDir(r, r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	full := stageFilePath(stageDir, r.PathValue("kind"), r.PathValue("stage"))
	if full == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.invalid_kind_or_stage")
		return
	}
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			writeErrKey(w, r, http.StatusNotFound, "api.projects.errors.stage_file_missing")
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, "deleted")
}
