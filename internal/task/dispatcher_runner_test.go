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
	d := NewDispatcher(store, l, projects, nil, nil).WithAgentPushesResults()
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
	if env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin {
		t.Errorf("LLM source = %q, want %q", env[engine.LLMSourceEnv], engine.LLMSourceRunnerLogin)
	}
	if env["OCTOPUS_RESULT_REF"] != "" {
		t.Errorf("a runner run was told to push to the daemon: %q", env["OCTOPUS_RESULT_REF"])
	}
	if env["STEER_ENABLED"] == "1" {
		t.Error("a runner run was launched with steering on")
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
