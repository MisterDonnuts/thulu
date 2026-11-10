package auth

import (
	"net/url"
	"os"
	"strings"

	oidclib "github.com/coreos/go-oidc/v3/oidc"
)

// No id_token_hint: Thulu keeps no ID tokens; the spec allows client_id instead.
// Off by default outside SaaS: a provider without the post-logout redirect registered exactly shows
// an error page.

func idpLogoutEnabled() bool {
	if v, ok := os.LookupEnv("OCTOPUS_OIDC_IDP_LOGOUT"); ok && strings.TrimSpace(v) != "" {
		return envOn("OCTOPUS_OIDC_IDP_LOGOUT")
	}
	return SaaSMode()
}

func discoverEndSession(p *oidclib.Provider) string {
	if p == nil {
		return ""
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(meta.EndSession))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

func (h *Handler) idpLogoutURL(frontend string) string {
	if !idpLogoutEnabled() {
		return ""
	}
	p := h.ssoProviders[ssoKindOIDC]
	if p == nil || p.endSessionURL == "" {
		return ""
	}
	u, err := url.Parse(p.endSessionURL)
	if err != nil {
		return ""
	}
	redirect := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_POST_LOGOUT_REDIRECT_URI"))
	if redirect == "" {
		redirect = frontend + "/login/"
	}
	q := u.Query()
	q.Set("client_id", p.oauth2Config.ClientID)
	q.Set("post_logout_redirect_uri", redirect)
	u.RawQuery = q.Encode()
	return u.String()
}
