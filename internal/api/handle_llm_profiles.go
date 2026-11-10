package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type createProfileReq struct {
	Name           string                   `json:"name"`
	Engine         string                   `json:"engine"`
	Model          string                   `json:"model"`
	Effort         string                   `json:"effort"`
	BaseURL        string                   `json:"base_url"`
	OrgID          string                   `json:"org_id"`
	Provider       string                   `json:"provider"`
	AuthHeader     string                   `json:"auth_header"`
	Region         string                   `json:"region"`
	ProjectID      string                   `json:"project_id"`
	SmallFastModel string                   `json:"small_fast_model"`
	Kind           string                   `json:"kind"`
	Federation     *engine.FederationConfig `json:"federation,omitempty"`
	profileSecretFields
	IsDefault bool `json:"is_default"`
}

type updateProfileReq struct {
	Name           string                   `json:"name"`
	Engine         string                   `json:"engine"`
	Model          *string                  `json:"model,omitempty"`
	Effort         *string                  `json:"effort,omitempty"`
	BaseURL        *string                  `json:"base_url,omitempty"`
	OrgID          *string                  `json:"org_id,omitempty"`
	Provider       string                   `json:"provider"`
	AuthHeader     *string                  `json:"auth_header,omitempty"`
	Region         *string                  `json:"region,omitempty"`
	ProjectID      *string                  `json:"project_id,omitempty"`
	SmallFastModel *string                  `json:"small_fast_model,omitempty"`
	Kind           string                   `json:"kind"`
	Federation     *engine.FederationConfig `json:"federation,omitempty"`
	profileSecretFields
	ClearKey  bool  `json:"clear_key"`
	IsDefault *bool `json:"is_default,omitempty"`
}

type profileView struct {
	*secrets.LLMProfile
	BoundAgents []string `json:"bound_agents"`
}

type setBindingReq struct {
	ProfileID string `json:"profile_id"`
}

func (s *Server) handleListLLMProfiles(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	profiles, err := s.secretsStore.ListLLMProfiles(r.Context(), wsID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	bound, err := s.secretsStore.BoundAgentsByProfile(r.Context(), wsID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	out := make([]profileView, 0, len(profiles))
	for _, p := range profiles {
		agents := bound[p.ID]
		agents = nonNil(agents)
		out = append(out, profileView{LLMProfile: p, BoundAgents: agents})
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *Server) handleCreateLLMProfile(w http.ResponseWriter, r *http.Request) {
	var req createProfileReq
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.name_required")
		return
	}
	if strings.TrimSpace(req.Engine) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.engine_required")
		return
	}
	desc, ok := engineByName(r.Context(), req.Engine)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.unknown_engine", "engine", req.Engine)
		return
	}
	shape := profileShape{
		provider:       strings.TrimSpace(req.Provider),
		baseURL:        strings.TrimSpace(req.BaseURL),
		orgID:          strings.TrimSpace(req.OrgID),
		authHeader:     strings.TrimSpace(req.AuthHeader),
		region:         strings.TrimSpace(req.Region),
		projectID:      strings.TrimSpace(req.ProjectID),
		smallFastModel: strings.TrimSpace(req.SmallFastModel),
	}
	if shape.provider == "" {
		shape.provider = desc.Provider
	}
	if err := validateProfileShape(desc, shape); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	fed, federates, cred, status, err := s.newProfileCredential(r, desc, shape, req)
	if err != nil {
		writeError(w, r, status, err)
		return
	}
	cloud := engine.IsCloudProvider(shape.provider)
	if !cred.set && !cloud && !federates {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.key_required")
		return
	}
	if !cred.set && cloud {
		if aerr := ambientCloudProfileAllowed(shape.provider); aerr != nil {
			writeError(w, r, http.StatusBadRequest, aerr)
			return
		}
	}

	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	prof, err := s.secretsStore.CreateLLMProfile(r.Context(), wsID, secrets.LLMProfileInput{
		Name:           req.Name,
		Engine:         req.Engine,
		Model:          req.Model,
		Effort:         req.Effort,
		BaseURL:        shape.baseURL,
		OrgID:          shape.orgID,
		Provider:       shape.provider,
		AuthHeader:     shape.authHeader,
		Region:         shape.region,
		ProjectID:      shape.projectID,
		SmallFastModel: shape.smallFastModel,
		Federation:     fed,
		RawKey:         cred.raw,
		KeyHint:        cred.hint,
		KeyOptional:    cloud || federates,
		IsDefault:      req.IsDefault,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeErrKey(w, r, http.StatusConflict, "api.llm.errors.profile_name_taken")
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.auditLLMProfile(r, audit.KindLLMProfileCreated, prof, i18n.Text("api.llm.audit.profile_created", "name", prof.Name))
	writeJSON(w, http.StatusCreated, s.recordProfileKeyCheck(r.Context(), desc, prof, cred.raw))
}

func (s *Server) ownerOnlyUpdate(
	r *http.Request, desc engineDescriptor, existing *secrets.LLMProfile, kind string, in secrets.LLMProfileInput,
) (int, error) {
	if denied := s.ownerOnlyCredential(r, desc, kind, in.RawKey); denied != nil {
		return http.StatusForbidden, denied
	}
	if in.RawKey != "" || in.ClearKey {
		return http.StatusOK, nil
	}
	return s.ownerOnlyStored(r, existing)
}

func (s *Server) newProfileCredential(
	r *http.Request, desc engineDescriptor, shape profileShape, req createProfileReq,
) (fed engine.FederationConfig, federates bool, cred profileCredential, status int, err error) {
	kind := strings.TrimSpace(req.Kind)
	fed, federates, err = s.requestFederation(desc, shape, req.trimmed(), kind, req.Federation)
	if err != nil || federates {
		return fed, federates, profileCredential{}, http.StatusBadRequest, err
	}
	if cred, err = buildProfileCredential(desc, shape, req.trimmed(), kind); err != nil {
		return fed, false, cred, http.StatusBadRequest, err
	}
	if denied := s.ownerOnlyCredential(r, desc, kind, cred.raw); denied != nil {
		return fed, false, cred, http.StatusForbidden, denied
	}
	return fed, false, cred, http.StatusOK, nil
}

func (s *Server) handleGetLLMProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	prof, err := s.secretsStore.GetLLMProfile(r.Context(), callerWID, id)
	if errors.Is(err, secrets.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

func (s *Server) handleUpdateLLMProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	existing, lookupErr := s.secretsStore.GetLLMProfile(r.Context(), wsID, id)
	if errors.Is(lookupErr, secrets.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
		return
	}
	if lookupErr != nil {
		writeError(w, r, http.StatusInternalServerError, lookupErr)
		return
	}

	var req updateProfileReq
	if !decodeBody(w, r, &req) {
		return
	}

	engineName := strings.TrimSpace(req.Engine)
	if engineName == "" {
		engineName = existing.Engine
	}
	if existing.IsDefault && engineName != existing.Engine {
		writeErrKey(w, r, http.StatusConflict, "api.llm.errors.default_engine_locked", "engine", existing.Engine)
		return
	}
	desc, ok := engineByName(r.Context(), engineName)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.unknown_engine", "engine", engineName)
		return
	}
	in, err := s.updatedProfileInput(r.Context(), desc, existing, req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if status, denied := s.ownerOnlyUpdate(r, desc, existing, strings.TrimSpace(req.Kind), in); denied != nil {
		writeError(w, r, status, denied)
		return
	}

	prof, err := s.secretsStore.UpdateLLMProfile(r.Context(), wsID, id, in)
	if err != nil {
		if isUniqueViolation(err) {
			writeErrKey(w, r, http.StatusConflict, "api.llm.errors.profile_name_taken")
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.IsDefault != nil && *req.IsDefault != prof.IsDefault {
		if prof, err = s.secretsStore.SetLLMProfileDefault(r.Context(), wsID, id, *req.IsDefault); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	s.auditLLMProfile(r, audit.KindLLMProfileUpdated, prof, i18n.Text("api.llm.audit.profile_updated", "name", prof.Name))
	writeJSON(w, http.StatusOK, s.recheckUpdatedProfile(r.Context(), desc, existing, prof, in.RawKey))
}

func (s *Server) handleDeleteLLMProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	prof, err := s.secretsStore.GetLLMProfile(r.Context(), wsID, id)
	if errors.Is(err, secrets.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := s.secretsStore.DeleteLLMProfile(r.Context(), wsID, id); err != nil {
		if errors.Is(err, secrets.ErrProfileInUse) {
			writeErrKey(w, r, http.StatusConflict, "api.llm.errors.profile_in_use")
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditLLMProfile(r, audit.KindLLMProfileDeleted, prof, i18n.Text("api.llm.audit.profile_deleted", "name", prof.Name))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetAgentLLMBinding(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	b, err := s.secretsStore.GetAgentBinding(r.Context(), callerWID, agentName)
	if errors.Is(err, secrets.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"bound": false})
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bound": true, "binding": b})
}

func (s *Server) handlePutAgentLLMBinding(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}

	var req setBindingReq
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProfileID) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.profile_id_required")
		return
	}
	prof, err := s.secretsStore.GetLLMProfile(r.Context(), wsID, req.ProfileID)
	if errors.Is(err, secrets.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	agentType := s.lookupAgentType(wsID, agentName)
	if agentType == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.agent_not_found")
		return
	}
	forced := false
	if !agentSupportsEngine(agentType, prof.Engine) {
		if !isAdminForcing(r) {
			supported := strings.Join(agentType.SupportedEngines, ", ")
			if supported == "" {
				supported = i18n.TCtx(r.Context(), "api.llm.labels.no_engines_declared")
			}
			writeErrKey(w, r, http.StatusUnprocessableEntity, "api.llm.errors.engine_not_supported",
				"engine", prof.Engine, "supported", supported)
			return
		}
		forced = true
	}

	updatedBy := actorIDFromRequest(r)
	if err := s.secretsStore.SetAgentBinding(r.Context(), wsID, agentName, req.ProfileID, updatedBy); err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	kind := audit.KindLLMBindingChanged
	summary := i18n.Text("api.llm.audit.binding_changed", "agent", agentName, "profile", prof.Name, "engine", prof.Engine)
	if forced {
		kind = audit.KindLLMBindingForced
		summary = i18n.Text("api.llm.audit.binding_forced", "agent", agentName, "profile", prof.Name, "engine", prof.Engine)
	}
	s.auditAgentBinding(r, kind, agentName, prof, summary)
	b, bErr := s.secretsStore.GetAgentBinding(r.Context(), wsID, agentName)
	if bErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"bound": true, "forced": forced})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bound": true, "binding": b, "forced": forced})
}

func (s *Server) handleDeleteAgentLLMBinding(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.secretsStore.DeleteAgentBinding(r.Context(), wsID, agentName); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditAgentBinding(r, audit.KindLLMBindingRemoved, agentName, nil,
		i18n.Text("api.llm.audit.binding_removed", "agent", agentName))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type effectiveLLMView struct {
	Source       string   `json:"source"`
	ProfileID    string   `json:"profile_id,omitempty"`
	ProfileName  string   `json:"profile_name,omitempty"`
	Engine       string   `json:"engine"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	BaseURL      string   `json:"base_url,omitempty"`
	OrgID        string   `json:"org_id,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	HasCred      bool     `json:"has_credential"`
	HostFallback bool     `json:"host_fallback"`
	Warnings     []string `json:"warnings,omitempty"`
}

func (s *Server) handleGetEffectiveLLM(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	at := s.lookupAgentType(wsID, agentName)
	fallback := engine.DefaultName
	if at != nil && at.Engine != "" {
		fallback = at.Engine
	}
	resolved, err := s.secretsStore.ResolveLLMForAgent(r.Context(), wsID, agentName, fallback)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// Must match the dispatcher's merge so the view shows what a run actually uses.
	if at != nil {
		resolved = resolved.ForAgent(at.Model, at.Effort)
	}
	view := effectiveLLMView{
		Source:       resolved.Source,
		ProfileID:    resolved.ProfileID,
		ProfileName:  resolved.ProfileName,
		Engine:       resolved.Engine,
		Model:        resolved.Model,
		Effort:       resolved.Effort,
		BaseURL:      resolved.BaseURL,
		OrgID:        resolved.OrgID,
		Provider:     resolved.Provider,
		HasCred:      resolved.Credential != "",
		HostFallback: s.hostLLMCredentialAllowed(),
	}
	if view.Model == "" {
		if desc, ok := engineByName(r.Context(), view.Engine); ok {
			view.Model = desc.DefaultModel
		}
	}
	if at != nil && resolved.Source == "binding" && !agentSupportsEngine(at, resolved.Engine) {
		view.Warnings = append(view.Warnings,
			i18n.TCtx(r.Context(), "api.llm.hints.engine_no_longer_declared", "engine", resolved.Engine))
	}
	writeJSON(w, http.StatusOK, view)
}

func agentSupportsEngine(at *types.AgentType, name string) bool {
	if at.SupportsEngine(name) {
		return true
	}
	return len(at.SupportedEngines) == 0 && at.Engine == "" && name == engine.DefaultName
}

// Treat the result as read-only: SupportedEngines is shared with the discovery result.
func (s *Server) lookupAgentType(workspaceID, name string) *types.AgentType {
	if s.taskDispatcher == nil {
		return nil
	}
	return s.taskDispatcher.FindAgentTypeForRelaunch(workspaceID, name)
}

func isAdminForcing(r *http.Request) bool {
	if strings.ToLower(strings.TrimSpace(r.Header.Get("X-Force-Engine"))) != "true" {
		return false
	}
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		return c.Role == auth.RoleAdmin || c.Role == auth.RoleOwner
	}
	return true
}

func actorIDFromRequest(r *http.Request) string {
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		if c.Email != "" {
			return c.Email
		}
		return c.UserID
	}
	return ""
}

func (s *Server) updatedProfileInput(
	ctx context.Context, desc engineDescriptor, existing *secrets.LLMProfile, req updateProfileReq,
) (secrets.LLMProfileInput, error) {
	current := storedProvider(ctx, existing)
	shape := profileShape{
		provider:       strings.TrimSpace(req.Provider),
		baseURL:        strings.TrimSpace(orExisting(req.BaseURL, existing.BaseURL)),
		orgID:          strings.TrimSpace(orExisting(req.OrgID, existing.OrgID)),
		authHeader:     strings.TrimSpace(orExisting(req.AuthHeader, existing.AuthHeader)),
		region:         strings.TrimSpace(orExisting(req.Region, existing.Region)),
		projectID:      strings.TrimSpace(orExisting(req.ProjectID, existing.ProjectID)),
		smallFastModel: strings.TrimSpace(orExisting(req.SmallFastModel, existing.SmallFastModel)),
	}
	if shape.provider == "" {
		shape.provider = current
		if !desc.supportsProvider(current) {
			shape.provider = desc.Provider
		}
	}
	if err := validateProfileShape(desc, shape); err != nil {
		return secrets.LLMProfileInput{}, err
	}
	kind := strings.TrimSpace(req.Kind)
	sec := req.trimmed()
	if in, federates, err := s.updatedFederation(desc, shape, existing, req); err != nil || federates {
		return in, err
	}
	cred, err := buildProfileCredential(desc, shape, sec, kind)
	if err != nil {
		return secrets.LLMProfileInput{}, err
	}
	if existing.Federation != nil && !cred.set && !engine.IsCloudProvider(shape.provider) {
		return secrets.LLMProfileInput{}, apperr.Invalid("api.llm.errors.key_required")
	}
	in := secrets.LLMProfileInput{
		Name:           req.Name,
		Engine:         desc.Name,
		Model:          orExisting(req.Model, existing.Model),
		Effort:         orExisting(req.Effort, existing.Effort),
		BaseURL:        shape.baseURL,
		OrgID:          shape.orgID,
		Provider:       shape.provider,
		AuthHeader:     shape.authHeader,
		Region:         shape.region,
		ProjectID:      shape.projectID,
		SmallFastModel: shape.smallFastModel,
		RawKey:         cred.raw,
		KeyHint:        cred.hint,
	}
	cloud := engine.IsCloudProvider(shape.provider)
	switch {
	case cred.set:
	case req.ClearKey && cloud:
		in.ClearKey = true
	case req.ClearKey:
		return secrets.LLMProfileInput{}, apperr.Invalid("api.llm.errors.clear_key_cloud_only")
	case shape.provider != current && cloud:
		in.ClearKey = true
	case shape.provider != current:
		return secrets.LLMProfileInput{}, apperr.Invalid("api.llm.errors.key_required_for_provider", "provider", shape.provider)
	}
	if in.ClearKey {
		if err := ambientCloudProfileAllowed(shape.provider); err != nil {
			return secrets.LLMProfileInput{}, err
		}
	}
	return in, nil
}

// Rows written before providers existed have none; they use the engine's own.
func storedProvider(ctx context.Context, p *secrets.LLMProfile) string {
	if p.Provider != "" {
		return p.Provider
	}
	if old, ok := engineByName(ctx, p.Engine); ok {
		return old.Provider
	}
	return ""
}

func (s *Server) updatedFederation(
	desc engineDescriptor, shape profileShape, existing *secrets.LLMProfile, req updateProfileReq,
) (secrets.LLMProfileInput, bool, error) {
	sec := req.trimmed()
	fedReq := req.Federation
	if fedReq == nil && existing.Federation != nil && sec.Key == "" && !sec.hasCloudFields() && !req.ClearKey {
		fedReq = existing.Federation
	}
	fed, federates, err := s.requestFederation(desc, shape, sec, strings.TrimSpace(req.Kind), fedReq)
	if err != nil || !federates {
		return secrets.LLMProfileInput{}, false, err
	}
	return secrets.LLMProfileInput{
		Name: req.Name, Engine: desc.Name,
		Model:      orExisting(req.Model, existing.Model),
		Effort:     orExisting(req.Effort, existing.Effort),
		Provider:   shape.provider,
		OrgID:      shape.orgID,
		Federation: fed,
		ClearKey:   true,
	}, true, nil
}

func ambientCloudProfileAllowed(provider string) error {
	if engine.AmbientCloudIdentityAllowed(os.Getenv) {
		return nil
	}
	return apperr.Invalid("api.llm.errors.cloud_profile_needs_credential",
		"provider", provider, "env", engine.EnvAmbientCloudIdentity)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate")
}

func orExisting(in *string, current string) string {
	if in == nil {
		return current
	}
	return *in
}

func (s *Server) auditLLMProfile(r *http.Request, kind audit.EventKind, p *secrets.LLMProfile, summary string) {
	if s.auditStore == nil || p == nil {
		return
	}
	s.auditStore.LogRequest(r, kind, p.ID, "llm_profile", summary, map[string]string{
		"workspace_id": p.WorkspaceID,
		"name":         p.Name,
		"engine":       p.Engine,
		"hint":         p.KeyHint,
	})
}

func (s *Server) auditAgentBinding(r *http.Request, kind audit.EventKind, agentName string, p *secrets.LLMProfile, summary string) {
	if s.auditStore == nil {
		return
	}
	auditWID, _ := s.requestWorkspace(r)
	meta := map[string]string{
		"workspace_id": auditWID,
		"agent":        agentName,
	}
	if p != nil {
		meta["profile_id"] = p.ID
		meta["profile_name"] = p.Name
		meta["engine"] = p.Engine
	}
	s.auditStore.LogRequest(r, kind, agentName, "agent_llm_binding", summary, meta)
}

func (s *Server) resolveLLMForAgent(ctx context.Context, workspaceID, agentName, fallbackEngine string) (secrets.ResolvedLLM, error) {
	if s.secretsStore == nil {
		return secrets.ResolvedLLM{Source: "host", Engine: fallbackEngine}, nil
	}
	return s.secretsStore.ResolveLLMForAgent(ctx, workspaceID, agentName, fallbackEngine)
}

func (s *Server) touchLLMProfile(ctx context.Context, workspaceID, profileID string) {
	if s.secretsStore == nil || workspaceID == "" || profileID == "" {
		return
	}
	logx.Warn(s.secretsStore.TouchLLMProfile(ctx, workspaceID, profileID),
		"api.llm.log.touch_profile_failed", "profile", profileID)
}

func (s *Server) auditLLMResolved(t *task.Task, agentName string, resolved secrets.ResolvedLLM) {
	if s.auditStore == nil || t == nil {
		return
	}
	s.auditStore.Log(audit.Entry{
		Kind:         audit.KindSecretAccessed,
		WorkspaceID:  t.WorkspaceID,
		ActorID:      "dispatcher",
		ResourceID:   agentName,
		ResourceType: "agent_llm_binding",
		Summary: i18n.Text("api.llm.audit.dispatched", "agent", agentName,
			"source", resolved.Source, "engine", resolved.Engine),
		Metadata: map[string]string{
			"task_id":      t.ID,
			"source":       resolved.Source,
			"engine":       resolved.Engine,
			"profile_id":   resolved.ProfileID,
			"profile_name": resolved.ProfileName,
		},
	})
}
