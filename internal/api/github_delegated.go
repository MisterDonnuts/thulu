package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// deployments:write is deliberately absent: approving a deployment waits for the person
// (github_held.go).
var delegatedTokenExtras = map[string]bool{
	"actions:write": true, //i18n:ignore GitHub permission token
	"checks:read":   true, //i18n:ignore GitHub permission token
}

// Kept apart from requireProjectScopeQuiet so the delegated-chat widening covers GitHub tokens
// only, not cloud-credential routes.
func (s *Server) runReachesGithubProject(r *http.Request, projectID string) bool {
	if scope := auth.APIKeyRunScope(r.Context()); scope.Delegated() && scope.TaskID == "" {
		return true
	}
	return s.requireProjectScopeQuiet(r, projectID)
}

func (s *Server) chatStageTools(ctx context.Context, scope auth.RunScope) []string {
	if s.taskDispatcher == nil || scope.Agent == "" {
		return nil
	}
	at := s.taskDispatcher.FindAgentTypeForRelaunch(auth.APIKeyWorkspace(ctx), scope.Agent)
	if at == nil {
		return nil
	}
	return at.MCPByStage[scope.Stage]
}

func (s *Server) delegatedTokenPermissions(r *http.Request, scope auth.RunScope) (githubapp.Permissions, error) {
	perms := githubapp.PermsDelegatedChat
	raw := r.URL.Query().Get("permissions")
	if raw == "" {
		return perms, nil
	}
	entries := s.chatStageTools(r.Context(), scope)
	for _, name := range strings.Split(raw, ",") {
		extra, known := tokenPermissionExtras[name]
		if !known {
			return nil, apperr.Invalid("api.github.errors.unknown_permission", "name", name)
		}
		if !delegatedTokenExtras[name] {
			return nil, apperr.Forbidden("api.github.errors.delegated_permission_held", "name", name)
		}
		if !githubPermissionGranted(entries, name) {
			return nil, apperr.Forbidden("api.github.errors.permission_not_granted",
				"name", name, "tools", strings.Join(githubPermissionTools[name], ", "), "server", githubMCPServer)
		}
		perms = perms.With(extra)
	}
	return perms, nil
}

func repoRef(raw string) (string, bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	var owner, name string
	if !strings.Contains(s, ":") && !strings.Contains(s, "@") {
		parts := strings.Split(s, "/")
		if len(parts) == 3 && strings.Contains(parts[0], ".") {
			parts = parts[1:]
		}
		if len(parts) != 2 {
			return "", false
		}
		owner, name = parts[0], parts[1]
	} else {
		var ok bool
		if owner, name, ok = ghrest.ParseRemote(s); !ok {
			return "", false
		}
	}
	name = strings.TrimSuffix(name, ".git")
	if owner == "" || name == "" {
		return "", false
	}
	return strings.ToLower(owner + "/" + name), true
}

func (s *Server) projectsForRepo(r *http.Request, ref string) ([]project.Project, error) {
	var all []project.Project
	var err error
	if wid, unrestricted := s.callerWorkspace(r); unrestricted {
		all, err = s.projectStore.List(r.Context())
	} else {
		all, err = s.projectStore.ListByWorkspace(r.Context(), wid)
	}
	if err != nil {
		return nil, err
	}
	var linked, others []project.Project
	for i := range all {
		p := &all[i]
		if got, ok := repoRef(p.RepoURL); !ok || got != ref || !s.internalVisible(r, p.WorkspaceID) {
			continue
		}
		if p.GithubInstallationID != 0 {
			linked = append(linked, *p)
		} else {
			others = append(others, *p)
		}
	}
	return append(linked, others...), nil
}

func (s *Server) handleInternalRepoGithubToken(w http.ResponseWriter, r *http.Request) {
	if s.gh.tokens == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.no_credential_source")
		return
	}
	if !s.requireInternalCaller(w, r, true) {
		return
	}
	raw := r.URL.Query().Get("repo")
	ref, ok := repoRef(raw)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.repo_invalid", "repo", raw)
		return
	}
	if s.projectStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.project_store_unavailable")
		return
	}
	candidates, err := s.projectsForRepo(r, ref)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if len(candidates) == 0 {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.repo_not_in_workspace", "repo", ref)
		return
	}
	var proj *project.Project
	for i := range candidates {
		if s.runReachesGithubProject(r, candidates[i].ID) {
			proj = &candidates[i]
			break
		}
	}
	if proj == nil {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.key_task_not_in_project")
		return
	}
	perms, err := s.tokenPermissionsFor(r, s.runTask(r))
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	s.writeProjectGithubToken(w, r, proj.ID, perms)
}

// A delegated chat gets only an App token: a stored PAT carries whatever its owner gave it, merge
// included, and cannot be narrowed.
func (s *Server) writeProjectGithubToken(w http.ResponseWriter, r *http.Request, projectID string, perms githubapp.Permissions) {
	rec, err := s.githubTokenForProjectWith(r.Context(), projectID, perms)
	if errors.Is(err, githubapp.ErrNoCredential) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.no_token")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if scope := auth.APIKeyRunScope(r.Context()); scope.Delegated() {
		if rec.Source != githubapp.SourceApp {
			writeErrKey(w, r, http.StatusForbidden, "api.github.errors.delegated_needs_app_token")
			return
		}
		s.emit(r, audit.KindChatGithubGrant, "", projectID, "project",
			i18n.Text("api.github.audit.delegated_token", "agent", scope.Agent, "person", scope.DelegatedBy,
				"project", s.projectLabel(r.Context(), projectID), "permissions", perms.String()), nil)
	}
	writeJSON(w, http.StatusOK, internalGithubTokenResp{
		ProjectID: rec.ProjectID,
		Token:     rec.Token,
		Login:     rec.Login,
	})
}

func (s *Server) projectName(ctx context.Context, projectID string) string {
	if s.projectStore == nil {
		return projectID
	}
	if p, err := s.projectStore.Get(ctx, projectID); err == nil && p != nil && p.Name != "" {
		return p.Name
	}
	return projectID
}

func (s *Server) projectLabel(ctx context.Context, projectID string) string {
	if s.projectStore == nil {
		return projectID
	}
	p, err := s.projectStore.Get(ctx, projectID)
	if err != nil || p == nil {
		return projectID
	}
	ref, ok := repoRef(p.RepoURL)
	if !ok {
		return p.Name
	}
	return p.Name + " (" + ref + ")"
}
