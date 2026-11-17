package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// newTree lays out an allowed folder with two repositories (one nested a level down), a folder that
// is not a repository, and a repository outside the allowed folder.
func newTree(t *testing.T) (root, outside string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(tmp, "code")
	outside = filepath.Join(tmp, "secrets")
	for _, dir := range []string{
		filepath.Join(root, "shop"),
		filepath.Join(root, "client", "app"),
		filepath.Join(root, "notes"),
		filepath.Join(root, "node_modules", "dep"),
		outside,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range []string{filepath.Join(root, "shop"), filepath.Join(root, "client", "app"),
		filepath.Join(root, "node_modules", "dep"), outside} {
		gitIn(t, repo, "init", "-q", "-b", "main")
	}
	gitIn(t, filepath.Join(root, "shop"), "remote", "add", "origin", "https://x-access-token:secret@github.com/acme/shop.git")
	return root, outside
}

func TestScanListsReposInsideAllowedFolders(t *testing.T) {
	root, _ := newTree(t)
	cfg := &Config{Roots: []string{root}}
	inv := cfg.Scan(context.Background())
	got := map[string]runner.InventoryRepo{}
	for _, r := range inv.Repos {
		got[r.Path] = r
	}
	if len(got) != 2 {
		t.Fatalf("Scan found %d repositories, want shop and client/app: %+v", len(got), inv.Repos)
	}
	shop, ok := got[filepath.Join(root, "shop")]
	if !ok || shop.Name != "shop" || shop.Branch != "main" {
		t.Errorf("shop = %+v", shop)
	}
	if shop.Origin == "" || strings.Contains(shop.Origin, "secret") {
		t.Errorf("origin not reported or not redacted: %q", shop.Origin)
	}
	if _, ok := got[filepath.Join(root, "client", "app")]; !ok {
		t.Error("a repository one level down was not found")
	}
}

func TestCheckoutForEnforcesAllowedFolders(t *testing.T) {
	root, outside := newTree(t)
	ctx := context.Background()
	cfg := &Config{Roots: []string{root}, Projects: map[string]string{"legacy": "/old/checkout"}}

	got, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "p", Path: filepath.Join(root, "shop")})
	if err != nil || got != filepath.Join(root, "shop") {
		t.Fatalf("checkoutFor(shop) = %q, %v", got, err)
	}
	if _, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "p", Path: outside}); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("a folder outside the allowed ones: err = %v", err)
	}
	if _, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "p", Path: filepath.Join(root, "notes")}); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("a folder that is not a repository: err = %v", err)
	}
	escape := filepath.Join(root, "shop", "..", "..", "secrets")
	if _, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "p", Path: escape}); err == nil {
		t.Error("a path climbing out of the allowed folder was accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "p", Path: link}); err == nil {
		t.Error("a symlink inside the allowed folder that points outside it was accepted")
	}
	// Without a folder from the dashboard, the config's own mapping still works.
	if got, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "legacy"}); err != nil || got != "/old/checkout" {
		t.Errorf("mapped project = %q, %v", got, err)
	}
	if _, err := cfg.checkoutFor(ctx, runner.JobProject{ID: "nope"}); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("unmapped project: err = %v", err)
	}
}

func TestMappedCheckoutsCountAsAllowed(t *testing.T) {
	root, outside := newTree(t)
	cfg := &Config{Roots: []string{root}, Projects: map[string]string{"a": filepath.Join(root, "shop"), "b": outside}}
	roots := cfg.allowedRoots()
	if len(roots) != 2 || roots[0] != root || roots[1] != outside {
		t.Fatalf("allowedRoots = %v, want the root and the mapped checkout outside it", roots)
	}
	if _, ok := cfg.folderAllowed(outside); !ok {
		t.Error("a checkout mapped with add-project was refused")
	}
}

func TestAllowAndDisallowEditTheConfig(t *testing.T) {
	root, _ := newTree(t)
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := (&Config{DaemonURL: "http://d"}).Save(path); err != nil {
		t.Fatal(err)
	}
	if err := Allow(path, []string{root, root}); err != nil {
		t.Fatal(err)
	}
	if err := Allow(path, []string{filepath.Join(root, "missing")}); err == nil {
		t.Error("allowing a folder that does not exist worked")
	}
	got, err := AllowedFolders(path)
	if err != nil || len(got) != 1 || got[0] != root {
		t.Fatalf("AllowedFolders = %v, %v", got, err)
	}
	if err := Disallow(path, []string{root}); err != nil {
		t.Fatal(err)
	}
	if got, _ := AllowedFolders(path); len(got) != 0 {
		t.Errorf("after Disallow = %v", got)
	}
}
