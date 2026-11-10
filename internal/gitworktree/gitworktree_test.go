package gitworktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// On macOS /var is a symlink to /private/var; git records the resolved path.
func realRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommonDir(t *testing.T) {
	root := realRoot(t)
	src := testgit.BareRepo(t)
	wtA := testgit.LinkedWorktree(t, src, root, "alpha", "task-a")
	wtA2 := testgit.LinkedWorktree(t, src, root, "alpha", "task-a2")
	wtB := testgit.LinkedWorktree(t, src, root, "beta", "task-b")
	commonA := filepath.Join(root, "alpha", ".git")
	commonB := filepath.Join(root, "beta", ".git")

	plainClone := filepath.Join(root, "plain")
	testgit.Clone(t, src, plainClone)

	crossProject := testgit.LinkedWorktree(t, src, root, "alpha", "task-cross")
	write(t, filepath.Join(crossProject, ".git"), "gitdir: "+filepath.Join(commonB, "worktrees", filepath.Base(wtB))+"\n")

	sibling := testgit.LinkedWorktree(t, src, root, "alpha", "task-sib")
	write(t, filepath.Join(sibling, ".git"), "gitdir: "+filepath.Join(commonA, "worktrees", filepath.Base(wtA2))+"\n")

	relative := testgit.LinkedWorktree(t, src, root, "alpha", "task-rel")
	write(t, filepath.Join(relative, ".git"), "gitdir: ../../alpha/.git/worktrees/alpha--task-rel\n")

	garbage := testgit.LinkedWorktree(t, src, root, "alpha", "task-garbage")
	write(t, filepath.Join(garbage, ".git"), "not a pointer\n")

	outside := testgit.LinkedWorktree(t, src, root, "alpha", "task-outside")
	write(t, filepath.Join(outside, ".git"), "gitdir: /etc\n")

	symlinked := testgit.LinkedWorktree(t, src, root, "alpha", "task-link")
	if err := os.Remove(filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(commonA, "worktrees", filepath.Base(symlinked)), filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}

	planted := testgit.LinkedWorktree(t, src, root, "alpha", "task-plant")
	fake := filepath.Join(planted, "fake")
	write(t, filepath.Join(fake, "worktrees", "x", "gitdir"), filepath.Join(planted, ".git")+"\n")
	write(t, filepath.Join(fake, "worktrees", "x", "commondir"), "../..\n")
	write(t, filepath.Join(planted, ".git"), "gitdir: "+filepath.Join(fake, "worktrees", "x")+"\n")

	cases := []struct {
		name     string
		worktree string
		want     string
		notWT    bool
		wantErr  bool
	}{
		{name: "linked worktree", worktree: wtA, want: commonA},
		{name: "second worktree of the same clone", worktree: wtA2, want: commonA},
		{name: "worktree of another clone", worktree: wtB, want: commonB},
		{name: "plain clone", worktree: plainClone, notWT: true},
		{name: "no .git at all", worktree: t.TempDir(), notWT: true},
		{name: "missing directory", worktree: filepath.Join(root, "nope"), notWT: true},
		{name: "pointer into another project", worktree: crossProject, wantErr: true},
		{name: "pointer at a sibling's admin dir", worktree: sibling, wantErr: true},
		{name: "relative pointer", worktree: relative, wantErr: true},
		{name: "not a pointer", worktree: garbage, wantErr: true},
		{name: "pointer outside any clone", worktree: outside, wantErr: true},
		{name: "symlinked .git", worktree: symlinked, wantErr: true},
		{name: "planted chain stays inside the worktree", worktree: planted, want: fake},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CommonDir(tc.worktree)
			switch {
			case tc.notWT:
				if !errors.Is(err, ErrNotLinkedWorktree) {
					t.Fatalf("CommonDir = %q, %v; want ErrNotLinkedWorktree", got, err)
				}
			case tc.wantErr:
				if err == nil || errors.Is(err, ErrNotLinkedWorktree) {
					t.Fatalf("CommonDir = %q, %v; want a verification error", got, err)
				}
			default:
				if err != nil || got != tc.want {
					t.Fatalf("CommonDir = %q, %v; want %q", got, err, tc.want)
				}
			}
		})
	}
}

func TestAdminDirIgnoresPointer(t *testing.T) {
	root := realRoot(t)
	wt := testgit.LinkedWorktree(t, testgit.BareRepo(t), root, "alpha", "task-a")
	common := filepath.Join(root, "alpha", ".git")
	want := filepath.Join(common, "worktrees", filepath.Base(wt))

	write(t, filepath.Join(wt, ".git"), "gitdir: /tmp/attacker\n")
	got, err := AdminDir(common, wt)
	if err != nil || got != want {
		t.Fatalf("AdminDir after tamper = %q, %v; want %q", got, err, want)
	}
	if _, err := AdminDir(common, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("unregistered worktree: err = %v", err)
	}
	if _, err := AdminDir(filepath.Join(root, "nope"), wt); err == nil {
		t.Error("missing git dir: expected an error")
	}
}
