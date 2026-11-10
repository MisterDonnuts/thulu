package api

// The callback is public and authenticated only by state plus the binder cookie of the browser that
// started the flow,
// so a copied link cannot land one account's tokens in another workspace.

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
)

const (
	mcpOAuthCallbackPath = "/credentials/oauth/callback"
	mcpOAuthClientPath   = "/credentials/oauth/client"
	mcpOAuthBinder       = "octopus_mcp_oauth"
	mcpOAuthBinderPath   = "/credentials/oauth/"
)

var mcpOAuthReturns = map[string]string{"credentials": "/credentials/", "mcps": "/mcps/"}

func (s *Server) SetMCPOAuth(svc *mcpoauth.Service) {
	s.mcpOAuth = svc
	s.rewireMCPProxyAuth()
}

func (s *Server) requireMCPOAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireCredentials(w, r) {
		return false
	}
	if s.mcpOAuth == nil || s.mcp.registry == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.credentials.errors.oauth_unavailable")
		return false
	}
	return true
}

// A hand-registered client (Slack) must list exactly this URL.
func mcpOAuthRedirectURI(r *http.Request) string {
	return auth.RequestOrigin(r) + mcpOAuthCallbackPath
}

// Authorization servers fetch this from the internet, so it exists only on https.
func mcpOAuthClientURL(r *http.Request) string {
	origin := auth.RequestOrigin(r)
	if !strings.HasPrefix(origin, "https://") {
		return ""
	}
	return origin + mcpOAuthClientPath
}

type oauthServerView struct {
	Name          string `json:"name"`
	Transport     string `json:"transport"`
	CredentialRef string `json:"credential_ref"`
	// URL only for the operator, like /mcps.
	URL        string                  `json:"url,omitempty"`
	Credential *credentials.Credential `json:"credential,omitempty"`
}

func (s *Server) handleListOAuthServers(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPOAuth(w, r) {
		return
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	regs, err := s.mcp.registry.List(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	creds, err := s.credStore.List(r.Context(), wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	byName := map[string]*credentials.Credential{}
	for _, c := range creds {
		byName[c.Name] = c
	}
	operator := s.callerIsOperator(r)
	out := []oauthServerView{}
	for i := range regs {
		reg := &regs[i]
		if reg.Auth != mcp.AuthCredential {
			continue
		}
		v := oauthServerView{Name: reg.Name, Transport: reg.Transport, CredentialRef: reg.CredentialRef,
			Credential: byName[reg.CredentialRef]}
		if operator {
			v.URL = reg.URL
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"servers":             out,
		"redirect_uri":        mcpOAuthRedirectURI(r),
		"client_metadata_url": mcpOAuthClientURL(r),
	})
}

type oauthStartReq struct {
	Server       string              `json:"server"`
	CredentialID string              `json:"credential_id"`
	ClientID     string              `json:"client_id"`
	ClientSecret string              `json:"client_secret"`
	Scope        string              `json:"scope"`
	Grants       []credentials.Grant `json:"grants"`
	Return       string              `json:"return"`
}

func (s *Server) handleStartOAuth(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPOAuth(w, r) {
		return
	}
	var req oauthStartReq
	if !decodeBody(w, r, &req) {
		return
	}
	for _, g := range req.Grants {
		if err := g.Valid(); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	if _, ok := mcpOAuthReturns[req.Return]; !ok {
		req.Return = "credentials"
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	server := strings.TrimSpace(req.Server)
	if server == "" && req.CredentialID != "" {
		server = s.oauthConnectionServer(r, wid, req.CredentialID)
	}
	reg, err := s.mcp.registry.Get(r.Context(), server)
	if err != nil {
		if errors.Is(err, mcp.ErrRegistrationNotFound) {
			writeErrKey(w, r, http.StatusNotFound, "api.credentials.errors.oauth_server_not_found", "server", server)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if reg.Auth != mcp.AuthCredential || reg.CredentialRef == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.credentials.errors.oauth_server_needs_credential", "server", reg.Name)
		return
	}
	started, err := s.mcpOAuth.Start(r.Context(), mcpoauth.StartRequest{
		WorkspaceID: wid, Author: authorLabel(r), CredentialID: req.CredentialID, Name: reg.CredentialRef,
		Server: reg.Name, ServerURL: reg.URL, Transport: reg.Transport, Scope: req.Scope,
		Grants: req.Grants, Return: req.Return,
		Client: mcpoauth.ClientRequest{
			ID: req.ClientID, Secret: req.ClientSecret,
			MetadataURL: mcpOAuthClientURL(r), RedirectURI: mcpOAuthRedirectURI(r),
		},
	})
	if err != nil {
		writeCredentialError(w, r, err)
		return
	}
	auth.SetFlowBindCookie(w, r, mcpOAuthBinder, mcpOAuthBinderPath, started.Binder, int(mcpoauth.FlowTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{
		"authorize_url": started.AuthorizeURL,
		"registration":  started.Registration,
		"redirect_uri":  mcpOAuthRedirectURI(r),
	})
}

func (s *Server) oauthConnectionServer(r *http.Request, wid, id string) string {
	c, err := s.credStore.Get(r.Context(), wid, id)
	if err != nil || !credentials.IsOAuth(c.Provider) {
		return ""
	}
	p, err := s.credStore.Payload(r.Context(), wid, id)
	if err != nil {
		return ""
	}
	return credentials.ParseOAuthConnection(p).Server
}

// Only RFC 6749-shaped error codes are echoed into the redirect.
var oauthErrorWord = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	back := func(ret string, q url.Values) {
		page, ok := mcpOAuthReturns[ret]
		if !ok {
			page = mcpOAuthReturns["credentials"]
		}
		http.Redirect(w, r, page+"?"+q.Encode(), http.StatusFound)
	}
	failed := func(ret, reason, detail string) {
		q := url.Values{"mcp_oauth": {"error"}, "reason": {reason}}
		if oauthErrorWord.MatchString(detail) {
			q.Set("detail", detail)
		}
		back(ret, q)
	}
	if s.mcpOAuth == nil || s.credStore == nil {
		failed("", "unavailable", "")
		return
	}
	q := r.URL.Query()
	binder := ""
	if c, err := r.Cookie(mcpOAuthBinder); err == nil {
		binder = c.Value
	}
	auth.ClearFlowBindCookie(w, r, mcpOAuthBinder, mcpOAuthBinderPath)
	fin, err := s.mcpOAuth.Finish(r.Context(), q.Get("state"), binder, q.Get("code"), q.Get("error"), q.Get("error_description"))
	switch {
	case errors.Is(err, mcpoauth.ErrState):
		failed(fin.Return, "state", "")
		return
	case q.Get("error") != "" && fin.WorkspaceID != "":
		failed(fin.Return, "denied", q.Get("error"))
		return
	case err != nil:
		logx.Warn(err, "api.credentials.log.oauth_finish_failed", "server", logx.Safe(fin.Server))
		var te *mcpoauth.TokenError
		if errors.As(err, &te) {
			failed(fin.Return, "token", te.Code)
			return
		}
		failed(fin.Return, "failed", "")
		return
	}
	if s.auditStore != nil {
		kind, key := audit.KindCredentialUpdated, "api.credentials.audit.oauth_reconnected"
		if fin.Created {
			kind, key = audit.KindCredentialCreated, "api.credentials.audit.oauth_connected"
		}
		s.auditStore.Log(audit.Entry{
			Kind: kind, WorkspaceID: fin.WorkspaceID, ActorType: auth.ActorUser, ActorLabel: fin.Author,
			ResourceID: fin.CredentialID, ResourceType: "credential",
			Summary:  i18n.Text(key, "name", fin.Name, "server", fin.Server),
			Metadata: map[string]string{"provider": credentials.OAuthProviderID, "name": fin.Name, "server": fin.Server},
		})
	}
	back(fin.Return, url.Values{"mcp_oauth": {"connected"}, "credential": {fin.CredentialID}})
}

func (s *Server) handleDisconnectOAuth(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPOAuth(w, r) {
		return
	}
	c, ok := s.credentialForCaller(w, r)
	if !ok {
		return
	}
	if !credentials.IsOAuth(c.Provider) {
		writeError(w, r, http.StatusBadRequest, mcpoauth.ErrNotOAuth)
		return
	}
	if err := s.mcpOAuth.Disconnect(r.Context(), c.WorkspaceID, c.ID); err != nil {
		writeCredentialError(w, r, err)
		return
	}
	s.auditCredential(r, audit.KindCredentialUpdated, c, i18n.Text("api.credentials.audit.oauth_disconnected", "name", c.Name))
	s.reloadAndRespond(w, r, c.WorkspaceID, c.ID)
}

func (s *Server) handleOAuthClientMetadata(w http.ResponseWriter, r *http.Request) {
	id := mcpOAuthClientURL(r)
	if id == "" {
		writeError(w, r, http.StatusNotFound, apperr.NotFound("api.credentials.errors.oauth_client_https_only"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":                  id,
		"client_name":                "Thulu", //i18n:ignore product name shown on consent screens
		"client_uri":                 auth.RequestOrigin(r),
		"redirect_uris":              []string{mcpOAuthRedirectURI(r)},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}
