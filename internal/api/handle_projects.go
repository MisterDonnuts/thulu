package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.projectStore.List(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if wid, unrestricted := s.callerWorkspace(r); !unrestricted {
		visible := make([]project.Project, 0, len(projects))
		for i := range projects {
			if workspaceVisible(projects[i].WorkspaceID, wid, false) {
				visible = append(visible, projects[i])
			}
		}
		projects = visible
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name                 string            `json:"name"`
		RepoURL              string            `json:"repo_url"`
		Description          string            `json:"description,omitempty"`
		DefaultBranch        string            `json:"default_branch,omitempty"`
		StageTemplate        map[string]string `json:"stage_template,omitempty"`
		DefaultBoardID       string            `json:"default_board_id,omitempty"`
		GithubCredentialID   string            `json:"github_credential_id,omitempty"`
		GitHubToken          string            `json:"github_token,omitempty"`
		GithubInstallationID int64             `json:"github_installation_id,omitempty"`
		Background           bool              `json:"background,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Name == "" || body.RepoURL == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.name_and_repo_required")
		return
	}
	// repo_url and default_branch become git arguments: validate before any token is minted or
	// anything is logged.
	// The store re-checks (with DNS) right before cloning.
	body.RepoURL = project.NormalizeRepoURL(body.RepoURL)
	if err := validProjectSource(body.RepoURL, body.DefaultBranch); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	auth := projectGitAuth{
		CredentialID:   strings.TrimSpace(body.GithubCredentialID),
		RawToken:       strings.TrimSpace(body.GitHubToken),
		InstallationID: body.GithubInstallationID,
	}
	if err := auth.valid(); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	var cloneToken string
	if auth.InstallationID != 0 {
		tok, ok := s.cloneTokenForInstallation(w, r, auth.InstallationID, body.RepoURL)
		if !ok {
			return
		}
		cloneToken = tok
	}

	// Refuse rather than store an unowned project: it is hidden from every tenant, and tasks
	// created against it inherit its empty workspace.
	wid, ok := s.newResourceWorkspace(w, r, "project")
	if !ok {
		return
	}

	if auth.InstallationID == 0 {
		tok, ok := s.cloneTokenFromCredential(w, r, wid, auth)
		if !ok {
			return
		}
		cloneToken = tok
	}

	if body.DefaultBoardID != "" && !s.boardVisibleToCaller(r, body.DefaultBoardID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.boards.errors.unknown_board", "board", body.DefaultBoardID)
		return
	}

	log.Printf("[api] %s", i18n.Log("api.projects.log.creating", "name", body.Name, "repo", body.RepoURL,
		"credential", auth.CredentialID != "", "token", auth.RawToken != "", "installation", auth.InstallationID))
	opts := project.CreateOpts{
		Description:          body.Description,
		DefaultBranch:        body.DefaultBranch,
		StageTemplate:        body.StageTemplate,
		DefaultBoardID:       body.DefaultBoardID,
		GitHubToken:          cloneToken,
		WorkspaceID:          wid,
		GithubInstallationID: body.GithubInstallationID,
	}
	proj, status, err := s.createProject(r.Context(), body.Name, body.RepoURL, opts, body.Background)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.projects.log.create_failed", "err", err))
		writeError(w, r, projectInputStatus(err), err)
		return
	}
	s.persistProjectCredential(r, wid, proj.ID, proj.Name, auth)
	writeJSON(w, status, proj)
}

func validProjectSource(repoURL, branch string) error {
	if err := project.ValidateRepoURL(repoURL); err != nil {
		return err
	}
	if branch == "" {
		return nil
	}
	return project.ValidateBranchName(branch)
}

func (s *Server) createProject(
	ctx context.Context, name, repoURL string, opts project.CreateOpts, background bool,
) (*project.Project, int, error) {
	if !background {
		proj, err := s.projectStore.Create(ctx, name, repoURL, opts)
		return proj, http.StatusCreated, err
	}
	proj, err := s.projectStore.CreateInBackground(ctx, name, repoURL, opts, func(p *project.Project) {
		if p.CloneStatus == project.CloneFailed {
			log.Printf("[api] %s", i18n.Log("api.projects.log.create_failed", "err", p.CloneError))
		}
	})
	return proj, http.StatusAccepted, err
}

func projectInputStatus(err error) int {
	if errors.Is(err, project.ErrInvalidRepoURL) || errors.Is(err, project.ErrInvalidBranch) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (s *Server) cloneTokenForInstallation(w http.ResponseWriter, r *http.Request, installationID int64, repoURL string) (string, bool) {
	if !s.githubAppReady(w, r) {
		return "", false
	}
	inst, ok := s.installationForCaller(w, r, installationID)
	if !ok {
		return "", false
	}
	_, repo, parsed := github.ParseRemote(repoURL)
	if !parsed {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.repo_url_not_a_repository")
		return "", false
	}
	tok, err := s.gh.app.MintToken(r.Context(), inst.InstallationID, repo, githubapp.PermsClone)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, apperr.Wrap(err, "api.projects.errors.github_app_token_failed"))
		return "", false
	}
	return tok, true
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	current, ok := s.projectForCaller(w, r, id)
	if !ok {
		return
	}
	var p project.Patch
	if !decodeBody(w, r, &p) {
		return
	}
	if p.DefaultBoardID != nil && *p.DefaultBoardID != "" &&
		!s.boardVisibleToCaller(r, *p.DefaultBoardID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.boards.errors.unknown_board", "board", *p.DefaultBoardID)
		return
	}
	// Only a new installation is checked: a GET-then-PATCH round-trip must not fail when the linked
	// installation was since removed.
	if p.GithubInstallationID != nil && *p.GithubInstallationID != 0 &&
		*p.GithubInstallationID != current.GithubInstallationID {
		if _, ok := s.cloneTokenForInstallation(w, r, *p.GithubInstallationID, current.RepoURL); !ok {
			return
		}
	}
	runnerName, allowed := s.patchRunnerAllowed(w, r, current, p.RunnerID)
	if !allowed {
		return
	}
	proj, err := s.projectStore.Update(r.Context(), id, p)
	if err != nil {
		status := projectInputStatus(err)
		if status == http.StatusInternalServerError {
			status = apperr.Status(err)
		}
		writeError(w, r, status, err)
		return
	}
	if proj.RunnerID != current.RunnerID {
		s.auditRunnerChange(r, proj, current.RunnerID, runnerName)
	}
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.projectForCaller(w, r, id); !ok {
		return
	}
	// Must run before the environments are purged: DetachProject copies their names and URLs.
	if s.evidenceStore != nil {
		paths, err := s.evidenceStore.DetachProject(r.Context(), id)
		logx.Warn(err, "api.evidence.log.detach_failed", "project", id)
		removeEvidenceFiles(paths)
	}

	// Purge before the project row goes: a re-created project must not inherit them, and an
	// orphaned cloud credential is a secret nobody can see to revoke.
	s.purgeProjectEnvironments(r, id)

	if grantWID, ok := s.requestWorkspace(r); ok && s.credStore != nil {
		logx.Warn(s.credStore.RevokeGrantsTo(r.Context(), grantWID,
			credentials.SubjectProject, id), "api.projects.log.revoke_project_grants", "project", id)
	}

	// A link outliving its project still gets a task past the gate that requires a working copy to
	// mount.
	if s.taskStore != nil {
		n, err := s.taskStore.UnlinkProject(r.Context(), id)
		logx.Warn(err, "api.projects.log.unlink_tasks", "project", id)
		if n > 0 {
			log.Printf("[api] %s", i18n.Log("api.projects.log.unlinked_tasks", "project", id, "count", n))
		}
	}

	if err := s.projectStore.Delete(r.Context(), id); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, "deleted")
}

func (s *Server) purgeProjectEnvironments(r *http.Request, projectID string) {
	envs, err := s.projectStore.ListEnvironments(r.Context(), projectID)
	if err != nil {
		logx.Warn(err, "api.projects.log.list_environments", "project", projectID)
		return
	}
	for i := range envs {
		if grantWID, ok := s.requestWorkspace(r); ok && s.credStore != nil {
			logx.Warn(s.credStore.RevokeGrantsTo(r.Context(), grantWID,
				credentials.SubjectEnvironment, envs[i].ID),
				"api.projects.log.revoke_environment_grants", "environment", envs[i].ID)
		}
		if s.envMetrics != nil {
			logx.Warn(s.envMetrics.DeleteForEnvironment(r.Context(), envs[i].ID),
				"api.projects.log.delete_metric_mappings", "environment", envs[i].ID)
		}
	}
	logx.Warn(s.projectStore.DeleteEnvironmentsForProject(r.Context(), projectID),
		"api.projects.log.delete_environments", "project", projectID)
}

func (s *Server) listProjectResource(w http.ResponseWriter, r *http.Request, fetch func(id string) (any, error)) {
	id := r.PathValue("id")
	if _, ok := s.projectForCaller(w, r, id); !ok {
		return
	}
	out, err := fetch(id)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListWorktrees(w http.ResponseWriter, r *http.Request) {
	s.listProjectResource(w, r, func(id string) (any, error) { return s.projectStore.ListWorktrees(r.Context(), id) })
}
