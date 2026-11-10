package mcpproxy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
)

// Resolved from the run's API key only; never fill from the request's query or body.
type Run struct {
	KeyID       string
	TaskID      string
	ChatID      string
	Agent       string
	Stage       string
	WorkspaceID string
	Reach       credentials.Reach
}

// Called on every forwarded request so revoked grants and disabled credentials apply on the next
// call; do not cache.
// A *Refusal becomes 403, any other error 502.
type UpstreamAuth interface {
	UpstreamHeaders(ctx context.Context, reg mcp.Registration, run Run) (http.Header, error)
}

type Refusal struct {
	Decision credentials.Decision
}

func (r *Refusal) Error() string { return string(r.Decision.Reason) + ": " + r.Decision.Detail }

type ServerAuthorizer interface {
	AuthorizeServer(ctx context.Context, req credentials.ServerRequest) (credentials.Decision, error)
}

type PayloadReader interface {
	Get(ctx context.Context, workspaceID, id string) (*credentials.Credential, error)
	Payload(ctx context.Context, workspaceID, id string) (map[string]string, error)
}

type Renewer interface {
	RenewHeaders(ctx context.Context, reg mcp.Registration, run Run, rejected http.Header) (http.Header, error)
}

var ErrNotRenewable error = apperr.New("mcpproxy.errors.not_renewable")

type OAuthTokens interface {
	AccessToken(ctx context.Context, workspaceID, credentialID, serverURL string) (string, error)
	Renew(ctx context.Context, workspaceID, credentialID, serverURL, rejected string) (string, error)
}

type StaticHeaderAuth struct {
	Authorizer ServerAuthorizer
	Store      PayloadReader
	Tokens     OAuthTokens
	OnRefusal  func(ctx context.Context, reg mcp.Registration, run Run, d credentials.Decision)
}

func (a *StaticHeaderAuth) authorize(ctx context.Context, reg mcp.Registration, run Run) (*credentials.Credential, error) {
	d, err := a.Authorizer.AuthorizeServer(ctx, credentials.ServerRequest{
		Reach: run.Reach, TaskID: run.TaskID, Agent: run.Agent, Stage: run.Stage,
		Server: reg.Name, CredentialRef: reg.CredentialRef,
	})
	if err != nil {
		return nil, err
	}
	if !d.Allowed {
		return nil, a.refuse(ctx, reg, run, d)
	}
	return a.Store.Get(ctx, run.Reach.WorkspaceID, d.CredentialID)
}

func (a *StaticHeaderAuth) refuse(ctx context.Context, reg mcp.Registration, run Run, d credentials.Decision) error {
	if a.OnRefusal != nil {
		a.OnRefusal(ctx, reg, run, d)
	}
	return &Refusal{Decision: d}
}

func (a *StaticHeaderAuth) UpstreamHeaders(ctx context.Context, reg mcp.Registration, run Run) (http.Header, error) {
	cred, err := a.authorize(ctx, reg, run)
	if err != nil {
		return nil, err
	}
	if credentials.IsOAuth(cred.Provider) {
		return a.bearer(ctx, reg, run, cred, "")
	}
	payload, err := a.Store.Payload(ctx, run.Reach.WorkspaceID, cred.ID)
	if err != nil {
		return nil, err
	}
	h, err := credentials.HeadersFor(cred.Provider, cred.Kind, reg.HeaderTemplate, payload)
	if err != nil {
		return nil, &Refusal{Decision: credentials.Decision{
			Reason: credentials.ReasonHeaderMismatch, CredentialID: cred.ID, Provider: cred.Provider,
			Detail: apperr.Localize(err, i18n.FromContext(ctx)),
		}}
	}
	return h, nil
}

func (a *StaticHeaderAuth) RenewHeaders(ctx context.Context, reg mcp.Registration, run Run, rejected http.Header) (http.Header, error) {
	cred, err := a.authorize(ctx, reg, run)
	if err != nil {
		return nil, err
	}
	if !credentials.IsOAuth(cred.Provider) {
		return nil, ErrNotRenewable
	}
	stale, _ := strings.CutPrefix(rejected.Get("Authorization"), bearerPrefix)
	return a.bearer(ctx, reg, run, cred, stale)
}

const bearerPrefix = "Bearer " //i18n:ignore HTTP auth scheme

func (a *StaticHeaderAuth) bearer(
	ctx context.Context, reg mcp.Registration, run Run, cred *credentials.Credential, stale string,
) (http.Header, error) {
	if a.Tokens == nil {
		return nil, apperr.Unavailable("mcpproxy.errors.oauth_unavailable", "server", reg.Name)
	}
	var tok string
	var err error
	if stale == "" {
		tok, err = a.Tokens.AccessToken(ctx, run.Reach.WorkspaceID, cred.ID, reg.URL)
	} else {
		tok, err = a.Tokens.Renew(ctx, run.Reach.WorkspaceID, cred.ID, reg.URL, stale)
	}
	switch {
	case errors.Is(err, mcpoauth.ErrReconnect):
		return nil, a.refuse(ctx, reg, run, credentials.Decision{
			Reason: credentials.ReasonInvalid, CredentialID: cred.ID, Provider: cred.Provider,
			Detail: i18n.TCtx(ctx, "credentials.decisions.invalid", "credential", cred.Name, "provider", cred.Provider),
		})
	case errors.Is(err, mcpoauth.ErrWrongResource):
		return nil, a.refuse(ctx, reg, run, credentials.Decision{
			Reason: credentials.ReasonHeaderMismatch, CredentialID: cred.ID, Provider: cred.Provider,
			Detail: apperr.Localize(err, i18n.FromContext(ctx)),
		})
	case err != nil:
		return nil, err
	}
	h := http.Header{}
	h.Set("Authorization", bearerPrefix+tok)
	return h, nil
}
