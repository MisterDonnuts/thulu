package mcpoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
)

const (
	RegistrationManual = "manual"
	RegistrationDCR    = "dcr"
	RegistrationCIMD   = "cimd"
)

const (
	authNone  = "none"
	authBasic = "client_secret_basic"
	authPost  = "client_secret_post"
)

const clientName = "Thulu" //i18n:ignore product name shown on consent screens

type Client struct {
	Registration string
	ID           string
	Secret       string
	Auth         string
}

type ClientRequest struct {
	ID, Secret  string
	MetadataURL string
	RedirectURI string
}

func (s *Service) RegisterClient(ctx context.Context, serverURL string, meta ServerMetadata, req ClientRequest) (Client, error) {
	if id := strings.TrimSpace(req.ID); id != "" {
		c := Client{Registration: RegistrationManual, ID: id, Secret: strings.TrimSpace(req.Secret), Auth: authNone}
		if c.Secret != "" {
			c.Auth = secretAuth(meta.TokenEndpointAuthMethodsSupported)
		}
		return c, nil
	}
	if meta.RegistrationEndpoint != "" {
		return s.registerDynamic(ctx, serverURL, meta, req.RedirectURI)
	}
	if meta.ClientIDMetadataDocumentSupported && req.MetadataURL != "" {
		return Client{Registration: RegistrationCIMD, ID: req.MetadataURL, Auth: authNone}, nil
	}
	return Client{}, apperr.Invalid("mcpoauth.errors.client_required", "issuer", meta.Issuer)
}

func secretAuth(supported []string) string {
	if len(supported) == 0 || contains(supported, authBasic) {
		return authBasic
	}
	if contains(supported, authPost) {
		return authPost
	}
	return authBasic
}

func (s *Service) registerDynamic(ctx context.Context, serverURL string, meta ServerMetadata, redirectURI string) (Client, error) {
	body, err := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": authNone,
	})
	if err != nil {
		return Client{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return Client{}, apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", meta.RegistrationEndpoint)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.clientFor(serverURL, meta.RegistrationEndpoint).Do(req)
	if err != nil {
		return Client{}, apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", meta.RegistrationEndpoint)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return Client{}, apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", meta.RegistrationEndpoint)
	}
	if resp.StatusCode/100 != 2 {
		return Client{}, apperr.Invalid("mcpoauth.errors.registration_refused",
			"status", resp.StatusCode, "detail", oauthErrorDetail(raw))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthMethod   string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" {
		return Client{}, apperr.Invalid("mcpoauth.errors.registration_refused",
			"status", resp.StatusCode, "detail", oauthErrorDetail(raw))
	}
	c := Client{Registration: RegistrationDCR, ID: out.ClientID, Secret: out.ClientSecret, Auth: out.AuthMethod}
	switch {
	case c.Secret == "":
		c.Auth = authNone
	case c.Auth == "" || c.Auth == authNone:
		c.Auth = secretAuth(meta.TokenEndpointAuthMethodsSupported)
	}
	return c, nil
}

// Slack answers errors with 200 and ok:false, and oauth.v2.access puts the user token under
// authed_user.
type tokenResponse struct {
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token"`
	ExpiresIn        seconds `json:"expires_in"`
	Scope            string  `json:"scope"`
	Error            string  `json:"error"`
	ErrorDescription string  `json:"error_description"`
	OK               *bool   `json:"ok"`
	AuthedUser       *struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    seconds `json:"expires_in"`
		Scope        string  `json:"scope"`
	} `json:"authed_user"`
}

// Some servers send expires_in as a numeric string.
type seconds int64

func (s *seconds) UnmarshalJSON(b []byte) error {
	str := strings.Trim(string(b), `"`)
	if str == "" || str == "null" {
		*s = 0
		return nil
	}
	n, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_expires_in", "value", str)
	}
	*s = seconds(n)
	return nil
}

type TokenError struct {
	Status    int
	Code      string
	Detail    string
	Permanent bool
}

func (e *TokenError) Error() string { return e.apperr().Error() }

func (e *TokenError) apperr() *apperr.Error {
	return apperr.Invalid("mcpoauth.errors.token_refused", "status", e.Status, "code", e.Code, "detail", e.Detail)
}

var permanentCodes = map[string]bool{
	"invalid_grant": true, "invalid_client": true, "unauthorized_client": true,
	"unsupported_grant_type": true, "invalid_scope": true,
	// Slack's equivalents.
	"invalid_refresh_token": true, "invalid_code": true, "token_revoked": true, "token_expired": true,
}

func (s *Service) requestToken(ctx context.Context, conn *credentials.OAuthConnection, form url.Values) error {
	form.Set("resource", conn.Resource)
	if conn.ClientAuth != authBasic {
		form.Set("client_id", conn.ClientID)
	}
	if conn.ClientAuth == authPost {
		form.Set("client_secret", conn.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "mcpoauth.errors.bad_url", "url", conn.TokenEndpoint)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if conn.ClientAuth == authBasic {
		req.SetBasicAuth(url.QueryEscape(conn.ClientID), url.QueryEscape(conn.ClientSecret))
	}
	resp, err := s.clientFor(conn.Resource, conn.TokenEndpoint).Do(req)
	if err != nil {
		return apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", conn.TokenEndpoint)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return apperr.WrapKind(apperr.KindUnavailable, err, "mcpoauth.errors.unreachable", "url", conn.TokenEndpoint)
	}
	tr, te := parseTokenResponse(resp.StatusCode, raw)
	if te != nil {
		return te
	}
	s.applyTokens(conn, tr)
	return nil
}

func parseTokenResponse(status int, raw []byte) (tokenResponse, *TokenError) {
	var tr tokenResponse
	decodeErr := json.Unmarshal(raw, &tr)
	if tr.AccessToken == "" && tr.AuthedUser != nil {
		tr.AccessToken, tr.RefreshToken = tr.AuthedUser.AccessToken, tr.AuthedUser.RefreshToken
		tr.ExpiresIn, tr.Scope = tr.AuthedUser.ExpiresIn, tr.AuthedUser.Scope
	}
	refused := status/100 != 2 || decodeErr != nil || tr.Error != "" ||
		(tr.OK != nil && !*tr.OK) || tr.AccessToken == ""
	if !refused {
		return tr, nil
	}
	te := &TokenError{Status: status, Code: tr.Error, Detail: tr.ErrorDescription}
	if te.Detail == "" && decodeErr != nil {
		te.Detail = oauthErrorDetail(raw)
	}
	te.Permanent = permanentCodes[tr.Error] ||
		(status == http.StatusBadRequest || status == http.StatusUnauthorized) && tr.Error != ""
	return tr, te
}

func (s *Service) applyTokens(conn *credentials.OAuthConnection, tr tokenResponse) {
	conn.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		conn.RefreshToken = tr.RefreshToken
	}
	if tr.Scope != "" {
		conn.Scope = tr.Scope
	}
	conn.ExpiresAt = time.Time{}
	if tr.ExpiresIn > 0 {
		conn.ExpiresAt = s.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	conn.Status, conn.Error = credentials.OAuthConnected, ""
}

func (s *Service) revoke(ctx context.Context, conn credentials.OAuthConnection, token, hint string) {
	if conn.RevocationEndpoint == "" || token == "" || !s.endpointOK(conn.RevocationEndpoint) {
		return
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}}
	if conn.ClientAuth != authBasic {
		form.Set("client_id", conn.ClientID)
	}
	if conn.ClientAuth == authPost {
		form.Set("client_secret", conn.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if conn.ClientAuth == authBasic {
		req.SetBasicAuth(url.QueryEscape(conn.ClientID), url.QueryEscape(conn.ClientSecret))
	}
	if resp, err := s.clientFor(conn.Resource, conn.RevocationEndpoint).Do(req); err == nil {
		resp.Body.Close()
	}
}

func oauthErrorDetail(raw []byte) string {
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Description != "" {
			return e.Description
		}
		if e.Error != "" {
			return e.Error
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
