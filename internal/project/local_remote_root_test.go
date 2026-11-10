package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestLocalRemoteRoot(t *testing.T) {
	t.Setenv(envAllowLocalRemotes, "")
	base := t.TempDir()
	sources := filepath.Join(base, "sources")
	if err := os.MkdirAll(sources, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envLocalRemoteRoot, sources)

	bare := testgit.BareRepoAt(t, filepath.Join(sources, "app.git"))

	outside := testgit.BareRepoAt(t, filepath.Join(base, "projects", "other.git"))
	checkout := filepath.Join(sources, "checkout")
	testgit.Clone(t, bare, checkout)
	escape := filepath.Join(sources, "escape.git")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sources, "link.git")
	if err := os.Symlink(bare, inner); err != nil {
		t.Fatal(err)
	}

	for _, ok := range []string{bare, bare + "/", "file://" + bare, inner} {
		if err := ValidateRepoURL(ok); err != nil {
			t.Errorf("ValidateRepoURL(%q) = %v, want allowed", ok, err)
		}
	}
	for _, bad := range []string{
		outside,
		"file://" + outside,
		escape,
		sources,
		checkout,
		filepath.Join(checkout, ".git"),
		filepath.Join(sources, "missing.git"),
		filepath.Join(sources, "..", "projects", "other.git"),
		"file://evil.example" + bare,
		"https://localhost/o/r",
	} {
		if err := ValidateRepoURL(bad); err == nil {
			t.Errorf("ValidateRepoURL(%q) = nil, want rejected", bad)
		}
	}

	if protos := currentRemotePolicy().allowedProtocols(); !slices.Contains(protos, "file") {
		t.Errorf("protocols = %v: git would refuse the clone the policy just allowed", protos)
	}

	store := newTestStore(t)
	if _, err := store.Create(t.Context(), "Scoped", bare, CreateOpts{WorkspaceID: testWS}); err != nil {
		t.Fatalf("Create from the sources root: %v", err)
	}
	if _, err := store.Create(t.Context(), "Outside", outside, CreateOpts{WorkspaceID: testWS}); err == nil {
		t.Error("Create cloned a repository outside the sources root")
	}
}

func TestLocalRemoteRootUnsetRefusesLocal(t *testing.T) {
	t.Setenv(envAllowLocalRemotes, "")
	t.Setenv(envLocalRemoteRoot, "")
	bare := testgit.BareRepo(t)
	if err := ValidateRepoURL(bare); err == nil {
		t.Error("a local path was allowed with neither opt-in")
	}
	if protos := currentRemotePolicy().allowedProtocols(); slices.Contains(protos, "file") {
		t.Errorf("protocols = %v, want no file transport", protos)
	}

	t.Setenv(envLocalRemoteRoot, "data/sources")
	if err := ValidateRepoURL(bare); err == nil {
		t.Error("a relative root allowed a local path")
	}
}
