package githubapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("OCTOPUS_JWT_SECRET", "githubapp-store-test-secret")
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestConfigRoundTripEncryptedAtRest(t *testing.T) {
	st := newTestStore(t)

	if _, err := st.GetConfig(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty store: want ErrNotConfigured, got %v", err)
	}
	cfg := AppConfig{
		AppID:         123456,
		Slug:          "octopus-test",
		ClientID:      "Iv23liTEST",
		ClientSecret:  "super-client-secret",
		PrivateKey:    "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----",
		WebhookSecret: "hook-secret",
	}
	if err := st.SetConfig(t.Context(), cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got, err := st.GetConfig(t.Context())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.AppID != cfg.AppID || got.Slug != cfg.Slug || got.ClientSecret != cfg.ClientSecret ||
		got.PrivateKey != cfg.PrivateKey || got.WebhookSecret != cfg.WebhookSecret {
		t.Errorf("round trip mismatch: %+v", got)
	}

	var rawKey, rawSecret string
	if err := st.db.QueryRow(`SELECT private_key, client_secret FROM github_app_config WHERE id = 1`).
		Scan(&rawKey, &rawSecret); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if !strings.HasPrefix(rawKey, "v1:") || strings.Contains(rawKey, "PRIVATE KEY") {
		t.Errorf("private key stored in plaintext: %.40s", rawKey)
	}
	if !strings.HasPrefix(rawSecret, "v1:") || strings.Contains(rawSecret, "super-client-secret") {
		t.Errorf("client secret stored in plaintext: %.40s", rawSecret)
	}

	cfg.Slug = "renamed"
	if err := st.SetConfig(t.Context(), cfg); err != nil {
		t.Fatalf("re-SetConfig: %v", err)
	}
	if got, _ = st.GetConfig(t.Context()); got.Slug != "renamed" {
		t.Errorf("upsert did not replace slug: %q", got.Slug)
	}

	if err := st.DeleteConfig(t.Context()); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, err := st.GetConfig(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("after delete: want ErrNotConfigured, got %v", err)
	}
}

func TestInstallationClaimPreservation(t *testing.T) {
	st := newTestStore(t)

	if err := st.UpsertInstallation(t.Context(), Installation{
		InstallationID: 77, AccountLogin: "acme-org", AccountType: "Organization", RepoSelection: "selected",
	}); err != nil {
		t.Fatalf("upsert unclaimed: %v", err)
	}
	if err := st.UpsertInstallation(t.Context(), Installation{
		InstallationID: 77, WorkspaceID: "ws-1", AccountLogin: "acme-org", AccountType: "Organization",
		RepoSelection: "selected", InstalledBy: "octocat",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.UpsertInstallation(t.Context(), Installation{
		InstallationID: 77, AccountLogin: "acme-org", AccountType: "Organization", RepoSelection: "all",
	}); err != nil {
		t.Fatalf("webhook refresh: %v", err)
	}
	got, err := st.GetInstallation(t.Context(), 77)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WorkspaceID != "ws-1" {
		t.Errorf("webhook re-delivery unclaimed the installation: wid=%q", got.WorkspaceID)
	}
	if got.InstalledBy != "octocat" {
		t.Errorf("webhook re-delivery erased installer attribution: %q", got.InstalledBy)
	}
	if got.RepoSelection != "all" {
		t.Errorf("refresh did not update repo selection: %q", got.RepoSelection)
	}
}

func TestInstallationListScoping(t *testing.T) {
	st := newTestStore(t)
	seed := []Installation{
		{InstallationID: 1, WorkspaceID: "ws-a", AccountLogin: "org-a"},
		{InstallationID: 2, WorkspaceID: "ws-b", AccountLogin: "org-b"},
		{InstallationID: 3, AccountLogin: "org-unclaimed"},
	}
	for _, inst := range seed {
		if err := st.UpsertInstallation(t.Context(), inst); err != nil {
			t.Fatalf("seed %d: %v", inst.InstallationID, err)
		}
	}
	got, err := st.ListInstallations(t.Context(), "ws-a", false)
	if err != nil {
		t.Fatalf("list ws-a: %v", err)
	}
	if len(got) != 1 || got[0].InstallationID != 1 {
		t.Errorf("ws-a list = %+v, want just installation 1", got)
	}
	all, err := st.ListInstallations(t.Context(), "", true)
	if err != nil {
		t.Fatalf("list unrestricted: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("unrestricted list has %d rows, want 3", len(all))
	}

	if err := st.DeleteByWorkspace(t.Context(), "ws-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := st.GetInstallation(t.Context(), 1); !errors.Is(err, ErrInstallationNotFound) {
		t.Errorf("ws-a installation survived purge: %v", err)
	}
	if _, err := st.GetInstallation(t.Context(), 2); err != nil {
		t.Errorf("ws-b installation lost in ws-a purge: %v", err)
	}
}

func TestUpsertPreservesFieldsOnPartialPayload(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertInstallation(t.Context(), Installation{
		InstallationID: 5, WorkspaceID: "ws-1", AccountLogin: "org-x", AccountType: "Organization",
		RepoSelection: "selected",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetSuspended(t.Context(), 5, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := st.UpsertInstallation(t.Context(), Installation{InstallationID: 5}); err != nil {
		t.Fatalf("partial upsert: %v", err)
	}
	got, err := st.GetInstallation(t.Context(), 5)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AccountLogin != "org-x" || got.AccountType != "Organization" || got.RepoSelection != "selected" {
		t.Errorf("partial upsert blanked identity fields: %+v", got)
	}
	if got.SuspendedAt == nil {
		t.Error("partial upsert cleared suspended_at")
	}
	if got.WorkspaceID != "ws-1" {
		t.Errorf("partial upsert unclaimed the row: %q", got.WorkspaceID)
	}
}

func TestSuspendLifecycle(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertInstallation(t.Context(), Installation{InstallationID: 9, AccountLogin: "org"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetSuspended(t.Context(), 9, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	got, _ := st.GetInstallation(t.Context(), 9)
	if got.SuspendedAt == nil || time.Since(*got.SuspendedAt) > time.Minute {
		t.Errorf("suspended_at not set: %+v", got.SuspendedAt)
	}
	if err := st.SetSuspended(t.Context(), 9, false); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	if got, _ = st.GetInstallation(t.Context(), 9); got.SuspendedAt != nil {
		t.Errorf("suspended_at not cleared: %+v", got.SuspendedAt)
	}
}
