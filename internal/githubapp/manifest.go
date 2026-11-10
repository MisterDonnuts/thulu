package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Every agent token mints PermsAgentTools; an App lacking any of it clones fine but fails every
// push and PR as if the project had no credential.
var RequiredAppPermissions = PermsAgentTools

var RecommendedAppPermissions = PermChecksRead

var DeployAppPermissions = Permissions{"actions": "write", "deployments": "write"}

type PermissionGap struct {
	Name     string `json:"name"`
	Want     string `json:"want"`
	Has      string `json:"has,omitempty"`
	Required bool   `json:"required"`
}

func permissionRank(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	}
	return 0
}

func MissingPermissions(granted map[string]string) []PermissionGap {
	var out []PermissionGap
	add := func(want Permissions, required bool) {
		var group []PermissionGap
		for name, level := range want {
			if name == "metadata" {
				continue // every App holds it and GitHub does not let it be unset
			}
			if permissionRank(granted[name]) < permissionRank(level) {
				group = append(group, PermissionGap{Name: name, Want: level, Has: granted[name], Required: required})
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		out = append(out, group...)
	}
	add(RequiredAppPermissions, true)
	add(RecommendedAppPermissions, false)
	return out
}

type ManifestOptions struct {
	BaseURL string
	Org     string
	Public  bool
	Deploy  bool
	Name    string
}

const ManifestCallbackPath = "/github/manifest/callback"

type Manifest struct {
	Name           string         `json:"name"`
	URL            string         `json:"url"`
	HookAttributes map[string]any `json:"hook_attributes"`
	RedirectURL    string         `json:"redirect_url"`
	CallbackURLs   []string       `json:"callback_urls"`
	SetupURL       string         `json:"setup_url"`
	SetupOnUpdate  bool           `json:"setup_on_update"`
	// Without this GitHub sends no OAuth code on install, and a SaaS daemon refuses every install
	// claim (VerifyInstaller).
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	Public                bool              `json:"public"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
}

func BuildManifest(opts ManifestOptions) (Manifest, string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return Manifest{}, "", apperr.Invalid("githubapp.errors.manifest_base_url")
	}
	root := base.String()
	perms := RequiredAppPermissions.With(RecommendedAppPermissions)
	// Must cover githubEventKind and prStatusEvents in internal/api, each backed by a permission
	// the App holds.
	// issue_comment is left out on purpose: it needs Issues, which nothing else asks for.
	//i18n:ignore GitHub webhook event names
	events := []string{
		"pull_request", "pull_request_review", "pull_request_review_comment", "push",
		"check_suite", "check_run", "workflow_run", "workflow_job",
	}
	if opts.Deploy {
		perms = perms.With(DeployAppPermissions)
		events = append(events, "deployment_status") //i18n:ignore GitHub webhook event name
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "Thulu " + base.Hostname() //i18n:ignore product name, the App's default name on GitHub
	}
	m := Manifest{
		Name:                  name,
		URL:                   root,
		HookAttributes:        map[string]any{"url": root + "/github/webhook", "active": true},
		RedirectURL:           root + ManifestCallbackPath,
		CallbackURLs:          []string{root + "/github/oauth/callback"},
		SetupURL:              root + "/github/setup",
		SetupOnUpdate:         true,
		RequestOAuthOnInstall: true,
		Public:                opts.Public,
		DefaultPermissions:    perms,
		DefaultEvents:         events,
	}
	target := githubcfg.WebURL() + "/settings/apps/new"
	if org := strings.TrimSpace(opts.Org); org != "" {
		target = githubcfg.WebURL() + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return m, target, nil
}

type ManifestConversion struct {
	ID            int64             `json:"id"`
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	ClientID      string            `json:"client_id"`
	ClientSecret  string            `json:"client_secret"`
	WebhookSecret string            `json:"webhook_secret"`
	PEM           string            `json:"pem"`
	Permissions   map[string]string `json:"permissions"`
}

func (s *Service) ConvertManifest(ctx context.Context, code string) (*ManifestConversion, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, apperr.Invalid("githubapp.errors.manifest_code_required")
	}
	var out ManifestConversion
	target := s.apiBase + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	if err := s.doJSON(ctx, http.MethodPost, target, "", nil, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 || out.PEM == "" {
		return nil, apperr.New("githubapp.errors.manifest_conversion_incomplete")
	}
	return &out, nil
}

func (c *ManifestConversion) Config() AppConfig {
	return AppConfig{
		AppID:         c.ID,
		Slug:          c.Slug,
		ClientID:      c.ClientID,
		ClientSecret:  c.ClientSecret,
		PrivateKey:    c.PEM,
		WebhookSecret: c.WebhookSecret,
	}
}

func MarshalManifest(m Manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Manifest states use a different hash (manifestStateHash), so an install state cannot finish an
// App registration and vice versa.

const ManifestStateTTL = 30 * time.Minute

func manifestStateHash(state string) string { return tokenHash("manifest:" + state) }

func (s *Service) IssueManifestState(ctx context.Context, workspaceID, userID string) (state, binder string, err error) {
	if s.store == nil {
		return "", "", apperr.Unavailable("githubapp.errors.state_store_unavailable")
	}
	if state, err = randomToken(); err != nil {
		return "", "", err
	}
	if binder, err = randomToken(); err != nil {
		return "", "", err
	}
	now := s.now()
	logx.Warn(s.store.pruneInstallStates(ctx, now), "githubapp.log.prune_states_failed")
	st := InstallState{WorkspaceID: workspaceID, UserID: userID, ExpiresAt: now.Add(ManifestStateTTL)}
	if err := s.store.saveInstallState(ctx, manifestStateHash(state), tokenHash(binder), st, now); err != nil {
		return "", "", apperr.Wrap(err, "githubapp.errors.save_state")
	}
	return state, binder, nil
}

func (s *Service) ConsumeManifestState(ctx context.Context, state, binder string) (*InstallState, error) {
	if s.store == nil {
		return nil, apperr.Unavailable("githubapp.errors.state_store_unavailable")
	}
	if state == "" || binder == "" {
		return nil, ErrStateInvalid
	}
	return s.store.consumeInstallState(ctx, manifestStateHash(state), tokenHash(binder), s.now())
}
