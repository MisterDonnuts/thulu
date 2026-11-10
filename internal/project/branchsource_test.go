package project

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func pushBranch(t *testing.T, bare, branch, file, body string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "push-clone")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "clone", bare, clone)
	run(clone, "config", "user.email", "test@example.com")
	run(clone, "config", "user.name", "test")
	run(clone, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(clone, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run(clone, "add", ".")
	run(clone, "commit", "-m", "on "+branch)
	run(clone, "push", "origin", branch)
	return run(clone, "rev-parse", "HEAD")
}

func TestBranchSource_FetchResolveExtract(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "BranchSrc", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := pushBranch(t, bare, "feature/login", "login.txt", "new login\n")

	if _, err := store.BranchCommit(t.Context(), proj.ID, "feature/login"); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("before the fetch BranchCommit = %v, want not found", err)
	}
	if err := store.FetchBranch(t.Context(), proj.ID, "feature/login", "tok-secret-789"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	got, err := store.BranchCommit(t.Context(), proj.ID, "feature/login")
	if err != nil || got != want {
		t.Fatalf("BranchCommit = %q, %v; want %s", got, err, want)
	}
	cfg, err := os.ReadFile(filepath.Join(proj.Path, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "tok-secret-789") {
		t.Error("token persisted to .git/config")
	}

	dir, err := store.AppSourceDir("batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ExtractCommit(t.Context(), proj.ID, got, dir); err != nil {
		t.Fatalf("ExtractCommit: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "login.txt")); err != nil || string(b) != "new login\n" {
		t.Fatalf("login.txt = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the extracted tree has a .git: %v", err)
	}
	if names := store.AppSourceNames(); len(names) != 1 || names[0] != "batch-1" {
		t.Fatalf("AppSourceNames = %v", names)
	}

	rc, err := store.OpenCommitArchive(t.Context(), proj.ID, got)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(rc)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("archive close: %v", err)
	}
	if !names["login.txt"] || !names["README.md"] {
		t.Fatalf("archive holds %v", names)
	}
}

func TestBranchSource_LocalBranchAndRefusals(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "BranchLocal", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := gitRun(proj.Path, "branch", "local-only"); err != nil {
		t.Fatal(err)
	}
	head, err := gitOutput(proj.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.BranchCommit(t.Context(), proj.ID, "local-only"); err != nil || got != strings.TrimSpace(head) {
		t.Fatalf("BranchCommit(local-only) = %q, %v", got, err)
	}

	err = store.FetchBranch(t.Context(), proj.ID, "nope", "tok-secret-000")
	if err == nil || strings.Contains(err.Error(), "tok-secret-000") {
		t.Fatalf("FetchBranch(nope) = %v, want a redacted failure", err)
	}
	if _, err := store.BranchCommit(t.Context(), proj.ID, "nope"); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("BranchCommit(nope) = %v, want not found", err)
	}
	for _, bad := range []string{"--upload-pack=x", "a..b", ""} {
		if err := store.FetchBranch(t.Context(), proj.ID, bad, ""); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("FetchBranch(%q) = %v, want invalid", bad, err)
		}
		if _, err := store.BranchCommit(t.Context(), proj.ID, bad); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("BranchCommit(%q) = %v, want invalid", bad, err)
		}
	}
	if _, err := store.AppSourceDir("../escape"); err == nil {
		t.Fatal("AppSourceDir accepted a path")
	}
}
