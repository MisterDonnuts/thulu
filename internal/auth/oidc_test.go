package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/middleware"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func reqWithHost(host string, secure bool) *http.Request {
	r := &http.Request{Host: host, Header: http.Header{}}
	if secure {
		r.TLS = &tls.ConnectionState{}
	}
	return r
}

func TestIsAllowedFrontendOrigin(t *testing.T) {
	tests := []struct {
		name      string
		frontend  string
		allowed   string
		reqHost   string
		tls       bool
		origin    string
		wantAllow bool
	}{
		{
			name:      "explicit frontend origin allowed",
			frontend:  "https://app.example.com",
			reqHost:   "api.example.com",
			origin:    "https://app.example.com",
			wantAllow: true,
		},
		{
			name:      "trailing slash normalized",
			frontend:  "https://app.example.com/",
			origin:    "https://app.example.com",
			wantAllow: true,
		},
		{
			name:      "host injection rejected when frontend configured",
			frontend:  "https://app.example.com",
			reqHost:   "evil.com",
			origin:    "https://evil.com",
			wantAllow: false,
		},
		{
			name:      "one of the allowed origins list",
			allowed:   "https://a.example.com, https://b.example.com",
			origin:    "https://b.example.com",
			wantAllow: true,
		},
		{
			name:      "origin not in allowed list rejected",
			allowed:   "https://a.example.com",
			origin:    "https://c.example.com",
			wantAllow: false,
		},
		{
			name:      "no config falls back to same-origin (http)",
			reqHost:   "localhost:8080",
			origin:    "http://localhost:8080",
			wantAllow: true,
		},
		{
			name:      "no config rejects foreign origin",
			reqHost:   "localhost:8080",
			origin:    "http://evil.com",
			wantAllow: false,
		},
		{
			name:      "no config same-origin honors TLS scheme",
			reqHost:   "thulu.example.com",
			tls:       true,
			origin:    "https://thulu.example.com",
			wantAllow: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OCTOPUS_FRONTEND_ORIGIN", tt.frontend)
			t.Setenv("OCTOPUS_ALLOWED_ORIGINS", tt.allowed)
			r := reqWithHost(tt.reqHost, tt.tls)
			if got := isAllowedFrontendOrigin(r, tt.origin); got != tt.wantAllow {
				t.Errorf("isAllowedFrontendOrigin(%q) = %v, want %v", tt.origin, got, tt.wantAllow)
			}
		})
	}
}

func newSSOTestStore(t *testing.T) *UserStore {
	t.Helper()
	db := testpg.New(t)
	store, err := NewUserStore(db)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	return store
}

func ssoUser(t *testing.T, store *UserStore, email, name string) *User {
	t.Helper()
	u := &User{ID: genID(), Email: email, Name: name, CreatedAt: time.Now()}
	if err := store.CreateUserSSO(t.Context(), u); err != nil {
		t.Fatalf("CreateUserSSO: %v", err)
	}
	return u
}

func TestProvisionSSOWorkspace_PersonalWhenAllowed(t *testing.T) {
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	store := newSSOTestStore(t)

	first := ssoUser(t, store, "first@corp.com", "First Customer")
	firstWS, err := store.CreateWorkspace(t.Context(), first.ID, "First's workspace", "first-customer")
	if err != nil {
		t.Fatalf("create first workspace: %v", err)
	}

	newcomer := ssoUser(t, store, "new@other.com", "Newcomer")
	wsID, role, err := store.ProvisionSSOWorkspace(t.Context(), newcomer)
	if err != nil {
		t.Fatalf("ProvisionSSOWorkspace: %v", err)
	}
	if wsID == firstWS.ID {
		t.Fatal("new SSO user landed in the oldest workspace — cross-tenant join")
	}
	if role != RoleOwner {
		t.Errorf("role = %q, want owner of the personal workspace", role)
	}
	ws, err := store.GetWorkspace(t.Context(), wsID)
	if err != nil || ws == nil {
		t.Fatalf("GetWorkspace(%s): %v", wsID, err)
	}
	if ws.OwnerID != newcomer.ID {
		t.Errorf("workspace owner = %q, want %q", ws.OwnerID, newcomer.ID)
	}

	twin := ssoUser(t, store, "twin@other.com", "Newcomer")
	twinWS, _, err := store.ProvisionSSOWorkspace(t.Context(), twin)
	if err != nil {
		t.Fatalf("ProvisionSSOWorkspace (slug collision): %v", err)
	}
	if twinWS == wsID {
		t.Error("twin user got the same workspace")
	}
}

func TestProvisionSSOWorkspace_OperatorPinnedSlug(t *testing.T) {
	store := newSSOTestStore(t)

	owner := ssoUser(t, store, "owner@corp.com", "Owner")
	shared, err := store.CreateWorkspace(t.Context(), owner.ID, "Shared", "shared-landing")
	if err != nil {
		t.Fatalf("create shared workspace: %v", err)
	}

	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "shared-landing")
	u := ssoUser(t, store, "dev@corp.com", "Dev")
	wsID, role, err := store.ProvisionSSOWorkspace(t.Context(), u)
	if err != nil {
		t.Fatalf("ProvisionSSOWorkspace: %v", err)
	}
	if wsID != shared.ID {
		t.Errorf("wsID = %q, want pinned workspace %q", wsID, shared.ID)
	}
	if role != RoleViewer {
		t.Errorf("role = %q, want viewer in the pinned workspace", role)
	}
	got, err := store.GetMemberRole(t.Context(), shared.ID, u.ID)
	if err != nil || got != RoleViewer {
		t.Errorf("GetMemberRole = (%q, %v), want viewer", got, err)
	}

	t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", "developer")
	d := ssoUser(t, store, "dev3@corp.com", "Dev Three")
	if _, role, err := store.ProvisionSSOWorkspace(t.Context(), d); err != nil || role != RoleDeveloper {
		t.Errorf("with OCTOPUS_SSO_DEFAULT_ROLE=developer: role=%q err=%v, want developer", role, err)
	}
	t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", "owner")
	o := ssoUser(t, store, "own@corp.com", "Would Be Owner")
	if _, _, err := store.ProvisionSSOWorkspace(t.Context(), o); err == nil {
		t.Error("OCTOPUS_SSO_DEFAULT_ROLE=owner was accepted")
	}
	t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", "")

	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "no-such-slug")
	u2 := ssoUser(t, store, "dev2@corp.com", "Dev Two")
	if _, _, err := store.ProvisionSSOWorkspace(t.Context(), u2); err == nil {
		t.Error("expected error for nonexistent OCTOPUS_SSO_DEFAULT_WORKSPACE slug")
	}
}

func TestSSOCallbackURLFollowsForwardedProto(t *testing.T) {
	t.Setenv("OCTOPUS_OIDC_REDIRECT_URI", "")
	trustTestPeer(t)

	plain := httptest.NewRequest(http.MethodGet, "http://10.0.0.5:8585/auth/oidc/start", nil)
	if got, want := ssoCallbackURL(plain), "http://10.0.0.5:8585/auth/oidc/callback"; got != want {
		t.Errorf("plain http: ssoCallbackURL = %q, want %q", got, want)
	}

	proxied := httptest.NewRequest(http.MethodGet, "http://thulu.example.com/auth/oidc/start", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if got, want := ssoCallbackURL(proxied), "https://thulu.example.com/auth/oidc/callback"; got != want {
		t.Errorf("behind a proxy: ssoCallbackURL = %q, want %q", got, want)
	}

	t.Setenv("OCTOPUS_OIDC_REDIRECT_URI", "https://pinned.example/cb")
	if got := ssoCallbackURL(proxied); got != "https://pinned.example/cb" {
		t.Errorf("override ignored: got %q", got)
	}
}

func trustTestPeer(t *testing.T) {
	t.Helper()
	t.Setenv("OCTOPUS_TRUSTED_PROXIES", "192.0.2.1")
	middleware.ReloadTrustedProxies()
	t.Cleanup(middleware.ReloadTrustedProxies)
}

func TestForwardedProtoNeedsTrustedPeer(t *testing.T) {
	t.Setenv("OCTOPUS_OIDC_REDIRECT_URI", "")
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	t.Setenv("OCTOPUS_TRUSTED_PROXIES", "10.85.85.2")
	middleware.ReloadTrustedProxies()
	t.Cleanup(middleware.ReloadTrustedProxies)

	spoofed := httptest.NewRequest(http.MethodGet, "http://octopus.example/auth/oidc/start", nil)
	spoofed.RemoteAddr = "203.0.113.7:40000"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	if requestIsSecure(spoofed) {
		t.Error("untrusted peer's X-Forwarded-Proto was believed")
	}
	if got, want := ssoCallbackURL(spoofed), "http://octopus.example/auth/oidc/callback"; got != want {
		t.Errorf("untrusted peer: ssoCallbackURL = %q, want %q", got, want)
	}
	if got, want := frontendOrigin(spoofed), "http://octopus.example"; got != want {
		t.Errorf("untrusted peer: frontendOrigin = %q, want %q", got, want)
	}

	proxied := httptest.NewRequest(http.MethodGet, "http://octopus.example/auth/oidc/start", nil)
	proxied.RemoteAddr = "10.85.85.2:51000"
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if !requestIsSecure(proxied) {
		t.Error("trusted proxy's X-Forwarded-Proto ignored")
	}
	proxied.Header.Set("X-Forwarded-Proto", "https, http")
	if requestIsSecure(proxied) {
		t.Error("used the client's leftmost X-Forwarded-Proto over the proxy's")
	}
}

func TestFrontendOriginFollowsForwardedProto(t *testing.T) {
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	trustTestPeer(t)

	proxied := httptest.NewRequest(http.MethodGet, "http://thulu.example.com/x", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if got, want := frontendOrigin(proxied), "https://thulu.example.com"; got != want {
		t.Errorf("frontendOrigin = %q, want %q", got, want)
	}

	plain := httptest.NewRequest(http.MethodGet, "http://10.0.0.5:8585/x", nil)
	if got, want := frontendOrigin(plain), "http://10.0.0.5:8585"; got != want {
		t.Errorf("frontendOrigin = %q, want %q", got, want)
	}

	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "")
	if !isAllowedFrontendOrigin(proxied, "https://thulu.example.com") {
		t.Error("proxied origin rejected by its own fallback allowlist")
	}
}

func TestRedirectToLoginPendingAccessReason(t *testing.T) {
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	h := &Handler{}

	r := httptest.NewRequest(http.MethodGet, "http://octopus.example/auth/oidc/callback", nil)
	w := httptest.NewRecorder()
	h.redirectToLoginReason(w, r, ErrPendingAccess, loginReasonPendingAccess)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := loc.Query().Get("reason"); got != "pending_access" {
		t.Errorf("reason = %q, want pending_access", got)
	}
	if got := loc.Query().Get("error"); got != ErrPendingAccess.Error() {
		t.Errorf("error = %q, want the pending-access message", got)
	}

	w = httptest.NewRecorder()
	h.redirectToLogin(w, r, apperr.Unauthenticated("auth.errors.sso_token_exchange_failed"))
	if loc := w.Header().Get("Location"); strings.Contains(loc, "reason=") {
		t.Errorf("plain refusal carries a reason: %s", loc)
	}
}
