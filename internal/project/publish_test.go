package project

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestWorktreePublish(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "Publish", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wtPath, branch, err := store.CreateTaskWorktree(t.Context(), proj.ID, "pub1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	wt, err := store.HostWorktreeGit(t.Context(), proj.ID, "pub1")
	if err != nil {
		t.Fatalf("HostWorktreeGit: %v", err)
	}
	remoteTip := func() string {
		return strings.TrimSpace(gitOutT(t, repo, "rev-parse", "refs/heads/"+branch))
	}
	localTip := func() string {
		return strings.TrimSpace(gitOutT(t, wtPath, "rev-parse", "HEAD"))
	}

	writeF(t, filepath.Join(wtPath, "a.txt"), "one\n")
	gitRunT(t, wtPath, "add", "-A")
	gitRunT(t, wtPath, "commit", "-q", "-m", "first")
	forced, err := wt.Publish("")
	if err != nil || forced {
		t.Fatalf("first publish = (%v, %v), want a plain push", forced, err)
	}
	if remoteTip() != localTip() {
		t.Fatal("remote branch is not at the published tip")
	}

	gitRunT(t, wtPath, "commit", "-q", "--amend", "-m", "first, reworded")
	forced, err = wt.Publish("")
	if err != nil || !forced {
		t.Fatalf("publish after a rewrite = (%v, %v), want a forced push", forced, err)
	}
	if remoteTip() != localTip() {
		t.Fatal("remote branch is not at the rewritten tip")
	}

	other := filepath.Join(t.TempDir(), "other")
	gitRunT(t, "", "clone", "-q", repo, other)
	gitRunT(t, other, "checkout", "-q", branch)
	writeF(t, filepath.Join(other, "b.txt"), "theirs\n")
	gitRunT(t, other, "add", "-A")
	gitRunT(t, other, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-q", "-m", "theirs")
	gitRunT(t, other, "push", "-q", "origin", branch)
	theirs := remoteTip()

	gitRunT(t, wtPath, "commit", "-q", "--amend", "-m", "first, reworded again")
	if _, err := wt.Publish(""); err == nil {
		t.Fatal("publish over a branch someone else moved succeeded; the lease should refuse it")
	}
	if remoteTip() != theirs {
		t.Error("their commit was overwritten")
	}
}
