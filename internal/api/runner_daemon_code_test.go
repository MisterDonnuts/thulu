package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// machineRig is a daemon in Docker mode with a Thulu project, a task worktree on it and a registered
// machine, which runs one job through the daemon's real /git/ and a real hostrun runner.
type machineRig struct {
	srv    *Server
	h      http.Handler
	projID string
	taskID string
	wt     project.TaskWorktree
	proj   *project.Project
	key    string
	reg    *runner.RegisterResponse
}

func newMachineRig(t *testing.T) *machineRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	machineEnv(t)
	srv, h := newIntegrationServer(t)
	rs, err := runner.NewStore(srv.secretsStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetRunners(rs)
	ctx := t.Context()
	token, ws := mintWorkspaceUser(t, srv, "machine-git@test.com", "MG")
	projID := createProject(t, h, token, "shop")
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "on the mac", "project_ids": []string{projID}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	taskID := decodeID(t, w.Body.Bytes())
	wt, err := srv.projectStore.CreateTaskWorktreeAuth(ctx, projID, taskID, "")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := srv.projectStore.Get(ctx, projID)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := srv.authStore.CreateRunScopedAPIKey(ctx, auth.RunScope{TaskID: taskID, Agent: "dev"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	regTok, _, err := rs.CreateRegistration(ctx, ws, "mac", "")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := rs.Register(ctx, runner.RegisterRequest{Token: regTok})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Authenticate(ctx, reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	return &machineRig{srv: srv, h: h, projID: projID, taskID: taskID, wt: wt, proj: proj, key: key, reg: reg}
}

// run places one in_progress run on the machine with script as the agent and waits for it to end.
func (m *machineRig) run(t *testing.T, script string) task.RunnerReport {
	t.Helper()
	ctx := t.Context()
	daemon := httptest.NewServer(m.h)
	defer daemon.Close()
	tmp := t.TempDir()
	entry := filepath.Join(tmp, "agent.sh")
	if err := os.WriteFile(entry, []byte("set -eu\n"+script), 0o600); err != nil {
		t.Fatal(err)
	}

	tk, err := m.srv.taskStore.Get(ctx, m.taskID)
	if err != nil {
		t.Fatal(err)
	}
	tk.WorktreePaths = map[string]string{m.projID: m.wt.Path}
	l := m.srv.runnerRoute.runner
	env := map[string]string{"INSTANCE_ID": "dev-task-" + m.taskID + "-1", "TASK_ID": m.taskID,
		runner.RunnerIDEnv: m.reg.RunnerID, "OCTOPUS_API": "http://host.docker.internal:8585", "OCTOPUS_API_KEY": m.key}
	cid, _, _, err := l.LaunchForStage(ctx, tk, types.StageInProgress, &types.AgentType{Name: "dev", Type: "dev"},
		env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	exits := make(chan int, 1)
	l.OnExit(ctx, cid, func(code int, _, _ string) { exits <- code })
	var report task.RunnerReport
	l.report = func(_ context.Context, _ string, rep task.RunnerReport) { report = rep }

	cfg := &hostrun.Config{DaemonURL: daemon.URL, RunnerID: m.reg.RunnerID, Credential: m.reg.Credential,
		WorkDir: filepath.Join(tmp, "work"), Concurrency: 1, Entrypoint: entry}
	runHostRunner(t, cfg)
	deadline := time.After(60 * time.Second)
	for {
		select {
		case code := <-exits:
			if code != 0 {
				t.Fatalf("exit code %d; log:\n%s", code, l.RunLogs(cid))
			}
			result := project.ResultBranchName(m.taskID)
			if len(report.Pushed) != 1 || report.Pushed[0].Branch != result {
				t.Fatalf("report = %+v, want the result branch pushed; log:\n%s", report, l.RunLogs(cid))
			}
			return report
		case <-deadline:
			t.Fatalf("the run never ended; log:\n%s", l.RunLogs(cid))
		case <-time.After(200 * time.Millisecond):
			l.settle(context.Background())
		}
	}
}

// integrated checks that the daemon's worktree holds files as committed work and that no result branch
// or parked result is left.
func (m *machineRig) integrated(t *testing.T, files map[string]string) {
	t.Helper()
	for name, want := range files {
		if b, err := os.ReadFile(filepath.Join(m.wt.Path, name)); err != nil || strings.TrimSpace(string(b)) != want {
			t.Errorf("the daemon's worktree has %s = %q, %v; want %q", name, b, err, want)
		}
	}
	if st := gitT(t, m.wt.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the integrated work is not committed: %q", st)
	}
	result := project.ResultBranchName(m.taskID)
	if refs := gitT(t, m.proj.Path, "for-each-ref", "refs/heads/"+result, "refs/octopus/parked/"); refs != "" {
		t.Errorf("the result outlived its integration: %q", refs)
	}
}

// published returns where GitHub (the project's remote) has the task branch, "" when it has none.
func (m *machineRig) published(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", m.proj.RepoURL, "rev-parse", "--verify", "--quiet",
		"refs/heads/"+m.wt.Branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Case 32: a run placed on a machine works on a project kept by Thulu. The machine fetches the task
// branch through the daemon's real /git/ with the run key, the agent commits nothing itself, the
// machine pushes the result branch, and the daemon integrates it into the task's worktree as the push
// lands, as it does for a pod. That final push publishes nothing: GitHub gets it at the end of the
// run, only if it ended well and the column pushes.
func TestRunnerWorksOnAThuluProjectThroughGit(t *testing.T) {
	m := newMachineRig(t)
	m.run(t, `echo from-the-mac > "$OCTOPUS_ROOT/projects/shop/hello.txt"
`)
	m.integrated(t, map[string]string{"hello.txt": "from-the-mac"})
	if got := m.published(t); got != "" {
		t.Errorf("the run's final push published the task branch at %s; the column decides that", got)
	}
}

// The agent pushes mid-run while GitHub's default branch has moved. The daemon publishes that push
// without rebasing the task branch, so the agent's pr_create finds it and the machine's final result
// still lands on it instead of being parked as diverged.
func TestAMidRunPushKeepsTheMachinesResultOnTheTaskBranch(t *testing.T) {
	m := newMachineRig(t)
	moved := gitT(t, m.proj.RepoURL, "commit-tree", "-p", "main", "-m", "upstream moved", "main^{tree}")
	m.run(t, `cd "$OCTOPUS_ROOT/projects/shop"
echo one > one.txt
git add one.txt
git commit -q -m one
git --git-dir="`+m.proj.RepoURL+`" update-ref refs/heads/main `+moved+`
git push -q
echo two > two.txt
`)
	m.integrated(t, map[string]string{"one.txt": "one", "two.txt": "two"})
	if got := m.published(t); got == "" || gitT(t, m.wt.Path, "log", "-1", "--format=%s", got) != "one" {
		t.Errorf("GitHub has the task branch at %q, want the agent's own push and not the final one", got)
	}
}
