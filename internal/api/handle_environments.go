package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/patch"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// Use the project's workspace, not the caller's: the unscoped operator key floors to "default" and
// would read
// has_credential=false. Safe because projectForCaller already authorized the caller.
func (s *Server) envWithCredential(r *http.Request, workspaceID string, e project.Environment) project.Environment {
	e.IngestEndpoint = s.ingestEndpoint(r.Context())
	if s.credStore == nil {
		return e
	}
	granted, err := s.credStore.GrantedSubjects(r.Context(),
		workspaceID, credentials.SubjectEnvironment)
	if err != nil {
		logx.Warn(err, "api.environments.log.list_granted_failed")
		return e
	}
	e.HasCredential = granted[e.ID]
	return e
}

// OTel SDKs append "/v1/traces" to this base. Return "" when unset, never a guess: a wrong address
// fails
// silently in the deployed app.
func (s *Server) ingestEndpoint(ctx context.Context) string {
	if s.settingsStore == nil {
		return ""
	}
	base, err := s.settingsStore.IngestPublicURL(ctx)
	if err != nil {
		logx.Warn(err, "api.environments.log.read_public_url_failed")
		return ""
	}
	if base == "" {
		return ""
	}
	return base + "/ingest"
}

func (s *Server) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	envs, err := s.projectStore.ListEnvironments(r.Context(), proj.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	granted := map[string]bool{}
	if s.credStore != nil {
		g, gerr := s.credStore.GrantedSubjects(r.Context(),
			proj.WorkspaceID, credentials.SubjectEnvironment)
		if gerr != nil {
			logx.Warn(gerr, "api.environments.log.list_granted_project_failed", "project", proj.ID)
		} else {
			granted = g
		}
	}
	endpoint := s.ingestEndpoint(r.Context())
	out := make([]project.Environment, 0, len(envs))
	for i := range envs {
		envs[i].HasCredential = granted[envs[i].ID]
		envs[i].IngestEndpoint = endpoint
		out = append(out, envs[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var in project.EnvironmentInput
	if !decodeBody(w, r, &in) {
		return
	}
	env, err := s.projectStore.CreateEnvironment(r.Context(), proj.ID, in)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.envWithCredential(r, proj.WorkspaceID, *env))
}

type environmentPatch struct {
	Name         *string            `json:"name"`
	DisplayName  *string            `json:"display_name"`
	System       *string            `json:"system"`
	Provider     *string            `json:"provider"`
	Region       *string            `json:"region"`
	Account      *string            `json:"account"`
	Selectors    *map[string]string `json:"selectors"`
	IsProduction *bool              `json:"is_production"`
	URL          *string            `json:"url"`
}

func (p environmentPatch) apply(cur project.Environment) project.EnvironmentInput {
	in := project.EnvironmentInput{
		Name:         cur.Name,
		DisplayName:  cur.DisplayName,
		System:       cur.System,
		Provider:     cur.Provider,
		Region:       cur.Region,
		Account:      cur.Account,
		Selectors:    cur.Selectors,
		IsProduction: cur.IsProduction,
		URL:          cur.URL,
	}
	patch.Set(&in.Name, p.Name)
	patch.Set(&in.DisplayName, p.DisplayName)
	patch.Set(&in.System, p.System)
	patch.Set(&in.Provider, p.Provider)
	patch.Set(&in.Region, p.Region)
	patch.Set(&in.Account, p.Account)
	patch.Set(&in.Selectors, p.Selectors)
	patch.Set(&in.IsProduction, p.IsProduction)
	patch.Set(&in.URL, p.URL)
	return in
}

func (s *Server) handleUpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body environmentPatch
	if !decodeBody(w, r, &body) {
		return
	}
	envID := r.PathValue("envId")
	cur, err := s.projectStore.GetEnvironment(r.Context(), proj.ID, envID)
	if err != nil {
		s.writeEnvError(w, r, err)
		return
	}
	env, err := s.projectStore.UpdateEnvironment(r.Context(), proj.ID, envID, body.apply(*cur))
	if err != nil {
		s.writeEnvError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.envWithCredential(r, proj.WorkspaceID, *env))
}

func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	envID := r.PathValue("envId")
	if _, err := s.projectStore.GetEnvironment(r.Context(), proj.ID, envID); err != nil {
		s.writeEnvError(w, r, err)
		return
	}
	// Must run before DeleteEnvironment: detaching copies the environment's name, URL and
	// production flag.
	if s.evidenceStore != nil {
		if err := s.evidenceStore.DetachProjectEnvironment(r.Context(), envID); err != nil {
			writeEvidenceErr(w, r, err)
			return
		}
	}
	if err := s.projectStore.DeleteEnvironment(r.Context(), proj.ID, envID); err != nil {
		s.writeEnvError(w, r, err)
		return
	}
	// Revoke the grant, not the credential: a re-created environment reusing this id must not
	// inherit its cloud access.
	if grantWID, ok := s.requestWorkspace(r); ok && s.credStore != nil {
		logx.Warn(s.credStore.RevokeGrantsTo(r.Context(), grantWID,
			credentials.SubjectEnvironment, envID),
			"api.environments.log.revoke_grants_failed", "environment", envID)
	}
	if s.envMetrics != nil {
		logx.Warn(s.envMetrics.DeleteForEnvironment(r.Context(), envID),
			"api.environments.log.delete_mappings_failed", "environment", envID)
	}
	writeStatus(w, "deleted")
}

func (s *Server) writeEnvError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, project.ErrEnvNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.not_found")
		return
	}
	writeError(w, r, http.StatusBadRequest, err)
}

func (s *Server) requireEnv(w http.ResponseWriter, r *http.Request) (*project.Environment, bool) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return nil, false
	}
	env, err := s.projectStore.GetEnvironment(r.Context(), proj.ID, r.PathValue("envId"))
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.not_found")
		return nil, false
	}
	return env, true
}

// Carries no secret: /internal/credentials/resolve is the single audited plaintext path.
// 404 rather than 403 so other tenants' environments cannot be enumerated.
func (s *Server) handleInternalEnvironment(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalCaller(w, r, false) {
		return
	}
	env, err := s.projectStore.FindEnvironment(r.Context(), r.PathValue("envId"))
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.not_found")
		return
	}
	if !s.requireProjectScopeQuiet(r, env.ProjectID) || !s.internalProjectVisible(r, env.ProjectID) {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"environment_id": env.ID,
		"project_id":     env.ProjectID,
		"name":           env.Name,
		"provider":       env.Provider,
		"region":         env.Region,
		"account":        env.Account,
		"selectors":      env.Selectors,
		"is_production":  env.IsProduction,
	})
}
