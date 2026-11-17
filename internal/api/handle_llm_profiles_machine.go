package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

// runnerReader is what the profile endpoints read of registered machines.
type runnerReader interface {
	Get(ctx context.Context, workspaceID, id string) (*runner.Runner, error)
	Inventory(ctx context.Context, workspaceID, id string) (*runner.Inventory, error)
}

// validateMachineProfile checks a profile paid by a machine's own CLI login.
func (s *Server) validateMachineProfile(
	ctx context.Context, wsID, runnerID string, desc engineDescriptor, shape profileShape, sec profileSecretFields,
	fed *engine.FederationConfig,
) error {
	if !desc.MachineLogin {
		return apperr.Invalid("api.llm.errors.machine_login_unsupported", "engine", desc.Label)
	}
	if shape.provider != "" && shape.provider != desc.Provider {
		return apperr.Invalid("api.llm.errors.machine_profile_provider",
			"engine", desc.Label, "provider", desc.Provider, "got", shape.provider)
	}
	if shape != (profileShape{provider: shape.provider}) || sec.Key != "" || sec.hasCloudFields() ||
		(fed != nil && fed.Trimmed().Set()) {
		return apperr.Invalid("api.llm.errors.machine_profile_shape")
	}
	if s.runners == nil {
		return apperr.Precondition("api.runners.errors.unavailable")
	}
	r, err := s.runners.Get(ctx, wsID, runnerID)
	if apperr.KindOf(err) == apperr.KindNotFound {
		return apperr.Invalid("api.llm.errors.runner_not_found", "runner", runnerID)
	}
	if err != nil {
		return err
	}
	if r.RevokedAt != nil {
		return apperr.Invalid("api.llm.errors.runner_revoked", "runner", r.Name)
	}
	return nil
}

func machineProfileInput(name, model, effort, runnerID string, desc engineDescriptor, isDefault bool) secrets.LLMProfileInput {
	return secrets.LLMProfileInput{
		Name: name, Engine: desc.Name, Model: model, Effort: effort,
		Provider: desc.Provider, RunnerID: runnerID, KeyOptional: true, IsDefault: isDefault,
	}
}

func (s *Server) createMachineProfile(w http.ResponseWriter, r *http.Request, desc engineDescriptor, shape profileShape, req createProfileReq) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	runnerID := strings.TrimSpace(req.RunnerID)
	if err := s.validateMachineProfile(r.Context(), wsID, runnerID, desc, shape, req.trimmed(), req.Federation); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	prof, err := s.secretsStore.CreateLLMProfile(r.Context(), wsID,
		machineProfileInput(req.Name, req.Model, req.Effort, runnerID, desc, req.IsDefault))
	if err != nil {
		if isUniqueViolation(err) {
			writeErrKey(w, r, http.StatusConflict, "api.llm.errors.profile_name_taken")
			return
		}
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.auditLLMProfile(r, audit.KindLLMProfileCreated, prof, i18n.Text("api.llm.audit.profile_created", "name", prof.Name))
	writeJSON(w, http.StatusCreated, prof)
}

// updatedMachineProfileInput builds the update of a machine profile. The kind never changes; the
// machine may.
func (s *Server) updatedMachineProfileInput(
	ctx context.Context, desc engineDescriptor, existing *secrets.LLMProfile, req updateProfileReq,
) (secrets.LLMProfileInput, error) {
	runnerID := strings.TrimSpace(orExisting(req.RunnerID, existing.RunnerID))
	shape := profileShape{
		provider:       strings.TrimSpace(req.Provider),
		baseURL:        strings.TrimSpace(orExisting(req.BaseURL, "")),
		orgID:          strings.TrimSpace(orExisting(req.OrgID, "")),
		authHeader:     strings.TrimSpace(orExisting(req.AuthHeader, "")),
		region:         strings.TrimSpace(orExisting(req.Region, "")),
		projectID:      strings.TrimSpace(orExisting(req.ProjectID, "")),
		smallFastModel: strings.TrimSpace(orExisting(req.SmallFastModel, "")),
	}
	if err := s.validateMachineProfile(ctx, existing.WorkspaceID, runnerID, desc, shape, req.trimmed(), req.Federation); err != nil {
		return secrets.LLMProfileInput{}, err
	}
	if req.ClearKey {
		return secrets.LLMProfileInput{}, apperr.Invalid("api.llm.errors.machine_profile_shape")
	}
	return machineProfileInput(req.Name, orExisting(req.Model, existing.Model), orExisting(req.Effort, existing.Effort),
		runnerID, desc, false), nil
}

// profileUpdateInput builds what an update writes, refusing a change of kind. machine skips the key
// checks that follow: a machine profile holds no key.
func (s *Server) profileUpdateInput(
	r *http.Request, desc engineDescriptor, existing *secrets.LLMProfile, req updateProfileReq,
) (in secrets.LLMProfileInput, machine bool, status int, err error) {
	if locked := profileKindLocked(existing, req); locked != nil {
		return in, false, http.StatusConflict, locked
	}
	if existing.RunnerID != "" {
		in, err = s.updatedMachineProfileInput(r.Context(), desc, existing, req)
		return in, true, apperr.Status(err), err
	}
	if in, err = s.updatedProfileInput(r.Context(), desc, existing, req); err != nil {
		return in, false, http.StatusBadRequest, err
	}
	status, err = s.ownerOnlyUpdate(r, desc, existing, strings.TrimSpace(req.Kind), in)
	return in, false, status, err
}

// profileKindLocked refuses an update that would turn a key profile into a machine one or back.
func profileKindLocked(existing *secrets.LLMProfile, req updateProfileReq) error {
	if req.RunnerID == nil || (strings.TrimSpace(*req.RunnerID) == "") == (existing.RunnerID == "") {
		return nil
	}
	return apperr.Conflict("api.llm.errors.profile_kind_locked")
}

// machineState is what the profile list shows about the machine a profile runs on.
type machineState struct {
	RunnerName  string              `json:"runner_name,omitempty"`
	EngineState *runner.EngineState `json:"engine_state,omitempty"`
}

func (s *Server) machineStateOf(ctx context.Context, p *secrets.LLMProfile) machineState {
	if p.RunnerID == "" || s.runners == nil {
		return machineState{}
	}
	var out machineState
	if r, err := s.runners.Get(ctx, p.WorkspaceID, p.RunnerID); err == nil {
		out.RunnerName = r.Name
	}
	if inv, err := s.runners.Inventory(ctx, p.WorkspaceID, p.RunnerID); err == nil {
		out.EngineState = inv.Engine(p.Engine)
	}
	return out
}

// machineWarnings says why runs on a machine profile would be refused or fail; revoked means it
// is not runnable at all. An error is a failed read, which says nothing about the machine.
func (s *Server) machineWarnings(
	ctx context.Context, wsID string, resolved secrets.ResolvedLLM,
) (name string, warnings []string, revoked bool, err error) {
	if s.runners == nil {
		return resolved.RunnerID, []string{i18n.TCtx(ctx, "api.runners.errors.unavailable")}, true, nil
	}
	r, err := s.runners.Get(ctx, wsID, resolved.RunnerID)
	if apperr.KindOf(err) == apperr.KindNotFound {
		return resolved.RunnerID, []string{i18n.TCtx(ctx, "api.llm.errors.runner_not_found", "runner", resolved.RunnerID)}, true, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if r.RevokedAt != nil {
		return r.Name, []string{i18n.TCtx(ctx, "api.llm.hints.runner_revoked", "runner", r.Name)}, true, nil
	}
	if !r.Online {
		warnings = append(warnings, i18n.TCtx(ctx, "api.llm.hints.runner_offline", "runner", r.Name))
	}
	label := resolved.Engine
	if desc, ok := engineByName(ctx, resolved.Engine); ok {
		label = desc.Label
	}
	var st *runner.EngineState
	if inv, err := s.runners.Inventory(ctx, wsID, resolved.RunnerID); err == nil {
		st = inv.Engine(resolved.Engine)
	}
	switch {
	case st == nil || !st.Installed:
		warnings = append(warnings, i18n.TCtx(ctx, "api.llm.hints.runner_engine_missing", "runner", r.Name, "engine", label))
	case !st.LoggedIn:
		warnings = append(warnings, i18n.TCtx(ctx, "api.llm.hints.runner_not_logged_in", "runner", r.Name, "engine", label))
	}
	return r.Name, warnings, false, nil
}
