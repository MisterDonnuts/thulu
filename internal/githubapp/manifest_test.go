package githubapp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubcfg"
)

func TestMissingPermissionsNamesRequiredBeforeRecommended(t *testing.T) {
	gaps := MissingPermissions(map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"})
	var got []string
	for _, g := range gaps {
		got = append(got, g.Name+"="+g.Want)
		if g.Name == "actions" && !g.Required {
			t.Error("actions:read is what every agent token asks for; it must be required")
		}
		if g.Name == "checks" && g.Required {
			t.Error("checks:read only widens the PR section; it must not be required")
		}
	}
	if want := "actions=read,checks=read"; strings.Join(got, ",") != want {
		t.Errorf("gaps = %v, want %s", got, want)
	}

	gaps = MissingPermissions(map[string]string{"contents": "read", "pull_requests": "write", "actions": "write", "checks": "read"})
	if len(gaps) != 1 || gaps[0].Name != "contents" || gaps[0].Has != "read" || gaps[0].Want != "write" {
		t.Errorf("gaps = %+v, want contents read→write only", gaps)
	}

	if gaps := MissingPermissions(RequiredAppPermissions.With(RecommendedAppPermissions)); len(gaps) != 0 {
		t.Errorf("an App with every permission still lacks %+v", gaps)
	}
}

func TestBuildManifestHangsURLsOffTheBase(t *testing.T) {
	t.Setenv(githubcfg.EnvWebURL, "")
	m, target, err := BuildManifest(ManifestOptions{BaseURL: "https://thulu.example.com/", Org: "acme"})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if target != "https://github.com/organizations/acme/settings/apps/new" {
		t.Errorf("target = %q", target)
	}
	if m.RedirectURL != "https://thulu.example.com"+ManifestCallbackPath ||
		m.SetupURL != "https://thulu.example.com/github/setup" ||
		m.HookAttributes["url"] != "https://thulu.example.com/github/webhook" ||
		len(m.CallbackURLs) != 1 || m.CallbackURLs[0] != "https://thulu.example.com/github/oauth/callback" {
		t.Errorf("urls = %+v", m)
	}
	if m.Name != "Thulu thulu.example.com" {
		t.Errorf("name = %q", m.Name)
	}
	if gaps := MissingPermissions(m.DefaultPermissions); len(gaps) != 0 {
		t.Errorf("the manifest registers an App that lacks %+v", gaps)
	}
	if m.DefaultPermissions["deployments"] != "" {
		t.Error("deploy permissions without asking for them")
	}
	if !m.RequestOAuthOnInstall {
		t.Error("the manifest does not request OAuth on install")
	}
	if js, _ := MarshalManifest(m); !strings.Contains(js, `"request_oauth_on_install":true`) {
		t.Errorf("manifest JSON = %s", js)
	}

	m, target, err = BuildManifest(ManifestOptions{BaseURL: "http://localhost:8585", Deploy: true, Name: " Mine "})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if target != "https://github.com/settings/apps/new" {
		t.Errorf("target = %q", target)
	}
	if m.Name != "Mine" || m.DefaultPermissions["actions"] != "write" || m.DefaultPermissions["deployments"] != "write" {
		t.Errorf("deploy manifest = %+v", m)
	}
	if !strings.Contains(strings.Join(m.DefaultEvents, ","), "deployment_status") {
		t.Errorf("events = %v, want deployment_status", m.DefaultEvents)
	}

	for _, bad := range []string{"", "thulu.example.com", "ftp://x", "https://"} {
		if _, _, err := BuildManifest(ManifestOptions{BaseURL: bad}); err == nil {
			t.Errorf("base %q accepted", bad)
		}
	}
}

func TestConvertManifestPostsTheCodeUnauthenticated(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/abc123/conversions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("conversion carried Authorization %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 42, "slug": "octopus-acme", "client_id": "Iv1.x", "client_secret": "cs",
			"webhook_secret": "wh", "pem": keyPEM,
		})
	}))
	defer srv.Close()
	svc := NewService(nil)
	svc.apiBase = srv.URL

	conv, err := svc.ConvertManifest(t.Context(), "abc123")
	if err != nil {
		t.Fatalf("ConvertManifest: %v", err)
	}
	cfg := conv.Config()
	if cfg.AppID != 42 || cfg.Slug != "octopus-acme" || cfg.PrivateKey != keyPEM || cfg.WebhookSecret != "wh" || cfg.ClientSecret != "cs" {
		t.Errorf("config = %+v", cfg)
	}
	if _, err := svc.ConvertManifest(t.Context(), "expired"); err == nil {
		t.Error("an unknown code converted")
	}
	if _, err := svc.ConvertManifest(t.Context(), " "); err == nil {
		t.Error("an empty code was sent to GitHub")
	}
}

func TestManifestStateIsNotAnInstallState(t *testing.T) {
	svc, _ := stateService(t)
	ctx := t.Context()

	state, binder, err := svc.IssueManifestState(ctx, "ws-a", "op-1")
	if err != nil {
		t.Fatalf("IssueManifestState: %v", err)
	}
	if _, err := svc.ConsumeState(ctx, state, binder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("install consume of a manifest state: err = %v", err)
	}
	flow, err := svc.ConsumeManifestState(ctx, state, binder)
	if err != nil {
		t.Fatalf("ConsumeManifestState: %v", err)
	}
	if flow.UserID != "op-1" {
		t.Errorf("flow = %+v", flow)
	}
	if _, err := svc.ConsumeManifestState(ctx, state, binder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("replayed manifest state: err = %v", err)
	}

	istate, ibinder, err := svc.IssueState(ctx, "ws-a", "admin-1")
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	if _, err := svc.ConsumeManifestState(ctx, istate, ibinder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("manifest consume of an install state: err = %v", err)
	}
}
