package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/cloudsign"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type gcpProvider struct{}

// Pinned instead of read from the key file's token_uri: a stored credential must not be able to
// redirect the daemon's outbound call (SSRF).
// Vars only so tests can redirect them.
var (
	gcpTokenEndpoint = "https://oauth2.googleapis.com/token" //nolint:gosec // G101: a public OAuth endpoint, not a credential
	gcpCRMEndpoint   = "https://cloudresourcemanager.googleapis.com/v1/projects/"
)

// Google's documented cap for one testIamPermissions call; fail here rather than with an opaque
// 400.
const gcpMaxPermissions = 100

func (p *gcpProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *gcpProvider) SpecIn(ctx context.Context) ProviderSpec {
	return ProviderSpec{
		ID:    "gcp",
		Label: i18n.TCtx(ctx, "credentials.labels.provider_gcp"),
		Help:  i18n.TCtx(ctx, "credentials.hints.provider_gcp"),
		Kinds: []KindSpec{{
			ID:    "gcp_sa",
			Label: i18n.TCtx(ctx, "credentials.labels.kind_gcp_sa"),
			Fields: []FieldSpec{
				{
					// Describe the file instead of showing a JSON skeleton: a literal
					// service-account shape in source trips secret scanners.
					Name:        "service_account_json",
					Label:       i18n.TCtx(ctx, "credentials.labels.field_service_account_json"),
					Secret:      true,
					Multiline:   true,
					Placeholder: i18n.TCtx(ctx, "credentials.hints.service_account_json_placeholder"),
				},
				{
					Name: "project_id", Label: i18n.TCtx(ctx, "credentials.labels.field_project_id"), Optional: true,
					Help: i18n.TCtx(ctx, "credentials.hints.project_id"),
				},
			},
		}},
	}
}

type gcpServiceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func (p *gcpProvider) Introspect(ctx context.Context, _ string, payload map[string]string, want []string) (Introspection, error) {
	raw := strings.TrimSpace(payload["service_account_json"])
	if raw == "" {
		return Introspection{}, apperr.Invalid("credentials.errors.service_account_json_required")
	}
	var sa gcpServiceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return Introspection{State: StateInvalid, Err: i18n.Text("credentials.notes.invalid_key_file", "err", err)}, nil
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return Introspection{
			State: StateInvalid,
			Err:   i18n.Text("credentials.notes.key_file_incomplete"),
		}, nil
	}

	// client_email is the only field of the key file that is safe to show.
	out := Introspection{Hint: sa.ClientEmail, Identity: sa.ClientEmail}

	token, err := gcpAccessToken(ctx, sa)
	if err != nil {
		var apiErr *gcpAPIError
		if errors.As(err, &apiErr) {
			out.State = StateInvalid
			out.Err = apiErr.Error()
			return out, nil
		}
		return Introspection{}, err
	}
	out.State = StateValid
	// No ExpiresAt: the service-account key does not expire, only the 1h tokens minted from it.

	project := strings.TrimSpace(payload["project_id"])
	if project == "" {
		project = sa.ProjectID
	}
	perms := wantForProvider("gcp", want)
	if len(perms) == 0 {
		return out, nil
	}
	if project == "" {
		out.Err = i18n.Text("credentials.notes.no_project_id")
		return out, nil
	}
	granted, err := gcpTestPermissions(ctx, token, project, perms)
	if err != nil {
		// Leave capabilities undiscovered: failing the check does not mean the account can do
		// nothing.
		out.Err = i18n.Text("credentials.notes.capabilities_not_discovered", "reason", apperr.Text(err))
		return out, nil
	}
	out.Capabilities = namespaced("gcp", granted)
	return out, nil
}

func gcpAccessToken(ctx context.Context, sa gcpServiceAccount) (string, error) {
	assertion, err := cloudsign.GCPAssertion(sa.ClientEmail, sa.PrivateKey, time.Now())
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, //i18n:ignore OAuth grant type URN
		"assertion":  {assertion},
	}
	endpoint, err := gcpTokenURL()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", apperr.Wrap(err, "credentials.errors.read_response", "service", "token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", parseGCPError(body, resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", apperr.Wrap(err, "credentials.errors.decode_response", "service", "token")
	}
	if tok.AccessToken == "" {
		return "", apperr.Unavailable("credentials.errors.google_no_access_token")
	}
	return tok.AccessToken, nil
}

func gcpTestPermissions(ctx context.Context, token, project string, perms []string) ([]string, error) {
	if len(perms) > gcpMaxPermissions {
		return nil, apperr.Internal("credentials.errors.too_many_permissions", "count", len(perms), "max", gcpMaxPermissions)
	}
	payload, err := json.Marshal(map[string]any{"permissions": perms})
	if err != nil {
		return nil, err
	}
	base, err := gcpCRMURL()
	if err != nil {
		return nil, err
	}
	endpoint := base + url.PathEscape(project) + ":testIamPermissions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token) //i18n:ignore HTTP auth scheme
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, apperr.Wrap(err, "credentials.errors.read_response", "service", "testIamPermissions")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseGCPError(body, resp.StatusCode)
	}
	var out struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, apperr.Wrap(err, "credentials.errors.decode_response", "service", "testIamPermissions")
	}
	return out.Permissions, nil
}

type gcpAPIError struct {
	Message string
	Status  int
}

func (e *gcpAPIError) Error() string {
	if e.Message == "" {
		return i18n.Log("credentials.errors.google_http_status", "status", e.Status)
	}
	return e.Message
}

func parseGCPError(body []byte, status int) error {
	var oauthErr struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &oauthErr); err == nil && oauthErr.Error != "" {
		msg := oauthErr.Error
		if oauthErr.Description != "" {
			msg += ": " + oauthErr.Description
		}
		return &gcpAPIError{Message: msg, Status: status}
	}
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return &gcpAPIError{Message: apiErr.Error.Message, Status: status}
	}
	return &gcpAPIError{Status: status}
}
