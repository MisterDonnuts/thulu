package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func TestRepoKeyMatchesTheFormsOfOneRepository(t *testing.T) {
	want := "github.com/acme/app"
	for _, u := range []string{
		"https://github.com/acme/app",
		"https://github.com/Acme/App.git",
		"https://x-access-token:secret@github.com/acme/app/",
		"git@github.com:acme/app.git",
		"ssh://git@github.com:22/acme/app",
	} {
		if got := repoKey(u); got != want {
			t.Errorf("repoKey(%q) = %q, want %q", u, got, want)
		}
	}
	if repoKey("https://github.com/acme/other") == want {
		t.Error("two repositories compare equal")
	}
	if got := repoKey("/src/app/"); got != "/src/app" {
		t.Errorf("a local path = %q", got)
	}
}

type machine struct {
	remote, checkout string
	r                *Runner
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	tmp := t.TempDir()
	m := &machine{remote: filepath.Join(tmp, "remote.git"), checkout: filepath.Join(tmp, "app")}
	gitIn(t, tmp, "init", "-q", "--bare", "-b", "main", m.remote)
	gitIn(t, tmp, "clone", "-q", m.remote, m.checkout)
	gitIn(t, m.checkout, "commit", "-q", "--allow-empty", "-m", "root")
	gitIn(t, m.checkout, "push", "-q", "origin", "HEAD:main")
	if err := os.WriteFile(filepath.Join(m.checkout, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.r = &Runner{cfg: &Config{
		WorkDir:      filepath.Join(tmp, "work"),
		Projects:     map[string]string{"p1": m.checkout},
		ProjectFiles: map[string][]string{"p1": {".env", "missing.local"}},
	}}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	return m
}

func (m *machine) job(t *testing.T, p runner.JobProject, readOnly bool) *job {
	t.Helper()
	return &job{r: m.r, root: t.TempDir(), wts: map[string]string{},
		spec: &runner.JobSpec{ReadOnly: readOnly, Projects: []runner.JobProject{p}, Env: map[string]string{}}}
}

func TestTaskWorksInTheRunnersOwnClone(t *testing.T) {
	m := newMachine(t)
	ctx := context.Background()
	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1",
		RepoURL: m.remote, Push: true}
	j := m.job(t, p, false)
	if err := j.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	wt := j.wts["p1"]
	if b, err := os.ReadFile(filepath.Join(wt, ".env")); err != nil || string(b) != "SECRET=1\n" {
		t.Fatalf(".env in the worktree: %q, %v", b, err)
	}
	gitIn(t, wt, "config", "core.hooksPath", "/nowhere")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	pushed, err := j.afterRun(ctx, 0)
	if err != nil || len(pushed) != 1 {
		t.Fatalf("afterRun = %v, %v; want the branch pushed (Push)", pushed, err)
	}
	if files := gitIn(t, m.remote, "ls-tree", "--name-only", "task-1"); files != "a.txt" {
		t.Errorf("task-1 on origin has %q, want a.txt alone (no .env)", files)
	}
	if branches := gitIn(t, m.checkout, "branch", "--list", "task-1"); branches != "" {
		t.Errorf("the person's checkout got the task branch: %q", branches)
	}
	if v := gitIn(t, m.checkout, "config", "--default", "", "core.hooksPath"); v != "" {
		t.Errorf("the agent's git config reached the person's repository: %q", v)
	}
	if w := j.checkoutChanges(ctx); len(w) != 0 {
		t.Errorf("warnings for an untouched checkout: %v", w)
	}
}

func TestAFolderOfAnotherRepositoryIsRefused(t *testing.T) {
	m := newMachine(t)
	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1",
		RepoURL: "https://github.com/acme/other"}
	err := m.job(t, p, false).linkProject(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("linkProject = %v, want the wrong repository refused", err)
	}
}

func TestTheTaskBranchFollowsOrigin(t *testing.T) {
	m := newMachine(t)
	ctx := context.Background()
	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1", Push: true}
	j := m.job(t, p, false)
	if err := j.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := j.afterRun(ctx, 0); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", "-b", "task-1", m.remote, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "from elsewhere")
	gitIn(t, other, "push", "-q", "origin", "task-1")
	want := gitIn(t, other, "rev-parse", "HEAD")

	j2 := m.job(t, p, false)
	if err := j2.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, j2.wts["p1"], "rev-parse", "HEAD"); got != want {
		t.Errorf("the task worktree is at %s, want origin's %s", got, want)
	}
	ro := m.job(t, p, true)
	if err := ro.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, filepath.Join(ro.root, "projects", "app"), "rev-parse", "HEAD"); got != want {
		t.Errorf("the read-only worktree is at %s, want %s", got, want)
	}
	ro.cleanup(ctx)
}

func TestAnAgentThatLeftTheTaskBranchGetsNothingPushed(t *testing.T) {
	m := newMachine(t)
	ctx := context.Background()
	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1", Push: true}
	j := m.job(t, p, false)
	if err := j.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	gitIn(t, j.wts["p1"], "checkout", "-q", "-b", "elsewhere")
	pushed, err := j.afterRun(ctx, 0)
	if err == nil || !strings.Contains(err.Error(), "elsewhere") || len(pushed) != 0 {
		t.Fatalf("afterRun = %v, %v; want the run refused for leaving task-1", pushed, err)
	}
}

func TestAChangedCheckoutIsReported(t *testing.T) {
	m := newMachine(t)
	ctx := context.Background()
	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1"}
	j := m.job(t, p, false)
	if err := j.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.checkout, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := j.checkoutChanges(ctx); len(w) != 1 || !strings.Contains(w[0], m.checkout) {
		t.Errorf("warnings = %v, want one naming %s", w, m.checkout)
	}
}
