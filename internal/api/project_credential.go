package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type githubCredentialView struct {
	Source string `json:"source"`

	CredentialID string `json:"credential_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Identity     string `json:"identity,omitempty"`
	State        string `json:"state,omitempty"`

	InstallationID int64  `json:"installation_id,omitempty"`
	Account        string `json:"account,omitempty"`
}

func (s *Server) handleGetProjectGithubCredential(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	view := githubCredentialView{Source: "none"}

	if proj.GithubInstallationID != 0 && s.gh.app != nil && s.gh.app.Configured(r.Context()) {
		view.Source = "app"
		view.InstallationID = proj.GithubInstallationID
		if s.gh.store != nil {
			if inst, err := s.gh.store.GetInstallation(r.Context(), proj.GithubInstallationID); err == nil {
				view.Account = inst.AccountLogin
			}
		}
		writeJSON(w, http.StatusOK, view)
		return
	}

	if s.credStore != nil && proj.WorkspaceID != "" {
		c, err := s.credStore.GithubCredentialForProject(r.Context(), proj.WorkspaceID, proj.ID)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, githubCredentialView{
				Source:       "credential",
				CredentialID: c.ID,
				Name:         c.Name,
				Identity:     c.Identity,
				State:        string(c.State),
			})
			return
		case !errors.Is(err, credentials.ErrNotFound):
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, view)
}

type projectGitAuth struct {
	CredentialID   string
	RawToken       string
	InstallationID int64
}

func (a projectGitAuth) valid() error {
	n := 0
	for _, set := range []bool{a.CredentialID != "", a.RawToken != "", a.InstallationID != 0} {
		if set {
			n++
		}
	}
	if n > 1 {
		return apperr.Invalid("api.credentials.errors.git_auth_exclusive")
	}
	return nil
}

// The App installation is resolved separately (cloneTokenForInstallation) because it authorizes
// through its own workspace claim, before the request names a workspace.
// An empty token with ok=true means clone anonymously.
func (s *Server) cloneTokenFromCredential(w http.ResponseWriter, r *http.Request, workspaceID string, auth projectGitAuth) (string, bool) {
	switch {
	case auth.CredentialID != "":
		if !s.requireCredentials(w, r) {
			return "", false
		}
		c, err := s.credStore.Get(r.Context(), workspaceID, auth.CredentialID)
		if err != nil {
			writeCredentialError(w, r, err)
			return "", false
		}
		if c.Provider != "github" {
			writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.not_github",
				"name", c.Name, "provider", c.Provider)
			return "", false
		}
		if c.Disabled {
			writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.disabled", "name", c.Name)
			return "", false
		}
		payload, err := s.credStore.Payload(r.Context(), workspaceID, c.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return "", false
		}
		return strings.TrimSpace(payload["token"]), true

	case auth.RawToken != "":
		if !s.requireCredentials(w, r) {
			return "", false
		}
		if _, _, err := validateGithubToken(r.Context(), auth.RawToken); err != nil {
			writeError(w, r, http.StatusBadRequest,
				apperr.Wrap(err, "api.credentials.errors.token_validation_failed"))
			return "", false
		}
		return auth.RawToken, true
	}
	return "", true
}

// Failures are logged, not fatal: the project is already cloned, and failing the request would make
// the caller think it was never created.
func (s *Server) persistProjectCredential(r *http.Request, workspaceID, projectID, projectName string, auth projectGitAuth) {
	if s.credStore == nil {
		return
	}
	credID := auth.CredentialID
	if auth.RawToken != "" {
		c, err := s.mintProjectCredential(r, workspaceID, projectName, auth.RawToken)
		if err != nil {
			logx.Warn(err, "api.credentials.log.store_project_token_failed", "project", projectID)
			return
		}
		credID = c.ID
	}
	if credID == "" {
		return
	}
	logx.Warn(s.addProjectGrant(r.Context(), workspaceID, credID, projectID),
		"api.credentials.log.grant_project_failed", "credential", credID, "project", projectID)
}

func (s *Server) mintProjectCredential(r *http.Request, workspaceID, projectName, token string) (*credentials.Credential, error) {
	base := i18n.Text("api.credentials.labels.project_credential_name", "project", strings.TrimSpace(projectName))
	name := base
	var c *credentials.Credential
	var err error
	for attempt := 2; attempt <= 20; attempt++ {
		c, err = s.credStore.Create(r.Context(), workspaceID, credentials.Input{
			Name:     name,
			Provider: "github",
			Kind:     "github_pat",
			Payload:  map[string]string{"token": token},
		}, authorLabel(r))
		if !errors.Is(err, credentials.ErrExists) {
			break
		}
		name = fmt.Sprintf("%s (%d)", base, attempt)
	}
	if err != nil {
		return nil, err
	}
	s.auditCredential(r, audit.KindCredentialCreated, c,
		i18n.Text("api.credentials.audit.created_from_project", "name", c.Name))
	logx.Warn(s.introspectCredential(r.Context(), workspaceID, c.ID),
		"api.credentials.log.introspect_project_credential_failed", "project", projectName)
	return c, nil
}

// SetGrants replaces the whole list, so adding one grant needs this read-modify-write.
func (s *Server) addProjectGrant(ctx context.Context, workspaceID, credentialID, projectID string) error {
	grants, err := s.credStore.Grants(ctx, credentialID)
	if err != nil {
		return err
	}
	want := credentials.Grant{SubjectType: credentials.SubjectProject, SubjectID: projectID}
	for _, g := range grants {
		if g == want {
			return nil
		}
	}
	return s.credStore.SetGrants(ctx, workspaceID, credentialID, append(grants, want))
}

// Old project grants must be dropped, or the previous credential silently answers for the project
// whenever the new one is unusable.
func (s *Server) setProjectCredential(ctx context.Context, workspaceID, projectID, credentialID string) error {
	all, err := s.credStore.List(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.Provider != "github" || c.ID == credentialID {
			continue
		}
		kept := make([]credentials.Grant, 0, len(c.Grants))
		for _, g := range c.Grants {
			if g.SubjectType == credentials.SubjectProject && g.SubjectID == projectID {
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == len(c.Grants) {
			continue
		}
		if err := s.credStore.SetGrants(ctx, workspaceID, c.ID, kept); err != nil {
			return err
		}
	}
	if credentialID == "" {
		return nil
	}
	return s.addProjectGrant(ctx, workspaceID, credentialID, projectID)
}

func (s *Server) handleSetProjectGithubCredential(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.requireCredentials(w, r) {
		return
	}
	var req struct {
		CredentialID string `json:"credential_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if proj.WorkspaceID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.project_without_workspace")
		return
	}
	id := strings.TrimSpace(req.CredentialID)
	if id != "" {
		c, err := s.credStore.Get(r.Context(), proj.WorkspaceID, id)
		if err != nil {
			writeCredentialError(w, r, err)
			return
		}
		if c.Provider != "github" {
			writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.not_github",
				"name", c.Name, "provider", c.Provider)
			return
		}
	}
	if err := s.setProjectCredential(r.Context(), proj.WorkspaceID, proj.ID, id); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.handleGetProjectGithubCredential(w, r)
}
