package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

type githubProvider struct{}

var githubAPI = ""

func githubAPIBase() string {
	if githubAPI != "" {
		return githubAPI
	}
	return githubcfg.APIURL()
}

func (p *githubProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *githubProvider) SpecIn(ctx context.Context) ProviderSpec {
	return ProviderSpec{
		ID:    "github",
		Label: i18n.TCtx(ctx, "credentials.labels.provider_github"),
		Help:  i18n.TCtx(ctx, "credentials.hints.provider_github"),
		Kinds: []KindSpec{{
			ID:    "github_pat",
			Label: i18n.TCtx(ctx, "credentials.labels.kind_github_pat"),
			Fields: []FieldSpec{{
				Name:        "token",
				Label:       i18n.TCtx(ctx, "credentials.labels.field_token"),
				Secret:      true,
				Placeholder: i18n.TCtx(ctx, "credentials.hints.token_placeholder"),
				Help:        i18n.TCtx(ctx, "credentials.hints.token"),
			}},
		}},
	}
}

var githubScopeGrants = map[string][]string{
	"repo":        {"repo:read", "repo:write", "actions:read", "actions:write"}, //i18n:ignore OAuth scope and permission identifiers
	"public_repo": {"repo:read", "repo:write"},                                  //i18n:ignore OAuth scope and permission identifiers
	"repo:status": {"repo:read"},                                                //i18n:ignore OAuth scope and permission identifiers
	"read:org":    {"repo:read"},                                                //i18n:ignore OAuth scope and permission identifiers
	"workflow":    {"actions:read", "actions:write"},                            //i18n:ignore OAuth scope and permission identifiers
}

func (p *githubProvider) Introspect(ctx context.Context, _ string, payload map[string]string, _ []string) (Introspection, error) {
	token := strings.TrimSpace(payload["token"])
	if token == "" {
		return Introspection{}, apperr.Invalid("credentials.errors.token_required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase()+"/user", http.NoBody)
	if err != nil {
		return Introspection{}, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return Introspection{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return Introspection{}, apperr.Wrap(err, "credentials.errors.read_response", "service", "/user")
	}

	out := Introspection{Hint: secretbox.Mask(token)}
	if resp.StatusCode != http.StatusOK {
		out.State = StateInvalid
		out.Err = githubErrorMessage(body, resp.StatusCode)
		return out, nil
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return Introspection{}, apperr.Wrap(err, "credentials.errors.decode_response", "service", "/user")
	}
	out.State = StateValid
	out.Identity = user.Login
	out.Capabilities = githubCapabilities(resp.Header.Get("X-OAuth-Scopes"))
	out.ExpiresAt = parseGithubExpiry(resp.Header.Get("github-authentication-token-expiration"))
	return out, nil
}

// An empty header (every fine-grained PAT) must yield nil, which HasCapability reads as
// "undiscovered", not "none".
func githubCapabilities(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	set := map[string]bool{}
	for _, raw := range strings.Split(header, ",") {
		scope := strings.TrimSpace(raw)
		for _, perm := range githubScopeGrants[scope] {
			set[perm] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	bare := make([]string, 0, len(set))
	for perm := range set {
		bare = append(bare, perm)
	}
	sort.Strings(bare)
	return namespaced("github", bare)
}

var githubExpiryLayouts = []string{
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05 -0700",
	time.RFC3339,
}

func parseGithubExpiry(header string) time.Time {
	header = strings.TrimSpace(header)
	if header == "" {
		return time.Time{}
	}
	for _, layout := range githubExpiryLayouts {
		if ts, err := time.Parse(layout, header); err == nil {
			return ts.UTC()
		}
	}
	return time.Time{}
}

func githubErrorMessage(body []byte, status int) string {
	var errResp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Message != "" {
		return errResp.Message
	}
	return fmt.Sprintf("HTTP %d", status)
}
