package credentials

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type oauthProvider struct{}

const OAuthProviderID = "oauth-connection"

const OAuthKindMCP = "mcp"

const (
	OAuthConnected    = "connected"
	OAuthFailed       = "failed"
	OAuthDisconnected = "disconnected"
)

const (
	OAuthReconnectRequired = "oauth_reconnect_required"
	OAuthNotConnected      = "oauth_disconnected"
)

type OAuthConnection struct {
	// Resource is the RFC 8707 audience the tokens were issued for; the proxy must send the token
	// only to this URL.
	Server   string
	Resource string

	Issuer             string
	TokenEndpoint      string
	RevocationEndpoint string

	Registration string
	ClientID     string
	ClientSecret string
	ClientAuth   string

	Scope        string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time

	Status      string
	Error       string
	ConnectedBy string
	ConnectedAt time.Time
}

func (c OAuthConnection) Payload() map[string]string {
	out := map[string]string{
		"server": c.Server, "resource": c.Resource, "issuer": c.Issuer,
		"token_endpoint": c.TokenEndpoint, "revocation_endpoint": c.RevocationEndpoint,
		"registration": c.Registration, "client_id": c.ClientID, "client_secret": c.ClientSecret,
		"client_auth": c.ClientAuth, "scope": c.Scope, "access_token": c.AccessToken,
		"refresh_token": c.RefreshToken, "status": c.Status, "error": c.Error,
		"connected_by": c.ConnectedBy,
	}
	if !c.ExpiresAt.IsZero() {
		out["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if !c.ConnectedAt.IsZero() {
		out["connected_at"] = c.ConnectedAt.UTC().Format(time.RFC3339)
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	return out
}

func ParseOAuthConnection(p map[string]string) OAuthConnection {
	when := func(k string) time.Time {
		t, err := time.Parse(time.RFC3339, p[k])
		if err != nil {
			return time.Time{}
		}
		return t
	}
	return OAuthConnection{
		Server: p["server"], Resource: p["resource"], Issuer: p["issuer"],
		TokenEndpoint: p["token_endpoint"], RevocationEndpoint: p["revocation_endpoint"],
		Registration: p["registration"], ClientID: p["client_id"], ClientSecret: p["client_secret"],
		ClientAuth: p["client_auth"], Scope: p["scope"], AccessToken: p["access_token"],
		RefreshToken: p["refresh_token"], ExpiresAt: when("expires_at"), Status: p["status"],
		Error: p["error"], ConnectedBy: p["connected_by"], ConnectedAt: when("connected_at"),
	}
}

func (c OAuthConnection) Introspection() Introspection {
	out := Introspection{Identity: issuerLabel(c.Issuer), Hint: c.Scope, State: StateValid}
	switch {
	case c.Status == OAuthDisconnected:
		out.State, out.Err = StateInvalid, OAuthNotConnected
	case c.Status != OAuthConnected || c.AccessToken == "":
		out.State, out.Err = StateInvalid, OAuthReconnectRequired
	}
	return out
}

func issuerLabel(issuer string) string {
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Host
	}
	return issuer
}

func (p *oauthProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *oauthProvider) SpecIn(ctx context.Context) ProviderSpec {
	return ProviderSpec{
		ID:      OAuthProviderID,
		Label:   i18n.TCtx(ctx, "credentials.labels.provider_oauth"),
		Help:    i18n.TCtx(ctx, "credentials.hints.provider_oauth"),
		Connect: true,
		Kinds: []KindSpec{{
			ID:    OAuthKindMCP,
			Label: i18n.TCtx(ctx, "credentials.labels.kind_oauth_mcp"),
			Help:  i18n.TCtx(ctx, "credentials.hints.kind_oauth_mcp"),
			Fields: []FieldSpec{
				{
					Name: "client_id", Label: i18n.TCtx(ctx, "credentials.labels.field_client_id"),
					Help: i18n.TCtx(ctx, "credentials.hints.field_client_id"), Optional: true,
				},
				{
					Name: "client_secret", Label: i18n.TCtx(ctx, "credentials.labels.field_client_secret"),
					Secret: true, Optional: true,
				},
				{
					Name: "scope", Label: i18n.TCtx(ctx, "credentials.labels.field_scope"),
					Help: i18n.TCtx(ctx, "credentials.hints.field_scope"), Optional: true,
				},
			},
		}},
	}
}

func (p *oauthProvider) Introspect(_ context.Context, kind string, payload map[string]string, _ []string) (Introspection, error) {
	if kind != OAuthKindMCP {
		return Introspection{}, apperr.Invalid("credentials.errors.unknown_kind", "kind", kind, "provider", OAuthProviderID)
	}
	return ParseOAuthConnection(payload).Introspection(), nil
}

// OAuth connection payloads must never leave the daemon; the internal resolve route refuses this
// provider.
func IsOAuth(provider string) bool { return strings.TrimSpace(provider) == OAuthProviderID }
