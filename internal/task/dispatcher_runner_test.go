package task

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestRunnerTaskLaunchesWithoutDaemonWorktreeOrCredential(t *testing.T) {
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.CreateOnRunner(t.Context(), "app", "", "runner1", project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	l := newRecordingLauncher()
	d := NewDispatcher(store, l, projects, nil, nil).WithAgentPushesResults().
		WithRunners(fakeRunners{"runner1": machine("runner1", "mac")})
	tk := &Task{WorkspaceID: testWS, Title: "on a runner", ProjectIDs: []string{proj.ID}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}

	tk = d.ensureWorktrees(t.Context(), tk)
	if len(tk.WorktreePaths) != 0 {
		t.Errorf("worktrees on the daemon: %v", tk.WorktreePaths)
	}
	if tk.BranchName != project.TaskBranchName(tk.ID) {
		t.Errorf("BranchName = %q, want %q", tk.BranchName, project.TaskBranchName(tk.ID))
	}

	at := &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}
	d.launchStage(t.Context(), tk, types.StageInProgress, "dev", at, launchOpts{})
	launches := l.launches()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want 1", len(launches))
	}
	env := launches[0]
	if env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin || env[runner.RunnerIDEnv] != "runner1" {
		t.Errorf("LLM source = %q on %q, want %q on runner1", env[engine.LLMSourceEnv], env[runner.RunnerIDEnv], engine.LLMSourceRunnerLogin)
	}
	if env["OCTOPUS_RESULT_REF"] != "" {
		t.Errorf("a runner run was told to push to the daemon: %q", env["OCTOPUS_RESULT_REF"])
	}
	if len(l.code[0]) != 0 {
		t.Errorf("worktree mounts for a runner run: %v", l.code[0])
	}
}

func TestRunnerClonedProjectSyncsInsteadOfPushing(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	ctx := t.Context()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	bare := testgit.BareRepo(t)
	proj, err := projects.Create(ctx, "app", bare, project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	runnerID := "runner1"
	if _, err := projects.Update(ctx, proj.ID, project.Patch{RunnerID: &runnerID}); err != nil {
		t.Fatal(err)
	}
	l := newRecordingLauncher()
	d := NewDispatcher(store, l, projects, nil, nil).
		WithRunners(fakeRunners{"runner1": machine("runner1", "mac")}).
		WithBranchPusher(func(context.Context, *Task, string) (bool, error) {
			t.Error("the daemon pushed a project whose runner pushes")
			return false, nil
		})
	tk := &Task{WorkspaceID: testWS, Title: "on a runner, cloned here", ProjectIDs: []string{proj.ID}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	tk = d.ensureWorktrees(ctx, tk)
	wt := tk.WorktreePaths[proj.ID]
	if wt == "" {
		t.Fatalf("no daemon worktree for a cloned project: %v", tk.WorktreePaths)
	}

	at := &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}
	d.launchStage(ctx, tk, types.StageInProgress, "dev", at, launchOpts{})
	if launches := l.launches(); len(launches) != 1 || launches[0][engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin {
		t.Fatalf("launches = %v, want one on the runner's login", launches)
	}

	machine := filepath.Join(t.TempDir(), "machine")
	gitIn(t, "", "clone", "--quiet", bare, machine)
	gitIn(t, machine, "checkout", "--quiet", "-b", tk.BranchName)
	if err := os.WriteFile(filepath.Join(machine, "agent.txt"), []byte("agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, machine, "add", ".")
	gitIn(t, machine, "-c", "user.email=r@r", "-c", "user.name=runner", "commit", "--quiet", "-m", "agent work")
	gitIn(t, machine, "push", "--quiet", "origin", "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("chat"), 0o600); err != nil {
		t.Fatal(err)
	}

	d.commitAndPushWorktrees(ctx, tk, tk.ID, types.StageInProgress, "dev", 0)
	if _, err := os.Stat(filepath.Join(wt, "agent.txt")); err != nil {
		t.Errorf("the runner's commit is not in the daemon's worktree: %v", err)
	}
	if head, remote := gitIn(t, wt, "rev-parse", "HEAD"), gitIn(t, bare, "rev-parse", tk.BranchName); head != remote {
		t.Errorf("worktree HEAD %s, remote branch %s", head, remote)
	}
	if st := gitIn(t, wt, "status", "--porcelain"); st != "?? scratch.txt" {
		t.Errorf("status = %q, want the scratch file left uncommitted", st)
	}
	if pushed, err := d.pushBranch(ctx, tk, proj.ID); pushed || err != nil {
		t.Errorf("pushBranch = (%v, %v), want (false, nil)", pushed, err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A machine's run on a Thulu project leaves its work on the result branch. In Docker mode too the
// daemon integrates it into the task's worktree and drops the branch, and the branch never shows in
// the task's activity as one the person would open.
func TestAMachinesResultIsIntegratedWithoutKubernetes(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	ctx := t.Context()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.Create(ctx, "shop", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(store, newRecordingLauncher(), projects, nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "on the mac", ProjectIDs: []string{proj.ID}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	tk = d.ensureWorktrees(ctx, tk)
	wt := tk.WorktreePaths[proj.ID]
	if wt == "" {
		t.Fatalf("no daemon worktree for a Thulu project: %v", tk.WorktreePaths)
	}

	result := project.ResultBranchName(tk.ID)
	machine := filepath.Join(t.TempDir(), "machine")
	gitIn(t, "", "clone", "--quiet", "--branch", tk.BranchName, proj.Path, machine)
	if err := os.WriteFile(filepath.Join(machine, "agent.txt"), []byte("agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, machine, "add", ".")
	gitIn(t, machine, "-c", "user.email=r@r", "-c", "user.name=runner", "commit", "--quiet", "-m", "agent work")
	gitIn(t, machine, "push", "--quiet", "origin", "+HEAD:refs/heads/"+result)
	d.RecordRunnerReport(ctx, tk.ID, RunnerReport{Runner: "mac", Pushed: []RunnerPush{{ProjectID: proj.ID, Branch: result}}})

	d.commitAndPushWorktrees(ctx, tk, tk.ID, types.StageInProgress, "dev", 0)
	if head, want := gitIn(t, wt, "log", "-1", "--format=%s"), "agent work"; head != want {
		t.Errorf("worktree HEAD = %q, want the machine's commit", head)
	}
	if refs := gitIn(t, proj.Path, "for-each-ref", "refs/heads/"+result); refs != "" {
		t.Errorf("the result branch outlived its integration: %q", refs)
	}
	events, err := store.ListEvents(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == EventBranchCreated && e.RefID == result {
			t.Errorf("the result branch was recorded as a task branch: %+v", e)
		}
	}
}

// A result that would drop the task's commits fails the run that brought it and is parked, so the
// runs after it (a Docker run never pushes a new result) integrate and push again.
func TestADivergedResultBlocksOnlyTheRunThatBroughtIt(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	ctx := t.Context()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.Create(ctx, "shop", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(store, newRecordingLauncher(), projects, nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "diverged", ProjectIDs: []string{proj.ID}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	tk = d.ensureWorktrees(ctx, tk)
	wt := tk.WorktreePaths[proj.ID]
	if err := os.WriteFile(filepath.Join(wt, "daemon.txt"), []byte("daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "-c", "user.email=d@d", "-c", "user.name=daemon", "commit", "--quiet", "-m", "daemon work")
	result := project.ResultBranchName(tk.ID)
	stray := gitIn(t, proj.Path, "-c", "user.email=r@r", "-c", "user.name=runner",
		"commit-tree", "-p", "origin/main", "-m", "stray", "origin/main^{tree}")
	gitIn(t, proj.Path, "branch", result, stray)

	keys := func() map[string]int {
		comments, err := store.ListComments(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, c := range comments {
			out[c.Key]++
		}
		return out
	}
	if stale := d.integrateAgentResults(ctx, []string{proj.ID}, tk.ID, tk.ID); !stale[proj.ID] {
		t.Fatal("the diverged result did not stop this run's push")
	}
	if got := keys(); got["task.comments.integrate_failed"] != 1 || got["task.comments.result_parked"] != 1 {
		t.Errorf("comments = %v, want the failure and where the result went", got)
	}
	if refs := gitIn(t, proj.Path, "for-each-ref", "--format=%(objectname)", "refs/octopus/parked/"); refs != stray {
		t.Errorf("parked refs = %q, want the stray result %s", refs, stray)
	}
	if stale := d.integrateAgentResults(ctx, []string{proj.ID}, tk.ID, tk.ID); stale[proj.ID] {
		t.Error("the next run is still blocked by the parked result")
	}
	if got := keys(); got["task.comments.integrate_failed"] != 1 {
		t.Errorf("comments = %v, want no new failure", got)
	}
	if head := gitIn(t, wt, "log", "-1", "--format=%s"); head != "daemon work" {
		t.Errorf("worktree HEAD = %q, want the daemon's commit kept", head)
	}
}

// A run the machine stopped on its own says why on the task, in the person's discussion.
func TestRunnerStopReasonReachesTheTask(t *testing.T) {
	for reason, want := range map[string]string{
		runner.StopDeadline:   "run time limit",
		runner.StopDaemonLost: "did not answer it",
	} {
		t.Run(reason, func(t *testing.T) {
			d, store := newKickDispatcher(t, 0)
			tk := createIn(t, store, "verify", "")
			d.RecordRunnerReport(t.Context(), tk.ID, RunnerReport{Runner: "mac", RunID: "runner-j1", StopReason: reason})
			if c := lastComment(t, store, tk.ID); !strings.Contains(c, "Runner mac stopped the run") || !strings.Contains(c, want) {
				t.Errorf("comment = %q, want the runner's stop reason (%q)", c, want)
			}
		})
	}
}
