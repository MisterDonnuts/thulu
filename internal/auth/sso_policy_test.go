package auth

import (
	"errors"
	"testing"
)

func TestSSOAdmit_Domains(t *testing.T) {
	t.Setenv("OCTOPUS_SSO_ALLOWED_DOMAINS", "acme.com, @acme.io")
	t.Setenv("OCTOPUS_SSO_ALLOWED_GROUPS", "")

	cases := []struct {
		name  string
		id    ssoIdentity
		admit bool
	}{
		{"verified in domain", ssoIdentity{Email: "ana@acme.com", EmailVerified: true}, true},
		{"second domain, @ prefix in config", ssoIdentity{Email: "bo@ACME.io", EmailVerified: true}, true},
		{"unverified in domain", ssoIdentity{Email: "ana@acme.com", EmailVerified: false}, false},
		{"other domain", ssoIdentity{Email: "eve@evil.com", EmailVerified: true}, false},
		{"suffix trick", ssoIdentity{Email: "eve@acme.com.evil.com", EmailVerified: true}, false},
		{"subdomain not implied", ssoIdentity{Email: "eve@eu.acme.com", EmailVerified: true}, false},
		{"lookalike prefix", ssoIdentity{Email: "eve@notacme.com", EmailVerified: true}, false},
		{"no email", ssoIdentity{EmailVerified: true}, false},
	}
	for _, c := range cases {
		err := ssoAdmit(c.id)
		if c.admit && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.admit && !errors.Is(err, ErrSSONotAllowed) {
			t.Errorf("%s: admitted (err=%v)", c.name, err)
		}
	}
}

func TestSSOAdmit_Groups(t *testing.T) {
	t.Setenv("OCTOPUS_SSO_ALLOWED_DOMAINS", "")
	t.Setenv("OCTOPUS_SSO_ALLOWED_GROUPS", "octopus-users,platform")

	if err := ssoAdmit(ssoIdentity{Email: "a@x.com", Groups: []string{"sales", "platform"}}); err != nil {
		t.Errorf("member of an allowed group refused: %v", err)
	}
	if err := ssoAdmit(ssoIdentity{Email: "a@x.com", Groups: []string{"sales"}}); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("non-member admitted: %v", err)
	}
	if err := ssoAdmit(ssoIdentity{Email: "a@x.com"}); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("identity without groups admitted: %v", err)
	}
}

func TestSSOAdmit_NoPolicyAdmitsAll(t *testing.T) {
	t.Setenv("OCTOPUS_SSO_ALLOWED_DOMAINS", "")
	t.Setenv("OCTOPUS_SSO_ALLOWED_GROUPS", "")
	if err := ssoAdmit(ssoIdentity{Email: "anyone@anywhere.com"}); err != nil {
		t.Errorf("no policy configured, still refused: %v", err)
	}
}

func TestPersonalWorkspacesAllowed_Defaults(t *testing.T) {
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "")

	t.Setenv("OCTOPUS_OIDC_ISSUER", "")
	if !PersonalWorkspacesAllowed() {
		t.Error("without OIDC (GitHub/dev) personal workspaces should stay on")
	}
	t.Setenv("OCTOPUS_OIDC_ISSUER", "https://idp.example.com")
	if PersonalWorkspacesAllowed() {
		t.Error("with an enterprise IdP personal workspaces must default off")
	}
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	if !PersonalWorkspacesAllowed() {
		t.Error("explicit opt-in ignored")
	}
	t.Setenv("OCTOPUS_OIDC_ISSUER", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "off")
	if PersonalWorkspacesAllowed() {
		t.Error("explicit opt-out ignored")
	}
}

func TestSSODefaultRole(t *testing.T) {
	for raw, want := range map[string]Role{"": RoleViewer, "viewer": RoleViewer, "Developer": RoleDeveloper, "admin": RoleAdmin} {
		t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", raw)
		got, err := SSODefaultRole()
		if err != nil || got != want {
			t.Errorf("%q: got (%q, %v), want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"owner", "root", "superadmin"} {
		t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", raw)
		if _, err := SSODefaultRole(); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestProvisionSSOWorkspace_PendingAccess(t *testing.T) {
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "0")
	store := newSSOTestStore(t)

	first := ssoUser(t, store, "first@corp.com", "First")
	if _, role, err := store.ProvisionSSOWorkspace(t.Context(), first); err != nil || role != RoleOwner {
		t.Fatalf("bootstrap user: role=%q err=%v, want owner of the first workspace", role, err)
	}

	stranger := ssoUser(t, store, "stranger@corp.com", "Stranger")
	if _, _, err := store.ProvisionSSOWorkspace(t.Context(), stranger); !errors.Is(err, ErrPendingAccess) {
		t.Fatalf("second user: err=%v, want ErrPendingAccess", err)
	}
	wss, err := store.ListWorkspacesForUser(t.Context(), stranger.ID)
	if err != nil {
		t.Fatalf("ListUserWorkspaces: %v", err)
	}
	if len(wss) != 0 {
		t.Errorf("pending user has %d workspaces, want 0", len(wss))
	}
}
