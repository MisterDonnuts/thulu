package mcpoauth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Discovery struct {
	Resource         string         `json:"resource"`
	ResourceMetadata string         `json:"resource_metadata,omitempty"`
	Scope            string         `json:"scope,omitempty"`
	Server           ServerMetadata `json:"server"`
}

type ServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported,omitempty"`
}

type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

const maxMetadataBytes = 1 << 20

const probeInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
	`"params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"octopus","version":"1"}}}`

func (s *Service) Discover(ctx context.Context, serverURL, transport string) (*Discovery, error) {
	resource, err := CanonicalResource(serverURL)
	if err != nil {
		return nil, err
	}
	challenge, err := s.challenge(ctx, serverURL, transport)
	if err != nil {
		return nil, err
	}
	out := &Discovery{Resource: resource, Scope: challenge["scope"]}

	prm, prmURL, err := s.resourceMetadata(ctx, serverURL, challenge["resource_metadata"])
	if err != nil {
		return nil, err
	}
	issuer := origin(serverURL)
	if prm != nil {
		out.ResourceMetadata = prmURL
		if prm.Resource != "" {
			if !Covers(prm.Resource, serverURL) {
				return nil, apperr.Invalid("mcpoauth.errors.resource_mismatch", "resource", prm.Resource, "url", serverURL)
			}
			if out.Resource, err = CanonicalResource(prm.Resource); err != nil {
				return nil, err
			}
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, apperr.Invalid("mcpoauth.errors.no_authorization_server", "url", prmURL)
		}
		issuer = prm.AuthorizationServers[0]
		if out.Scope == "" {
			out.Scope = strings.Join(prm.ScopesSupported, " ")
		}
	} else if challenge == nil {
		return nil, apperr.Invalid("mcpoauth.errors.not_oauth_server", "url", serverURL)
	}

	meta, err := s.serverMetadata(ctx, serverURL, issuer)
	if err != nil {
		return nil, err
	}
	out.Server = *meta
	return out, nil
}

func (s *Service) challenge(ctx context.Context, serverURL, transport string) (map[string]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var req *http.Request
	var err error
	if transport == "sse" {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, serverURL, http.NoBody)
		if err == nil {
			req.Header.Set("Accept", "text/event-stream")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(probeInitialize))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream") //i18n:ignore HTTP media types
		}
	}
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", serverURL)
	}
	resp, err := s.server.Do(req)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", serverURL)
	}
	// Only the headers matter; an SSE stream would otherwise stay open.
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, nil
	}
	params := map[string]string{}
	for _, v := range resp.Header.Values("WWW-Authenticate") {
		for k, val := range bearerParams(v) {
			if _, seen := params[k]; !seen {
				params[k] = val
			}
		}
	}
	return params, nil
}

var challengeParam = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^\s,]+))`)

func bearerParams(header string) map[string]string {
	out := map[string]string{}
	rest := strings.TrimSpace(header)
	if i := strings.IndexByte(rest, ' '); i > 0 && !strings.Contains(rest[:i], "=") {
		if !strings.EqualFold(rest[:i], "bearer") {
			return out
		}
		rest = rest[i+1:]
	}
	for _, m := range challengeParam.FindAllStringSubmatch(rest, -1) {
		v := m[3]
		if m[2] != "" || m[3] == "" {
			v = strings.ReplaceAll(m[2], `\"`, `"`)
		}
		out[strings.ToLower(m[1])] = v
	}
	return out
}

// No document is not an error: older servers publish none.
func (s *Service) resourceMetadata(ctx context.Context, serverURL, hinted string) (*resourceMetadata, string, error) {
	candidates := []string{}
	if hinted != "" {
		candidates = append(candidates, hinted)
	} else {
		u, err := url.Parse(serverURL)
		if err != nil {
			return nil, "", apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", serverURL)
		}
		base := origin(serverURL) + "/.well-known/oauth-protected-resource" //i18n:ignore RFC 9728 well-known path
		if p := strings.TrimSuffix(u.EscapedPath(), "/"); p != "" {
			candidates = append(candidates, base+p)
		}
		candidates = append(candidates, base)
	}
	for _, c := range candidates {
		var prm resourceMetadata
		found, err := s.getJSON(ctx, s.clientFor(serverURL, c), c, &prm, false)
		if err != nil {
			return nil, "", err
		}
		if found {
			return &prm, c, nil
		}
	}
	if hinted != "" {
		return nil, "", apperr.Invalid("mcpoauth.errors.metadata_missing", "url", hinted)
	}
	return nil, "", nil
}

func (s *Service) serverMetadata(ctx context.Context, serverURL, issuer string) (*ServerMetadata, error) {
	if !s.endpointOK(issuer) {
		return nil, apperr.Invalid("mcpoauth.errors.insecure_endpoint", "url", issuer)
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", issuer)
	}
	o := origin(issuer)
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	//i18n:ignore RFC 8414 and OpenID well-known paths
	candidates := []string{o + "/.well-known/oauth-authorization-server" + p, o + "/.well-known/openid-configuration" + p}
	if p != "" {
		candidates = append(candidates, o+p+"/.well-known/openid-configuration") //i18n:ignore OpenID well-known path
	}
	for _, c := range candidates {
		var meta ServerMetadata
		found, err := s.getJSON(ctx, s.clientFor(serverURL, c), c, &meta, true)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if strings.TrimSuffix(meta.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
			return nil, apperr.Invalid("mcpoauth.errors.issuer_mismatch", "issuer", meta.Issuer, "url", issuer)
		}
		if err := s.checkServerMetadata(&meta); err != nil {
			return nil, err
		}
		return &meta, nil
	}
	return nil, apperr.Invalid("mcpoauth.errors.server_metadata_missing", "issuer", issuer)
}

// Servers listing no PKCE methods still get PKCE (some real ones omit the list); listing methods
// without S256 means it would be ignored.
func (s *Service) checkServerMetadata(m *ServerMetadata) error {
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return apperr.Invalid("mcpoauth.errors.endpoints_missing", "issuer", m.Issuer)
	}
	for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.RegistrationEndpoint, m.RevocationEndpoint} {
		if e != "" && !s.endpointOK(e) {
			return apperr.Invalid("mcpoauth.errors.insecure_endpoint", "url", e)
		}
	}
	if len(m.CodeChallengeMethodsSupported) > 0 && !contains(m.CodeChallengeMethodsSupported, "S256") {
		return apperr.Invalid("mcpoauth.errors.no_pkce", "issuer", m.Issuer)
	}
	return nil
}

// secure applies only to authorization server metadata: it decides where codes and secrets go.
func (s *Service) getJSON(ctx context.Context, client *http.Client, raw string, out any, secure bool) (bool, error) {
	if secure && !s.endpointOK(raw) {
		return false, apperr.Invalid("mcpoauth.errors.insecure_endpoint", "url", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, http.NoBody)
	if err != nil {
		return false, apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", raw)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", raw)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed,
		resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return false, nil
	case resp.StatusCode != http.StatusOK:
		return false, apperr.Unavailable("mcpoauth.errors.metadata_status", "url", raw, "status", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(out); err != nil {
		return false, apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.metadata_decode", "url", raw)
	}
	return true, nil
}

func CanonicalResource(raw string) (string, error) {
	u, err := canonicalURL(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func canonicalURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, apperr.Invalid("mcpoauth.errors.bad_url", "url", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	u.User = nil
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = strings.TrimSuffix(u.RawPath, "/")
	return u, nil
}

func Covers(resource, serverURL string) bool {
	ru, err1 := canonicalURL(resource)
	su, err2 := canonicalURL(serverURL)
	if err1 != nil || err2 != nil {
		return false
	}
	if ru.Scheme != su.Scheme || ru.Host != su.Host {
		return false
	}
	return ru.Path == "" || su.Path == ru.Path || strings.HasPrefix(su.Path, ru.Path+"/")
}

func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// MCP authorization spec: endpoints receiving codes, secrets or tokens must be https; plain http
// only on loopback.
func secureEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
