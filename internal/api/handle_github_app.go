package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type githubTokenLookup interface {
	GetGithubToken(ctx context.Context, projectID string) (*githubapp.Token, error)
}

func (s *Server) SetGitHubApp(svc *githubapp.Service, store *githubapp.Store) {
	s.gh.app = svc
	s.gh.store = store
	s.rewireGithubResolver()
}

// Called by both SetGitHubApp and SetCredentials so they can be wired in either order.
func (s *Server) rewireGithubResolver() {
	// Assign only non-nil pointers: a typed nil in an interface passes `== nil` checks and the
	// resolver would panic.
	var installs githubapp.InstallationLookup
	if s.gh.store != nil {
		installs = s.gh.store
	}
	var projects githubapp.ProjectLookup
	if s.projectStore != nil {
		projects = s.projectStore
	}
	var creds githubapp.CredentialLookup
	if s.credStore != nil {
		creds = s.credStore
	}
	resolver := githubapp.NewResolver(s.gh.app, installs, projects, creds)
	s.gh.tokens = resolver
	if s.taskDispatcher != nil {
		s.taskDispatcher.WithTokenLookup(resolver)
	}
}

func (s *Server) githubTokenForProject(ctx context.Context, projectID string) (*githubapp.Token, error) {
	if s.gh.tokens == nil {
		return nil, githubapp.ErrNoCredential
	}
	return s.gh.tokens.GetGithubToken(ctx, projectID)
}

func (s *Server) githubTokenForProjectWith(ctx context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error) {
	if s.gh.tokens == nil {
		return nil, githubapp.ErrNoCredential
	}
	if scoped, ok := s.gh.tokens.(interface {
		GetGithubTokenFor(ctx context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error)
	}); ok {
		return scoped.GetGithubTokenFor(ctx, projectID, perms)
	}
	return s.gh.tokens.GetGithubToken(ctx, projectID)
}

var tokenPermissionExtras = map[string]githubapp.Permissions{
	"actions:write":     {"actions": "write"},           //i18n:ignore GitHub permission token
	"deployments:write": githubapp.PermDeploymentsWrite, //i18n:ignore GitHub permission token
	"workflows:write":   githubapp.PermWorkflowsWrite,   //i18n:ignore GitHub permission token
	// An installation without Checks: read fails the mint and the endpoint answers 404; the MCP's
	// pr_checks then retries without it.
	"checks:read": githubapp.PermChecksRead, //i18n:ignore GitHub permission token
}

func tokenPermissionsParam(r *http.Request) (githubapp.Permissions, error) {
	raw := r.URL.Query().Get("permissions")
	if raw == "" {
		return githubapp.PermsAgentTools, nil
	}
	perms := githubapp.PermsAgentTools
	for _, name := range strings.Split(raw, ",") {
		extra, ok := tokenPermissionExtras[name]
		if !ok {
			return nil, apperr.Invalid("api.github.errors.unknown_permission", "name", name)
		}
		if name == "workflows:write" && !githubapp.WorkflowWritesAllowed() { //i18n:ignore GitHub permission token
			return nil, apperr.Invalid("api.github.errors.workflow_writes_disabled", "env", githubapp.EnvAllowWorkflowWrites)
		}
		perms = perms.With(extra)
	}
	return perms, nil
}

func (s *Server) githubAppReady(w http.ResponseWriter, r *http.Request) bool {
	if s.gh.app == nil || s.gh.store == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.app_unavailable")
		return false
	}
	return true
}

func (s *Server) handleGithubAppStatus(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.gh.app.Status(r.Context()))
}

type setGithubAppReq struct {
	AppID         int64  `json:"app_id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret"`
}

// One App serves every workspace, so owner role is not enough: the operator gate is the boundary.
func (s *Server) handleSetGithubApp(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) || !s.requireOperator(w, r,
		i18n.TCtx(r.Context(), "api.github.labels.operator_subject"),
		i18n.TCtx(r.Context(), "api.github.labels.operator_verb_configure")) {
		return
	}
	if !s.requireDBConfigurableApp(w, r) {
		return
	}
	var req setGithubAppReq
	if !decodeBody(w, r, &req) {
		return
	}
	cfg := githubapp.AppConfig{
		AppID:         req.AppID,
		Slug:          strings.TrimSpace(req.Slug),
		ClientID:      strings.TrimSpace(req.ClientID),
		ClientSecret:  strings.TrimSpace(req.ClientSecret),
		PrivateKey:    strings.TrimSpace(req.PrivateKey),
		WebhookSecret: strings.TrimSpace(req.WebhookSecret),
	}
	if cfg.AppID <= 0 || cfg.PrivateKey == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.app_id_and_key_required")
		return
	}
	meta, err := s.gh.app.ValidateConfig(r.Context(), cfg)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.Wrap(err, "api.github.errors.app_validation_failed"))
		return
	}
	if meta.Slug != "" {
		cfg.Slug = meta.Slug
	}
	if err := s.gh.store.SetConfig(r.Context(), cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// Also drops cached installation tokens: tokens minted under the old credentials must not
	// outlive them.
	s.gh.app.InvalidateConfig()
	s.auditGithubApp(r, "github.app.configured", strconv.FormatInt(cfg.AppID, 10),
		i18n.Text("api.github.audit.app_configured", "slug", cfg.Slug, "app_id", cfg.AppID))
	writeJSON(w, http.StatusOK, githubAppConnectResp{
		AppStatus:          s.gh.app.Status(r.Context()),
		MissingPermissions: appPermissionGaps(meta),
	})
}

func (s *Server) requireDBConfigurableApp(w http.ResponseWriter, r *http.Request) bool {
	if st := s.gh.app.Status(r.Context()); st.Configured && st.Source == "env" {
		writeErrKey(w, r, http.StatusConflict, "api.github.errors.app_configured_by_env")
		return false
	}
	return true
}

func (s *Server) handleDeleteGithubApp(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) || !s.requireOperator(w, r,
		i18n.TCtx(r.Context(), "api.github.labels.operator_subject"),
		i18n.TCtx(r.Context(), "api.github.labels.operator_verb_remove")) {
		return
	}
	if !s.requireDBConfigurableApp(w, r) {
		return
	}
	if err := s.gh.store.DeleteConfig(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// Cached installation tokens stay valid on GitHub for up to an hour; serving them after a
	// delete (key compromise) would keep access alive.
	s.gh.app.InvalidateConfig()
	s.auditGithubApp(r, "github.app.removed", "", i18n.Text("api.github.audit.app_removed"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

const (
	githubInstallBinder     = "octopus_gh_install"
	githubInstallBinderPath = "/github/"
)

func (s *Server) handleGithubInstallLink(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) {
		return
	}
	if !s.gh.app.Configured(r.Context()) {
		writeErrKey(w, r, http.StatusConflict, "api.github.errors.no_app_configured")
		return
	}
	// An empty workspace would leave the installation reachable by every tenant.
	wid, ok := s.newResourceWorkspace(w, r, "installation")
	if !ok {
		return
	}
	userID := ""
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		userID = c.UserID
	}
	state, binder, err := s.gh.app.IssueState(r.Context(), wid, userID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	link, err := s.gh.app.InstallURLWithState(r.Context(), state)
	if err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	auth.SetFlowBindCookie(w, r, githubInstallBinder, githubInstallBinderPath, binder,
		int(githubapp.InstallStateTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{"url": link})
}

// Installation ids are low-entropy: tenants must never see across workspaces.
func (s *Server) handleListGithubInstallations(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) {
		return
	}
	wid, unrestricted := s.callerWorkspace(r)
	list, err := s.gh.store.ListInstallations(r.Context(), wid, unrestricted)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	list = nonNil(list)
	writeJSON(w, http.StatusOK, list)
}

// Unlike unstamped rows, an unclaimed installation ("") is not public: only unrestricted operators
// until a state-verified claim.
func (s *Server) installationForCaller(w http.ResponseWriter, r *http.Request, id int64) (*githubapp.Installation, bool) {
	return forCaller(s, w, r,
		func() (*githubapp.Installation, error) { return s.gh.store.GetInstallation(r.Context(), id) },
		func(inst *githubapp.Installation) string { return inst.WorkspaceID },
		func(resourceWID, wid string, unrestricted bool) bool {
			return unrestricted || (resourceWID != "" && resourceWID == wid)
		},
		func(error) error { return apperr.NotFound("api.github.errors.installation_not_found") })
}

func (s *Server) handleListInstallationRepos(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.invalid_installation_id")
		return
	}
	inst, ok := s.installationForCaller(w, r, id)
	if !ok {
		return
	}
	repos, err := s.gh.app.ListInstallationRepos(r.Context(), inst.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, apperr.Wrap(err, "api.github.errors.upstream_failed"))
		return
	}
	repos = nonNil(repos)
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) handleUnlinkGithubInstallation(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.invalid_installation_id")
		return
	}
	inst, ok := s.installationForCaller(w, r, id)
	if !ok {
		return
	}
	if err := s.gh.store.DeleteInstallation(r.Context(), inst.InstallationID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditGithubApp(r, "github.app.installation_unlinked", strconv.FormatInt(id, 10),
		i18n.Text("api.github.audit.installation_unlinked", "id", id, "account", inst.AccountLogin))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// The redirect query is attacker-typed: persist only what is fetched back from GitHub with the App
// JWT.

func githubRedirect(w http.ResponseWriter, r *http.Request, query string) {
	http.Redirect(w, r, "/projects/?"+query, http.StatusFound)
}

func githubClaimFailed(reason string) string { return "github=error&reason=" + reason }

func (s *Server) claimInstallation(w http.ResponseWriter, r *http.Request, installationID int64, state, code string) string {
	ctx := r.Context()
	id := strconv.FormatInt(installationID, 10)
	if state == "" {
		return s.refreshInstallation(r, installationID)
	}

	binder := ""
	if c, err := r.Cookie(githubInstallBinder); err == nil {
		binder = c.Value
	}
	auth.ClearFlowBindCookie(w, r, githubInstallBinder, githubInstallBinderPath)
	flow, err := s.gh.app.ConsumeState(ctx, state, binder)
	if err != nil {
		logx.Warn(err, "api.github.log.setup_state_failed", "id", installationID)
		return githubClaimFailed("state")
	}
	remote, err := s.gh.app.FetchInstallation(ctx, installationID)
	if err != nil {
		logx.Warn(err, "api.github.log.setup_fetch_failed", "id", installationID)
		return githubClaimFailed("fetch")
	}

	prev, err := s.gh.store.GetInstallation(ctx, installationID)
	if err != nil && !errors.Is(err, githubapp.ErrInstallationNotFound) {
		logx.Warn(err, "api.github.log.setup_persist_failed", "id", installationID)
		return githubClaimFailed("store")
	}
	if prev != nil && prev.WorkspaceID != "" && prev.WorkspaceID != flow.WorkspaceID {
		// Logged in the asking workspace, so it must not name the workspace that holds the
		// installation.
		s.auditGithubClaim(flow, "github.app.installation_claim_refused", id,
			i18n.Text("api.github.audit.installation_claim_refused", "id", installationID,
				"account", remote.Account.Login))
		return githubClaimFailed("claimed")
	}

	installedBy := ""
	if prev == nil || prev.WorkspaceID == "" {
		login, err := s.proveInstaller(ctx, flow, installationID, remote, code)
		switch {
		case errors.Is(err, errInstallerProofMissing):
			return githubClaimFailed("authorize")
		case errors.Is(err, githubapp.ErrInstallerNotAuthorized):
			return githubClaimFailed("installer")
		case err != nil:
			return githubClaimFailed("oauth")
		}
		installedBy = login
	}

	inst := githubapp.Installation{
		InstallationID: installationID,
		WorkspaceID:    flow.WorkspaceID,
		AccountLogin:   remote.Account.Login,
		AccountType:    remote.Account.Type,
		RepoSelection:  remote.RepositorySelection,
		InstalledBy:    installedBy,
		SuspendedAt:    remote.SuspendedAt,
	}
	if err := s.gh.store.ClaimInstallation(ctx, inst); errors.Is(err, githubapp.ErrInstallationClaimed) {
		return githubClaimFailed("claimed")
	} else if err != nil {
		logx.Warn(err, "api.github.log.setup_persist_failed", "id", installationID)
		return githubClaimFailed("store")
	}
	s.syncInstallationSuspension(ctx, installationID, remote)
	s.auditGithubClaim(flow, "github.app.installation_connected", id,
		i18n.Text("api.github.audit.installation_connected", "id", installationID, "account", remote.Account.Login))
	return "github=connected&installation_id=" + id
}

var errInstallerProofMissing = apperr.Precondition("api.github.errors.installer_proof_missing")

// SaaS refuses a claim without a code; self-hosted accepts it, so a code that can't be exchanged
// there stays best-effort.
func (s *Server) proveInstaller(ctx context.Context, flow *githubapp.InstallState, installationID int64,
	remote *githubapp.RemoteInstallation, code string) (string, error) {
	if code == "" {
		if githubapp.InstallerProofRequired() {
			return "", errInstallerProofMissing
		}
		return "", nil
	}
	login, err := s.gh.app.VerifyInstaller(ctx, code, installationID)
	if errors.Is(err, githubapp.ErrInstallerNotAuthorized) {
		s.auditGithubClaim(flow, "github.app.installation_claim_refused", strconv.FormatInt(installationID, 10),
			i18n.Text("api.github.audit.installation_installer_refused", "id", installationID,
				"account", remote.Account.Login, "login", login))
		return "", err
	}
	if err != nil {
		logx.Warn(err, "api.github.log.oauth_exchange_failed")
		if githubapp.InstallerProofRequired() {
			return "", err
		}
		return "", nil
	}
	return login, nil
}

func (s *Server) refreshInstallation(r *http.Request, installationID int64) string {
	ctx := r.Context()
	remote, err := s.gh.app.FetchInstallation(ctx, installationID)
	if err != nil {
		logx.Warn(err, "api.github.log.setup_fetch_failed", "id", installationID)
		return githubClaimFailed("fetch")
	}
	err = s.gh.store.UpsertInstallation(ctx, githubapp.Installation{
		InstallationID: installationID,
		AccountLogin:   remote.Account.Login,
		AccountType:    remote.Account.Type,
		RepoSelection:  remote.RepositorySelection,
		SuspendedAt:    remote.SuspendedAt,
	})
	if err != nil {
		logx.Warn(err, "api.github.log.setup_persist_failed", "id", installationID)
		return githubClaimFailed("store")
	}
	s.syncInstallationSuspension(ctx, installationID, remote)
	inst, err := s.gh.store.GetInstallation(ctx, installationID)
	if err != nil || inst.WorkspaceID == "" {
		return githubClaimFailed("unclaimed")
	}
	return "github=connected&installation_id=" + strconv.FormatInt(installationID, 10)
}

// Upserts never clear suspended_at (partial webhook payloads must not unsuspend); a fetch is
// authoritative, so set it explicitly.
func (s *Server) syncInstallationSuspension(ctx context.Context, installationID int64, remote *githubapp.RemoteInstallation) {
	logx.Warn(s.gh.store.SetSuspended(ctx, installationID, remote.SuspendedAt != nil),
		"api.github.log.setup_sync_suspension_failed", "id", installationID)
}

func (s *Server) handleGithubSetup(w http.ResponseWriter, r *http.Request) {
	if s.gh.app == nil || !s.gh.app.Configured(r.Context()) {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.app_not_configured")
		return
	}
	q := r.URL.Query()
	// A non-admin org member only requests the install: GitHub redirects with setup_action=request
	// and no installation_id.
	if q.Get("setup_action") == "request" {
		githubRedirect(w, r, "github=requested")
		return
	}
	installationID, err := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if err != nil || installationID <= 0 {
		githubRedirect(w, r, githubClaimFailed("params"))
		return
	}
	githubRedirect(w, r, s.claimInstallation(w, r, installationID, q.Get("state"), ""))
}

func (s *Server) handleGithubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.gh.app == nil || !s.gh.app.Configured(r.Context()) {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.app_not_configured")
		return
	}
	q := r.URL.Query()
	if q.Get("setup_action") == "request" {
		githubRedirect(w, r, "github=requested")
		return
	}
	installationID, err := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if err != nil || installationID <= 0 {
		githubRedirect(w, r, "github=authorized")
		return
	}
	githubRedirect(w, r, s.claimInstallation(w, r, installationID, q.Get("state"), q.Get("code")))
}

func (s *Server) handleReassignGithubInstallation(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) || !s.requireOperator(w, r,
		"api.github.labels.operator_subject_installations", "api.github.labels.operator_verb_reassign") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.invalid_installation_id")
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	if req.WorkspaceID != "" && s.authStore != nil {
		if ws, wsErr := s.authStore.GetWorkspace(r.Context(), req.WorkspaceID); wsErr != nil || ws == nil {
			writeErrKey(w, r, http.StatusNotFound, "api.workspace.errors.not_found")
			return
		}
	}
	prev, err := s.gh.store.ReassignInstallation(r.Context(), id, req.WorkspaceID)
	if errors.Is(err, githubapp.ErrInstallationNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.installation_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditGithubApp(r, "github.app.installation_reassigned", strconv.FormatInt(id, 10),
		i18n.Text("api.github.audit.installation_reassigned", "id", id, "from", prev, "to", req.WorkspaceID))
	inst, err := s.gh.store.GetInstallation(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

type githubWebhookEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string     `json:"repository_selection"`
		SuspendedAt         *time.Time `json:"suspended_at"`
	} `json:"installation"`
}

func (s *Server) handleGithubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.gh.app == nil || s.gh.store == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.app_not_configured")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.read_body")
		return
	}
	if !s.gh.app.VerifyWebhookSignature(r.Context(), payload, r.Header.Get("X-Hub-Signature-256")) {
		writeErrKey(w, r, http.StatusUnauthorized, "api.github.errors.invalid_signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	s.invalidatePRStatus(event, payload)
	s.evidenceDeployHook(r.Context(), event, payload)
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case "installation", "installation_repositories", "installation_target":
		var ev githubWebhookEvent
		if err := json.Unmarshal(payload, &ev); err != nil || ev.Installation.ID == 0 {
			writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.malformed_payload")
			return
		}
		if err := s.applyInstallationEvent(r.Context(), event, ev); err != nil {
			logx.Warn(err, "api.github.log.webhook_apply_failed",
				"event", event, "action", ev.Action, "id", ev.Installation.ID)
			writeErrKey(w, r, http.StatusInternalServerError, "api.github.errors.apply_event")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	default:
		if kind := githubEventKind(event); kind != "" {
			var ev githubEventPayload
			if err := json.Unmarshal(payload, &ev); err != nil {
				// Accept anyway: answering 4xx makes GitHub disable the hook.
				logx.Warn(err, "api.github.log.webhook_decode_failed", "event", event)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if workspaceID := s.githubEventWorkspace(r.Context(), ev); workspaceID != "" {
				data, dedupeKey := s.automationPayload(r.Context(), event, workspaceID, ev)
				s.publishAutomationEvent(r.Context(), kind, workspaceID, dedupeKey, data)
			} else {
				// Log only kind and numeric id: the rest of the payload is sender-controlled text.
				log.Printf("[github] %s", i18n.Log("api.github.log.webhook_unattributed",
					"kind", kind, "installation", ev.Installation.ID))
			}
		}
		// Always acknowledge, or GitHub marks deliveries failed and disables the hook.
		w.WriteHeader(http.StatusNoContent)
		return
	}
}

// Webhooks carry no workspace: these upserts must never claim a row (UpsertInstallation keeps
// existing claims for empty workspace ids).
func (s *Server) applyInstallationEvent(ctx context.Context, event string, ev githubWebhookEvent) error {
	id := ev.Installation.ID
	if event == "installation" {
		switch ev.Action {
		case "deleted":
			return s.gh.store.DeleteInstallation(ctx, id)
		case "suspend":
			return s.gh.store.SetSuspended(ctx, id, true)
		case "unsuspend":
			return s.gh.store.SetSuspended(ctx, id, false)
		}
	}
	return s.gh.store.UpsertInstallation(ctx, githubapp.Installation{
		InstallationID: id,
		AccountLogin:   ev.Installation.Account.Login,
		AccountType:    ev.Installation.Account.Type,
		RepoSelection:  ev.Installation.RepositorySelection,
		SuspendedAt:    ev.Installation.SuspendedAt,
	})
}

func (s *Server) auditGithubClaim(flow *githubapp.InstallState, kind, resourceID, summary string) {
	if s.auditStore == nil {
		return
	}
	s.auditStore.Log(audit.Entry{
		Kind:         audit.EventKind(kind),
		WorkspaceID:  flow.WorkspaceID,
		ActorID:      flow.UserID,
		ResourceID:   resourceID,
		ResourceType: "github_app",
		Summary:      summary,
	})
}

func (s *Server) auditGithubApp(r *http.Request, kind, resourceID, summary string) {
	if s.auditStore == nil {
		return
	}
	var actorID, actorEmail, wid string
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		actorID, actorEmail, wid = c.UserID, c.Email, c.WorkspaceID
	}
	s.auditStore.Log(audit.Entry{
		Kind:         audit.EventKind(kind),
		WorkspaceID:  wid,
		ActorID:      actorID,
		ActorEmail:   actorEmail,
		ResourceID:   resourceID,
		ResourceType: "github_app",
		Summary:      summary,
	})
}
