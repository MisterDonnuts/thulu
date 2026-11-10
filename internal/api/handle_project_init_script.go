package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// The author comes only from the credential, never the request body: a run key signs as its agent
// run.
func initScriptAuthor(ctx context.Context) (project.InitAuthor, error) {
	a, err := auth.AuthorFromContext(ctx)
	if err != nil {
		return project.InitAuthor{}, err
	}
	actor := auth.ActorFromContext(ctx)
	if !a.Agent {
		return project.InitAuthor{Kind: project.InitAuthorUser, ID: actor.ID, Label: a.Name}, nil
	}
	scope := auth.APIKeyRunScope(ctx)
	//i18n:ignore an identity label stored with the revision, not a message
	label := fmt.Sprintf("agent %s · task %s · stage %s", a.Name, scope.TaskID, scope.Stage)
	return project.InitAuthor{Kind: project.InitAuthorAgent, ID: actor.ID, Label: label}, nil
}

func (s *Server) auditInitScript(ctx context.Context, proj *project.Project, sc *project.InitScript) {
	if s.auditStore == nil {
		return
	}
	summary := i18n.Text("api.projects.audit.init_script_saved", "project", proj.Name, "revision", sc.Revision)
	if sc.RestoredFrom > 0 {
		summary = i18n.Text("api.projects.audit.init_script_restored",
			"project", proj.Name, "revision", sc.Revision, "from", sc.RestoredFrom)
	}
	s.auditStore.LogContext(ctx, proj.WorkspaceID, audit.KindProjectInitScriptChanged, proj.ID, "project",
		summary, map[string]string{
			"revision":      strconv.Itoa(sc.Revision),
			"restored_from": strconv.Itoa(sc.RestoredFrom),
			"author_kind":   sc.AuthorKind,
			"author":        sc.AuthorLabel,
			"note":          sc.Note,
		})
}

func initScriptStatus(err error) int {
	if errors.Is(err, project.ErrNoInitScript) {
		return http.StatusNotFound
	}
	return 0
}

func (s *Server) handleGetProjectInitScript(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	sc, err := s.projectStore.InitScript(r.Context(), proj.ID)
	if errors.Is(err, project.ErrNoInitScript) {
		writeJSON(w, http.StatusOK, project.InitScript{
			ProjectID: proj.ID, Manifest: project.DefaultInitManifest(),
		})
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleProjectRootFiles(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	files, commit, err := s.projectStore.RootFiles(r.Context(), proj.ID)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"branch": proj.DefaultBranch, "commit": commit, "files": files,
	})
}

func (s *Server) handlePutProjectInitScript(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body project.InitScriptInput
	if !decodeBody(w, r, &body) {
		return
	}
	author, err := initScriptAuthor(r.Context())
	if err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	sc, err := s.projectStore.SaveInitScript(r.Context(), proj.ID, body, author)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	s.auditInitScript(r.Context(), proj, sc)
	s.prewarmAfterSave(proj.ID)
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleListProjectInitScriptRevisions(w http.ResponseWriter, r *http.Request) {
	s.listProjectResource(w, r, func(id string) (any, error) {
		return s.projectStore.ListInitScriptRevisions(r.Context(), id, atoiDefault(r.URL.Query().Get("limit"), 0))
	})
}

func initRevisionParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	rev, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || rev < 1 {
		writeError(w, r, http.StatusBadRequest, apperr.Invalid("api.projects.errors.init_revision_invalid", "rev", r.PathValue("rev")))
		return 0, false
	}
	return rev, true
}

func (s *Server) handleGetProjectInitScriptRevision(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	rev, ok := initRevisionParam(w, r)
	if !ok {
		return
	}
	sc, err := s.projectStore.InitScriptRevision(r.Context(), proj.ID, rev)
	if err != nil {
		writeError(w, r, initScriptStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleRestoreProjectInitScript(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	rev, ok := initRevisionParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note,omitempty"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	author, err := initScriptAuthor(r.Context())
	if err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	sc, err := s.projectStore.RestoreInitScript(r.Context(), proj.ID, rev, author, body.Note)
	if err != nil {
		writeError(w, r, initScriptStatus(err), err)
		return
	}
	s.auditInitScript(r.Context(), proj, sc)
	s.prewarmAfterSave(proj.ID)
	writeJSON(w, http.StatusOK, sc)
}
