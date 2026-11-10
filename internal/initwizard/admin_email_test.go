package initwizard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/schema/schematest"
)

func askWorkspaceWith(t *testing.T, identity Identity, existing *EnvFile, answers string) (*Config, string, error) {
	t.Helper()
	var out bytes.Buffer
	cfg := &Config{Identity: identity}
	iv := &Interviewer{
		P:        NewTestPrompter(strings.NewReader(answers), &out),
		Cfg:      cfg,
		Ctx:      context.Background(),
		Existing: existing,
	}
	err := iv.askWorkspace()
	return cfg, out.String(), err
}

func TestAskWorkspace_AsksTheFirstAdminWithALogin(t *testing.T) {
	cfg, _, err := askWorkspaceWith(t, IdentityOIDC, nil, "\n\n Ana@Corp.io \n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminEmail != "ana@corp.io" {
		t.Errorf("admin email = %q, want ana@corp.io", cfg.AdminEmail)
	}
	if cfg.OperatorEmail != "bootstrap@octopus.local" {
		t.Errorf("owner = %q: the placeholder must stay apart from the admin", cfg.OperatorEmail)
	}
}

func TestAskWorkspace_SkippingTheAdminDefersIt(t *testing.T) {
	cfg, _, err := askWorkspaceWith(t, IdentityGitHub, nil, "\n\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminEmail != "" {
		t.Errorf("admin email = %q, want none", cfg.AdminEmail)
	}
	if got := strings.Join(deferredNames(cfg.Deferred), ","); !strings.Contains(got, "First admin") {
		t.Errorf("deferred = %q, want the first admin listed", got)
	}
}

func TestAskWorkspace_ReasksAWrongAdmin(t *testing.T) {
	cfg, out, err := askWorkspaceWith(t, IdentityOIDC, nil, "\n\nbootstrap@octopus.local\nana\nana@corp.io\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "placeholder owner") || !strings.Contains(out, "not an email") {
		t.Errorf("both refusals should be shown:\n%s", out)
	}
	if cfg.AdminEmail != "ana@corp.io" {
		t.Errorf("admin email = %q, want the third answer", cfg.AdminEmail)
	}
}

func TestAskWorkspace_NoLoginAsksNoAdmin(t *testing.T) {
	cfg, out, err := askWorkspaceWith(t, IdentityNone, nil, "\n\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminEmail != "" || strings.Contains(out, "identity provider") {
		t.Errorf("a no-login install was asked for an admin:\n%s", out)
	}
}

func TestAskWorkspace_KeepsTheLandingWorkspace(t *testing.T) {
	existing := &EnvFile{}
	existing.Set("OCTOPUS_SSO_DEFAULT_WORKSPACE", "acme")
	cfg, _, err := askWorkspaceWith(t, IdentityOIDC, existing, "\n\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.DefaultWorkspace != "acme" {
		t.Errorf("default workspace = %q: a rerun dropped what the env file had", cfg.OIDC.DefaultWorkspace)
	}
}

func TestInviteOwner_FirstSignInTakesTheWorkspace(t *testing.T) {
	ctx := context.Background()
	handle := schematest.NewDB(t)
	store, err := auth.NewUserStore(handle)
	if err != nil {
		t.Fatal(err)
	}
	boot := &Bootstrap{store: store, handle: handle}

	wsID, _, err := boot.EnsureWorkspace(ctx, "bootstrap@octopus.local", "Operator", "Default", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.InviteOwner(ctx, wsID, "ana@corp.io"); err != nil {
		t.Fatal(err)
	}
	if err := boot.InviteOwner(ctx, wsID, "ana@corp.io"); err != nil {
		t.Fatalf("second invite: %v", err)
	}

	ana, err := store.CreateUser(ctx, "ana@corp.io", "Ana")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkOIDCIdentity(ctx, ana.ID, "oidc", "sub-ana", "ana@corp.io"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInvitations(ctx, ana.ID, "ana@corp.io"); err != nil {
		t.Fatal(err)
	}
	ws, err := store.GetWorkspace(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.OwnerID != ana.ID {
		t.Errorf("owner = %q, want the invited admin %q", ws.OwnerID, ana.ID)
	}
	if role, _ := store.GetMemberRole(ctx, wsID, ana.ID); role != auth.RoleOwner {
		t.Errorf("role = %q, want owner", role)
	}
}

func TestAskWorkspace_KeepsTheOperatorWorkspace(t *testing.T) {
	existing := &EnvFile{}
	existing.Set("OCTOPUS_OPERATOR_WORKSPACE", "ws-1")
	cfg, _, err := askWorkspaceWith(t, IdentityNone, existing, "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceID != "ws-1" {
		t.Fatalf("workspace id = %q, want the one deploy/.env has", cfg.WorkspaceID)
	}
	env := &EnvFile{}
	cfg.ApplyToEnv(env)
	if got := env.Get("OCTOPUS_OPERATOR_WORKSPACE"); got != "ws-1" {
		t.Errorf("written before the bootstrap: OCTOPUS_OPERATOR_WORKSPACE = %q", got)
	}

	var out bytes.Buffer
	iv := &Interviewer{P: NewTestPrompter(strings.NewReader("\n\n"), &out), Cfg: &Config{Identity: IdentityNone}, Ctx: context.Background()}
	iv.PreviousWorkspace.ID = "ws-2"
	if err := iv.askWorkspace(); err != nil {
		t.Fatal(err)
	}
	if iv.Cfg.WorkspaceID != "ws-2" {
		t.Errorf("workspace id = %q, want the one the answers file has", iv.Cfg.WorkspaceID)
	}
}
