package auth

import (
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
)

// Without a license an install creates as many workspaces as it wants.
func TestCommunityCreatesSeveralWorkspaces(t *testing.T) {
	editiontest.Community(t)
	store := newSSOTestStore(t)
	ctx := t.Context()
	u, err := store.CreateUser(ctx, "owner@example.com", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkspace(ctx, u.ID, "First", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkspace(ctx, u.ID, "Second", "second"); err != nil {
		t.Fatalf("second workspace without a license: %v", err)
	}
	if _, err := store.CreateOwnedWorkspace(ctx, u.ID, "Third", "third", 0); err != nil {
		t.Fatalf("third workspace without a license: %v", err)
	}
}

// GitHub sign-in is open on a self-hosted install, so only the first login founds a workspace.
func TestSelfHostedNewcomerWaitsAfterFirstWorkspace(t *testing.T) {
	editiontest.Community(t)
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	t.Setenv("OCTOPUS_OIDC_ISSUER", "")
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "")
	store := newSSOTestStore(t)
	ctx := t.Context()
	first, err := store.CreateUser(ctx, "first@example.com", "First")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ProvisionSSOWorkspace(ctx, first); err != nil {
		t.Fatalf("the first login gets a workspace: %v", err)
	}
	stranger, err := store.CreateUser(ctx, "stranger@example.com", "Stranger")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ProvisionSSOWorkspace(ctx, stranger); !errors.Is(err, ErrPendingAccess) {
		t.Fatalf("a later newcomer = %v, want ErrPendingAccess", err)
	}

	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	if _, _, err := store.ProvisionSSOWorkspace(ctx, stranger); err != nil {
		t.Fatalf("with personal workspaces turned on: %v", err)
	}
}
