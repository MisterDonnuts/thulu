package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestRunnerPredicates(t *testing.T) {
	cases := []struct {
		name       string
		p          Project
		code, runs bool
	}{
		{"git project", Project{Source: SourceGit}, false, false},
		{"upload project", Project{Source: SourceUpload}, false, false},
		{"git project on a runner", Project{Source: SourceGit, RunnerID: "r1"}, false, true},
		{"runner project", Project{Source: SourceRunner, RunnerID: "r1"}, true, true},
		{"runner project that lost its runner", Project{Source: SourceRunner}, true, true},
	}
	for _, c := range cases {
		if got := c.p.CodeOnRunner(); got != c.code {
			t.Errorf("%s: CodeOnRunner = %v, want %v", c.name, got, c.code)
		}
		if got := c.p.RunsOnRunner(); got != c.runs {
			t.Errorf("%s: RunsOnRunner = %v, want %v", c.name, got, c.runs)
		}
	}
}

func TestCheckRunnerPatch(t *testing.T) {
	git := Project{Name: "app", Source: SourceGit, RepoURL: "https://github.com/o/app"}
	gitOnRunner := git
	gitOnRunner.RunnerID = "r1"
	cases := []struct {
		name   string
		p      Project
		runner string
		kind   apperr.Kind
	}{
		{"git project to a runner", git, "r1", apperr.KindUnspecified},
		{"git project back to the daemon", gitOnRunner, "", apperr.KindUnspecified},
		{"git project to another runner", gitOnRunner, "r2", apperr.KindUnspecified},
		{"git project echoing no runner", git, "", apperr.KindUnspecified},
		{"git project without a remote", Project{Name: "app", Source: SourceGit}, "r1", apperr.KindPrecondition},
		{"upload project", Project{Name: "app", Source: SourceUpload}, "r1", apperr.KindPrecondition},
		{"upload project echoing no runner", Project{Name: "app", Source: SourceUpload}, "", apperr.KindUnspecified},
		{"runner project cleared", Project{Name: "app", Source: SourceRunner, RunnerID: "r1"}, "", apperr.KindInvalid},
		{"runner project echoed", Project{Name: "app", Source: SourceRunner, RunnerID: "r1"}, "r1", apperr.KindUnspecified},
	}
	for _, c := range cases {
		err := c.p.checkRunnerPatch(c.runner)
		switch {
		case c.kind == apperr.KindUnspecified && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.kind != apperr.KindUnspecified && apperr.KindOf(err) != c.kind:
			t.Errorf("%s: err = %v (kind %v), want kind %v", c.name, err, apperr.KindOf(err), c.kind)
		}
	}
}

func TestUpdateRunnerID(t *testing.T) {
	store := newTestStore(t)
	proj, err := store.Create(t.Context(), "app", testgit.BareRepo(t), CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runnerID := "r1"
	if _, err := store.Update(t.Context(), proj.ID, Patch{RunnerID: &runnerID}); err != nil {
		t.Fatalf("set runner: %v", err)
	}
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil || got.RunnerID != "r1" || !got.RunsOnRunner() || got.CodeOnRunner() {
		t.Fatalf("after set: %+v, %v; want a git project running on r1", got, err)
	}
	empty := ""
	if _, err := store.Update(t.Context(), proj.ID, Patch{RunnerID: &empty}); err != nil {
		t.Fatalf("clear runner: %v", err)
	}
	if got, _ = store.Get(t.Context(), proj.ID); got.RunnerID != "" {
		t.Errorf("after clear: runner_id = %q", got.RunnerID)
	}
	onRunner, err := store.CreateOnRunner(t.Context(), "remote-only", "", "r1", CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("CreateOnRunner: %v", err)
	}
	if _, err := store.Update(t.Context(), onRunner.ID, Patch{RunnerID: &empty}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Errorf("clearing a runner project's runner: %v, want invalid", err)
	}
}

func runnerMachine(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "machine")
	gitRunT(t, "", "clone", "--quiet", bare, dir)
	return dir
}

func runnerPush(t *testing.T, machine, branch, file string) {
	t.Helper()
	writeF(t, filepath.Join(machine, file), file)
	gitRunT(t, machine, "add", ".")
	gitRunT(t, machine, "-c", "user.email=r@r", "-c", "user.name=runner", "commit", "--quiet", "-m", "runner: "+file)
	gitRunT(t, machine, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
}

func TestSyncRunnerBranch(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	machine := runnerMachine(t, bare)
	advanceOriginMain(t, bare, "main moved after the runner cloned")
	proj, err := store.Create(t.Context(), "app", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const task = "rs1"
	branch := taskBranchName(task)
	wt, err := store.CreateRunnerTaskWorktree(t.Context(), proj.ID, task, "")
	if err != nil {
		t.Fatalf("CreateRunnerTaskWorktree: %v", err)
	}
	if found, err := store.SyncRunnerBranch(t.Context(), proj.ID, task, ""); found || err != nil {
		t.Fatalf("sync before any push = (%v, %v), want (false, nil)", found, err)
	}

	gitRunT(t, machine, "checkout", "--quiet", "-b", branch)
	runnerPush(t, machine, branch, "first.txt")
	if found, err := store.SyncRunnerBranch(t.Context(), proj.ID, task, ""); !found || err != nil {
		t.Fatalf("sync after a push = (%v, %v)", found, err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "first.txt")); err != nil {
		t.Errorf("the runner's file is not in the daemon's worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "advance.txt")); err == nil {
		t.Error("the worktree kept the newer main the runner's branch does not have")
	}

	runnerPush(t, machine, branch, "second.txt")
	if _, err := store.SyncRunnerBranch(t.Context(), proj.ID, task, ""); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	head := strings.TrimSpace(gitOutT(t, wt.Path, "rev-parse", "HEAD"))
	remote := strings.TrimSpace(gitOutT(t, bare, "rev-parse", branch))
	if head != remote {
		t.Errorf("worktree HEAD %s, remote %s %s", head, branch, remote)
	}
}

func TestSyncRunnerBranchKeepsDaemonCommits(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "app", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const task = "rs2"
	branch := taskBranchName(task)
	wt, err := store.CreateRunnerTaskWorktree(t.Context(), proj.ID, task, "")
	if err != nil {
		t.Fatalf("CreateRunnerTaskWorktree: %v", err)
	}
	writeF(t, filepath.Join(wt.Path, "chat.txt"), "edited from task chat")
	gitRunT(t, wt.Path, "add", ".")
	gitRunT(t, wt.Path, "-c", "user.email=d@d", "-c", "user.name=daemon", "commit", "--quiet", "-m", "chat edit")
	machine := runnerMachine(t, bare)
	gitRunT(t, machine, "checkout", "--quiet", "-b", branch)
	runnerPush(t, machine, branch, "runner.txt")

	found, err := store.SyncRunnerBranch(t.Context(), proj.ID, task, "")
	if !found || apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("sync = (%v, %v), want the remote found and a conflict", found, err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "chat.txt")); err != nil {
		t.Errorf("the daemon's own commit was dropped: %v", err)
	}
}

func TestCreateRunnerTaskWorktreeStartsFromThePushedBranch(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "app", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const task = "rs3"
	branch := taskBranchName(task)
	machine := runnerMachine(t, bare)
	gitRunT(t, machine, "checkout", "--quiet", "-b", branch)
	runnerPush(t, machine, branch, "pushed.txt")

	wt, err := store.CreateRunnerTaskWorktree(t.Context(), proj.ID, task, "")
	if err != nil {
		t.Fatalf("CreateRunnerTaskWorktree: %v", err)
	}
	if wt.Branch != branch {
		t.Errorf("branch = %q, want %q", wt.Branch, branch)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "pushed.txt")); err != nil {
		t.Errorf("the worktree did not start from the runner's branch: %v", err)
	}
}
