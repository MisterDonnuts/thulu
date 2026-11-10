package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpproxy"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

type mcpProxyDeps struct {
	proxy *mcpproxy.Proxy
	auth  mcpproxy.UpstreamAuth
}

// Upstreams are operator-registered: internal policy (private addresses allowed, metadata refused).
// No client timeout: SSE streams live as long as the agent's session; the request context bounds
// each call.
func (s *Server) initMCPProxy() {
	s.mcp.proxy.proxy = mcpproxy.New(&http.Client{Transport: netguard.NewInternal().Transport()})
	s.rewireMCPProxyAuth()
}

// Called again from SetCredentials, which may run after New.
func (s *Server) rewireMCPProxyAuth() {
	if s.credStore == nil || s.credAuth == nil {
		s.mcp.proxy.auth = nil
		return
	}
	var tokens mcpproxy.OAuthTokens
	if s.mcpOAuth != nil {
		tokens = s.mcpOAuth
	}
	s.mcp.proxy.auth = &mcpproxy.StaticHeaderAuth{
		Authorizer: s.credAuth,
		Store:      s.credStore,
		Tokens:     tokens,
		OnRefusal: func(ctx context.Context, reg mcp.Registration, run mcpproxy.Run, d credentials.Decision) {
			logx.Warn(s.credStore.RecordDecision(ctx, credentials.DecisionRecord{
				WorkspaceID: run.Reach.WorkspaceID, CredentialID: d.CredentialID, TaskID: run.TaskID,
				Agent: run.Agent, Stage: run.Stage, Server: reg.Name, Allowed: false, Reason: string(d.Reason),
			}), "api.credentials.log.record_decision_failed", "task", run.TaskID)
		},
	}
}

func (s *Server) handleMCPProxy(w http.ResponseWriter, r *http.Request) {
	target, run, ok := s.mcpProxyTarget(w, r)
	if !ok {
		return
	}
	if target.Transport == mcp.TransportHTTP {
		s.mcp.proxy.proxy.Streamable(w, r, target)
		return
	}
	if r.Method != http.MethodGet {
		writeErrKey(w, r, http.StatusMethodNotAllowed, "api.mcpproxy.errors.sse_get_only", "server", target.Name)
		return
	}
	base := strings.TrimSuffix(r.URL.Path, "/")
	s.mcp.proxy.proxy.OpenSSE(w, r, target, run.KeyID, func(id string) string {
		return base + "/messages?session=" + id //i18n:ignore URL path
	})
}

func (s *Server) handleMCPProxyMessages(w http.ResponseWriter, r *http.Request) {
	target, run, ok := s.mcpProxyTarget(w, r)
	if !ok {
		return
	}
	if target.Transport != mcp.TransportSSE {
		writeErrKey(w, r, http.StatusNotFound, "api.mcpproxy.errors.unknown_server", "server", target.Name)
		return
	}
	s.mcp.proxy.proxy.PostSSE(w, r, target, run.KeyID, r.URL.Query().Get("session"))
}

// Identity comes only from the run key; never trust task, agent, stage or workspace from the query.
func (s *Server) mcpProxyTarget(w http.ResponseWriter, r *http.Request) (mcpproxy.Target, mcpproxy.Run, bool) {
	ctx := r.Context()
	scope := auth.APIKeyRunScope(ctx)
	if !scope.IsRun() {
		writeErrKey(w, r, http.StatusForbidden, "api.mcpproxy.errors.run_key_required")
		return mcpproxy.Target{}, mcpproxy.Run{}, false
	}
	if s.mcp.proxy.proxy == nil || s.mcp.fwd == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.mcpproxy.errors.unavailable")
		return mcpproxy.Target{}, mcpproxy.Run{}, false
	}
	name := r.PathValue("name")
	run := mcpproxy.Run{
		KeyID: auth.APIKeyID(ctx), TaskID: scope.TaskID, ChatID: scope.ChatID,
		Agent: scope.Agent, Stage: scope.Stage, WorkspaceID: auth.APIKeyWorkspace(ctx),
	}
	// Not granted and not registered answer alike, so a run cannot enumerate servers it was not
	// given.
	entry, grant, granted := s.mcpProxyGrant(ctx, scope, run.WorkspaceID, name)
	up, registered := s.mcp.fwd.Lookup(name)
	if !granted || !registered {
		writeErrKey(w, r, http.StatusForbidden, "api.mcpproxy.errors.server_not_granted", "server", name)
		return mcpproxy.Target{}, mcpproxy.Run{}, false
	}
	reg := s.mcpProxyRegistration(ctx, up)

	target := mcpproxy.Target{Name: name, URL: up.URL, Transport: up.Transport, Header: http.Header{}, Entry: entry}
	if reg.Auth == mcp.AuthCredential {
		h, renew, ok := s.mcpProxyCredentialHeaders(w, r, reg, &run)
		if !ok {
			return mcpproxy.Target{}, mcpproxy.Run{}, false
		}
		target.Header, target.Renew = h, renew
	} else {
		// Only first-party or in-deployment servers get the run query; third-party servers never
		// see task ids.
		q := mcp.RunQuery(scope.TaskID, grant.Locale, entry)
		if grant.OnRunner && name == browserrun.ServerName {
			q.Set(browserrun.HostLoopbackQuery, "1")
		}
		target.URL = mcp.WithQuery(up.URL, q)
	}
	if up.ForwardsCredential {
		target.Header.Set("X-API-Key", runKeyOf(r))
	}
	return target, run, true
}

// Falls back to the agent definition for keys minted before run grants were recorded, or after a
// failed write.
func (s *Server) mcpProxyGrant(ctx context.Context, scope auth.RunScope, workspaceID, server string) (mcp.ToolEntry, mcp.RunGrant, bool) {
	ref := scope.ChatID
	if ref == "" {
		ref = scope.TaskID
	}
	if s.mcp.registry != nil && ref != "" && scope.Agent != "" && scope.Stage != "" {
		g, found, err := s.mcp.registry.LookupRunGrant(ctx, ref, scope.Agent, scope.Stage)
		logx.Warn(err, "api.mcpproxy.log.read_grant_failed", "run", ref)
		if found {
			e, ok := mcp.FindToolEntry(g.Tools, server)
			return e, g, ok
		}
	}
	if s.taskDispatcher == nil || scope.Agent == "" {
		return mcp.ToolEntry{}, mcp.RunGrant{}, false
	}
	at := s.taskDispatcher.FindAgentTypeForRelaunch(workspaceID, scope.Agent)
	if at == nil {
		return mcp.ToolEntry{}, mcp.RunGrant{}, false
	}
	e, ok := mcp.FindToolEntry(strings.Join(at.MCPByStage[scope.Stage], ","), server)
	return e, mcp.RunGrant{Locale: i18n.FromContext(ctx)}, ok
}

func (s *Server) mcpProxyRegistration(ctx context.Context, up mcp.Upstream) mcp.Registration {
	fallback := mcp.Registration{Name: up.Name, URL: up.URL, Transport: up.Transport, Auth: mcp.AuthNone,
		ForwardsCredential: up.ForwardsCredential}
	if s.mcp.registry == nil {
		return fallback
	}
	reg, err := s.mcp.registry.Get(ctx, up.Name)
	if err != nil {
		if !errors.Is(err, mcp.ErrRegistrationNotFound) {
			logx.Warn(err, "api.mcpproxy.log.read_registration_failed", "server", up.Name)
		}
		return fallback
	}
	return reg
}

func (s *Server) mcpProxyCredentialHeaders(
	w http.ResponseWriter, r *http.Request, reg mcp.Registration, run *mcpproxy.Run,
) (h http.Header, renew mcpproxy.RenewFunc, ok bool) {
	if s.mcp.proxy.auth == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.mcpproxy.errors.credentials_unavailable", "server", reg.Name)
		return nil, nil, false
	}
	reach, ok := s.mcpProxyReach(w, r, run)
	if !ok {
		return nil, nil, false
	}
	run.Reach = reach
	h, err := s.mcp.proxy.auth.UpstreamHeaders(r.Context(), reg, *run)
	var refusal *mcpproxy.Refusal
	switch {
	case errors.As(err, &refusal):
		writeErrKey(w, r, http.StatusForbidden, "api.mcpproxy.errors.credential_refused",
			"server", reg.Name, "reason", string(refusal.Decision.Reason), "detail", refusal.Decision.Detail)
		return nil, nil, false
	case err != nil:
		writeError(w, r, http.StatusBadGateway, apperr.Wrap(err, "api.mcpproxy.errors.credential_failed", "server", reg.Name))
		return nil, nil, false
	}
	if renewer, isRenewer := s.mcp.proxy.auth.(mcpproxy.Renewer); isRenewer {
		// Renewal reuses the resolved run, reach included, so it is held to the same grant as the
		// first try.
		resolved := *run
		renew = func(ctx context.Context, rejected http.Header) (http.Header, error) {
			return renewer.RenewHeaders(ctx, reg, resolved, rejected)
		}
	}
	return h, renew, true
}

func (s *Server) mcpProxyReach(w http.ResponseWriter, r *http.Request, run *mcpproxy.Run) (credentials.Reach, bool) {
	if run.TaskID == "" {
		return credentials.Reach{WorkspaceID: run.WorkspaceID}, run.WorkspaceID != "" || writeRefusal(w, r)
	}
	t, err := s.taskStore.Get(r.Context(), run.TaskID)
	if err != nil || t == nil || !s.internalVisible(r, t.WorkspaceID) {
		return credentials.Reach{}, writeRefusal(w, r)
	}
	return s.reachForTask(r.Context(), t), true
}

func writeRefusal(w http.ResponseWriter, r *http.Request) bool {
	writeErrKey(w, r, http.StatusForbidden, "api.mcpproxy.errors.run_without_reach")
	return false
}

func runKeyOf(r *http.Request) string {
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok { //i18n:ignore HTTP auth scheme
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
