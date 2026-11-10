package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func (s *Server) requireCredentials(w http.ResponseWriter, r *http.Request) bool {
	if s.credStore == nil || s.credAuth == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.credentials.errors.not_configured")
		return false
	}
	return true
}

// Another workspace's credential and a missing one answer the same 404 (see forCaller in scope.go).
func (s *Server) credentialForCaller(w http.ResponseWriter, r *http.Request) (*credentials.Credential, bool) {
	id := r.PathValue("id")
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return nil, false
	}
	c, err := s.credStore.Get(r.Context(), callerWID, id)
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.credentials.errors.not_found")
		return nil, false
	}
	return c, true
}

func (s *Server) handleListCredentialProviders(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": s.credRegistry.SpecsIn(r.Context()),
		"tools":     s.credAuth.Catalog().AllIn(r.Context()),
	})
}

type credentialReq struct {
	Name     string              `json:"name"`
	Provider string              `json:"provider"`
	Kind     string              `json:"kind"`
	Payload  map[string]string   `json:"payload"`
	Grants   []credentials.Grant `json:"grants"`
}

type credentialView struct {
	*credentials.Credential
	Tools []credentials.ToolAvailability `json:"tools"`
}

func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	list, err := s.credStore.List(r.Context(), callerWID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": list})
}

func (s *Server) handleGetCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	tools, err := s.credAuth.ToolMatrix(r.Context(), c)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialView{Credential: c, Tools: tools})
}

func (s *Server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	var req credentialReq
	if !decodeBody(w, r, &req) {
		return
	}
	if credentials.IsOAuth(req.Provider) {
		// OAuth payloads are tokens from an authorization server; only the Connect flow may write
		// them.
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.oauth_use_connect")
		return
	}
	if err := s.credRegistry.ValidatePayload(req.Provider, req.Kind, req.Payload); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	for _, g := range req.Grants {
		if err := g.Valid(); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	c, err := s.credStore.Create(r.Context(), wid, credentials.Input{
		Name: req.Name, Provider: req.Provider, Kind: req.Kind, Payload: req.Payload,
	}, authorLabel(r))
	if err != nil {
		writeCredentialError(w, r, err)
		return
	}
	if len(req.Grants) > 0 {
		if err := s.credStore.SetGrants(r.Context(), wid, c.ID, req.Grants); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	s.auditCredential(r, audit.KindCredentialCreated, c,
		i18n.Text("api.credentials.audit.created", "provider", c.Provider, "name", c.Name))
	s.introspectAndRespond(w, r, wid, c.ID)
}

func (s *Server) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	cur, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	var req credentialReq
	if !decodeBody(w, r, &req) {
		return
	}
	rotating := len(req.Payload) > 0
	if rotating && credentials.IsOAuth(cur.Provider) {
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.oauth_use_connect")
		return
	}
	if rotating {
		kind := req.Kind
		if kind == "" {
			kind = cur.Kind
		}
		if err := s.credRegistry.ValidatePayload(cur.Provider, kind, req.Payload); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	updated, err := s.credStore.Update(r.Context(), wid, cur.ID, credentials.Input{
		Name: req.Name, Kind: req.Kind, Payload: req.Payload,
	})
	if err != nil {
		writeCredentialError(w, r, err)
		return
	}
	summary := i18n.Text("api.credentials.audit.updated", "name", updated.Name)
	if rotating {
		summary = i18n.Text("api.credentials.audit.rotated", "name", updated.Name)
	}
	s.auditCredential(r, audit.KindCredentialUpdated, updated, summary)
	if rotating {
		s.introspectAndRespond(w, r, wid, updated.ID)
		return
	}
	s.respondCredential(w, r, updated)
}

func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	if err := s.credStore.Delete(r.Context(), c.WorkspaceID, c.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditCredential(r, audit.KindCredentialDeleted, c, i18n.Text("api.credentials.audit.deleted", "name", c.Name))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleValidateCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	s.introspectAndRespond(w, r, c.WorkspaceID, c.ID)
}

func (s *Server) handleSetCredentialDisabled(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.credStore.SetDisabled(r.Context(), c.WorkspaceID, c.ID, req.Disabled); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	summary := i18n.Text("api.credentials.audit.enabled", "name", c.Name)
	if req.Disabled {
		summary = i18n.Text("api.credentials.audit.disabled", "name", c.Name)
	}
	s.auditCredential(r, audit.KindCredentialUpdated, c, summary)
	s.reloadAndRespond(w, r, c.WorkspaceID, c.ID)
}

func (s *Server) handleSetCredentialGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Grants []credentials.Grant `json:"grants"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.credStore.SetGrants(r.Context(), c.WorkspaceID, c.ID, req.Grants); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.auditCredential(r, audit.KindCredentialGrantsChanged, c,
		i18n.Text("api.credentials.audit.grants_changed", "name", c.Name))
	s.reloadAndRespond(w, r, c.WorkspaceID, c.ID)
}

func (s *Server) handleSetCredentialTools(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Tools []credentials.ToolPolicy `json:"tools"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.credStore.SetToolPolicy(r.Context(), c.WorkspaceID, c.ID, req.Tools, authorLabel(r)); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.auditCredential(r, audit.KindCredentialToolsChanged, c,
		i18n.Text("api.credentials.audit.tools_changed", "name", c.Name))
	s.reloadAndRespond(w, r, c.WorkspaceID, c.ID)
}

func (s *Server) handleListCredentialDecisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) {
		return
	}
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	list, err := s.credStore.ListDecisions(r.Context(), callerWID, 100)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": s.decisionViews(r.Context(), list)})
}

type decisionView struct {
	credentials.DecisionRecord
	TaskTitle   string `json:"task_title,omitempty"`
	StageLabel  string `json:"stage_label,omitempty"`
	TaskBoardID string `json:"task_board_id,omitempty"`
}

func (s *Server) decisionViews(ctx context.Context, list []credentials.DecisionRecord) []decisionView {
	out := make([]decisionView, 0, len(list))
	tasks := map[string]*task.Task{}
	for i := range list {
		v := decisionView{DecisionRecord: list[i]}
		if t := s.decisionTask(ctx, tasks, list[i].TaskID); t != nil {
			v.TaskTitle, v.TaskBoardID = t.Title, t.BoardID
			if info, ok := s.stagesFor(t).Get(types.Stage(list[i].Stage)); ok && list[i].Stage != "" {
				v.StageLabel = info.Label
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) decisionTask(ctx context.Context, seen map[string]*task.Task, id string) *task.Task {
	if id == "" || s.taskStore == nil {
		return nil
	}
	if t, ok := seen[id]; ok {
		return t
	}
	t, err := s.taskStore.Get(ctx, id)
	if err != nil {
		t = nil
	}
	seen[id] = t
	return t
}

func (s *Server) handleInternalAvailableCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) || !s.requireInternalCaller(w, r, false) {
		return
	}
	sess, ok := s.credentialSession(w, r)
	if !ok {
		return
	}
	reachable, err := s.credStore.Reachable(r.Context(), sess.reach)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	tools, err := s.credAuth.ListAvailable(r.Context(), credentials.ListRequest{
		Reach:  sess.reach,
		TaskID: sess.taskID,
		Agent:  sess.agent,
		Stage:  sess.stage,
		Server: r.URL.Query().Get("server"),
		// The allowlist the run was offered, never one the caller names.
		StageTools: sess.stageTools,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":     sess.taskID,
		"credentials": reachable,
		"tools":       tools,
	})
}

// This grant check is the boundary: the daemon holds the secret. The MCP-side check is only defense
// in depth.
func (s *Server) handleInternalResolveCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireCredentials(w, r) || !s.requireInternalCaller(w, r, true) {
		return
	}
	sess, ok := s.credentialSession(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	req := credentials.Request{
		Reach:        sess.reach,
		TaskID:       sess.taskID,
		Agent:        sess.agent,
		Stage:        sess.stage,
		Server:       q.Get("server"),
		Tool:         q.Get("tool"),
		CredentialID: q.Get("credential_id"),
		StageTools:   sess.stageTools,
	}
	if req.Server == "" || req.Tool == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.server_and_tool_required")
		return
	}
	if req.CredentialID == "" {
		req.Subject = subjectFromQuery(q.Get("environment_id"), q.Get("project_id"))
	}

	decision, err := s.credAuth.Authorize(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordCredentialDecision(r.Context(), sess, req, decision)

	if !decision.Allowed {
		writeJSON(w, http.StatusForbidden, map[string]any{"decision": decision})
		return
	}
	if decision.CredentialID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"decision": decision})
		return
	}
	cred, err := s.credStore.Get(r.Context(), sess.reach.WorkspaceID, decision.CredentialID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if credentials.IsOAuth(cred.Provider) {
		// OAuth tokens never leave the daemon; only the MCP proxy uses them.
		writeJSON(w, http.StatusForbidden, map[string]any{"decision": credentials.Decision{
			Reason: credentials.ReasonProviderUnsupported, CredentialID: cred.ID, Provider: cred.Provider,
			Detail: i18n.TCtx(r.Context(), "api.credentials.errors.oauth_not_resolvable"),
		}})
		return
	}
	payload, err := s.credStore.Payload(r.Context(), sess.reach.WorkspaceID, decision.CredentialID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"decision":      decision,
		"credential_id": cred.ID,
		"name":          cred.Name,
		"provider":      cred.Provider,
		"kind":          cred.Kind,
		"identity":      cred.Identity,
		"payload":       payload,
	})
}

// Every field comes from the task the API key is scoped to, never from query parameters, so the
// agent cannot forge its reach.
type credSession struct {
	taskID     string
	agent      string
	stage      string
	reach      credentials.Reach
	stageTools []string
}

// ?task= is only a hint: a scoped key wins and a mismatch is refused.
func (s *Server) credentialSession(w http.ResponseWriter, r *http.Request) (credSession, bool) {
	runScope := auth.APIKeyRunScope(r.Context())
	scoped := runScope.TaskID
	claimed := strings.TrimSpace(r.URL.Query().Get("task"))
	taskID := scoped
	switch {
	case scoped != "" && claimed != "" && scoped != claimed:
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.key_scoped_to_other_task")
		return credSession{}, false
	case scoped == "" && runScope.IsRun():
		// A run with no task (unlinked chat) must not borrow reach by naming one.
		writeErrKey(w, r, http.StatusForbidden, "api.credentials.errors.run_without_task")
		return credSession{}, false
	case scoped == "":
		taskID = claimed
	}
	if taskID == "" {
		// Unreachable for runs: a task-scoped key always yields its task above, so omitting ?task=
		// cannot widen an agent's reach.
		return s.subjectSession(w, r)
	}
	t, err := s.taskStore.Get(r.Context(), taskID)
	if err != nil || !s.internalVisible(r, t.WorkspaceID) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.task_not_found")
		return credSession{}, false
	}

	return credSession{
		taskID:     t.ID,
		agent:      runScope.Agent,
		stage:      string(t.Stage),
		reach:      s.reachForTask(r.Context(), t),
		stageTools: s.credentialStageTools(runScope, t),
	}, true
}

// Reach is exactly the named environment/project, not the whole workspace; a cross-workspace miss
// answers like a missing environment.
func (s *Server) subjectSession(w http.ResponseWriter, r *http.Request) (credSession, bool) {
	q := r.URL.Query()
	envID := strings.TrimSpace(q.Get("environment_id"))
	projectID := strings.TrimSpace(q.Get("project_id"))
	if envID == "" && projectID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.subject_required")
		return credSession{}, false
	}

	reach := credentials.Reach{}
	if envID != "" {
		env, err := s.projectStore.FindEnvironment(r.Context(), envID)
		if err != nil {
			writeErrKey(w, r, http.StatusNotFound, "api.credentials.errors.environment_not_found")
			return credSession{}, false
		}
		reach.EnvironmentIDs = []string{env.ID}
		projectID = env.ProjectID
	}
	if projectID != "" {
		proj, err := s.projectStore.Get(r.Context(), projectID)
		if err != nil || !s.internalVisible(r, proj.WorkspaceID) {
			if envID != "" {
				writeErrKey(w, r, http.StatusNotFound, "api.credentials.errors.environment_not_found")
			} else {
				writeErrKey(w, r, http.StatusNotFound, "api.github.errors.project_not_found")
			}
			return credSession{}, false
		}
		reach.ProjectIDs = []string{projectID}
		reach.WorkspaceID = proj.WorkspaceID
	}
	if reach.WorkspaceID == "" {
		reach.WorkspaceID = defaultWorkspaceID
	}
	return credSession{reach: reach}, true
}

// The pre-launch credential gate must use this same function, or it would refuse runs that work or
// pass runs that fail.
func (s *Server) reachForTask(ctx context.Context, t *task.Task) credentials.Reach {
	reach := credentials.Reach{
		WorkspaceID: t.WorkspaceID,
		ProjectIDs:  t.ProjectIDs,
	}
	if reach.WorkspaceID == "" {
		// Tasks with no workspace belong, with their credentials, to the default
		// workspace.
		reach.WorkspaceID = defaultWorkspaceID
	}
	for _, pid := range t.ProjectIDs {
		envs, envErr := s.projectStore.ListEnvironments(ctx, pid)
		if envErr != nil {
			// Skip rather than fail: one project's listing error must not break every cloud tool.
			logx.Warn(envErr, "api.credentials.log.list_environments_failed", "project", pid)
			continue
		}
		for i := range envs {
			reach.EnvironmentIDs = append(reach.EnvironmentIDs, envs[i].ID)
		}
	}
	return reach
}

func subjectFromQuery(envID, projectID string) *credentials.Grant {
	switch {
	case strings.TrimSpace(envID) != "":
		return &credentials.Grant{
			SubjectType: credentials.SubjectEnvironment,
			SubjectID:   strings.TrimSpace(envID),
		}
	case strings.TrimSpace(projectID) != "":
		return &credentials.Grant{
			SubjectType: credentials.SubjectProject,
			SubjectID:   strings.TrimSpace(projectID),
		}
	default:
		return nil
	}
}

func (s *Server) introspectAndRespond(w http.ResponseWriter, r *http.Request, workspaceID, id string) {
	if err := s.introspectCredential(r.Context(), workspaceID, id); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.reloadAndRespond(w, r, workspaceID, id)
}

// The error means recording failed; a provider rejection succeeds and leaves state=invalid.
func (s *Server) introspectCredential(ctx context.Context, workspaceID, id string) error {
	c, err := s.credStore.Get(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	payload, err := s.credStore.Payload(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	res, err := s.credRegistry.Introspect(ctx, c.Provider, c.Kind, payload,
		s.credAuth.Catalog().Permissions())
	if err != nil {
		// Unreachable provider is not a verdict: keep the last introspection. Blanking it would
		// mean no capabilities,
		// which is treated as capable of everything. See Store.SetIntrospectionError.
		logx.Warn(err, "api.credentials.log.provider_unreachable", "provider", c.Provider, "id", id)
		return s.credStore.SetIntrospectionError(ctx, workspaceID, id, credentials.IntrospectUnreachable)
	}
	return s.credStore.SetIntrospection(ctx, workspaceID, id, res)
}

func (s *Server) reloadAndRespond(w http.ResponseWriter, r *http.Request, workspaceID, id string) {
	c, err := s.credStore.Get(r.Context(), workspaceID, id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.respondCredential(w, r, c)
}

func (s *Server) respondCredential(w http.ResponseWriter, r *http.Request, c *credentials.Credential) {
	tools, err := s.credAuth.ToolMatrix(r.Context(), c)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialView{Credential: c, Tools: tools})
}

func writeCredentialError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, credentials.ErrExists):
		writeError(w, r, http.StatusConflict, err)
	case errors.Is(err, credentials.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.credentials.errors.not_found")
	default:
		writeError(w, r, http.StatusBadRequest, err)
	}
}

// Logs and continues: an audit write must never fail a tool call.
func (s *Server) recordCredentialDecision(ctx context.Context, sess credSession, req credentials.Request, d credentials.Decision) {
	logx.Warn(s.credStore.RecordDecision(ctx, credentials.DecisionRecord{
		WorkspaceID:  sess.reach.WorkspaceID,
		CredentialID: d.CredentialID,
		TaskID:       sess.taskID,
		Agent:        req.Agent,
		Stage:        sess.stage,
		Server:       req.Server,
		Tool:         req.Tool,
		Allowed:      d.Allowed,
		Reason:       string(d.Reason),
	}), "api.credentials.log.record_decision_failed", "task", sess.taskID)
}

func (s *Server) auditCredential(r *http.Request, kind audit.EventKind, c *credentials.Credential, summary string) {
	if s.auditStore == nil {
		return
	}
	s.auditStore.LogRequest(r, kind, c.ID, "credential", summary, map[string]string{
		"provider": c.Provider,
		"name":     c.Name,
	})
}
