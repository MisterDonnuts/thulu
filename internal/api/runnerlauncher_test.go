package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/task/launchercontract"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type listAgents []types.AgentType

func (l listAgents) All() ([]types.AgentType, error) {
	return append([]types.AgentType(nil), l...), nil
}
func (l listAgents) Invalidate() {}

type runnerFixture struct {
	store    *runner.Store
	projects *project.Store
	launcher *runnerLauncher
	reg      *runner.RegisterResponse
	project  *project.Project
	runsDir  string
	logsDir  string
}

func newRunnerFixture(t *testing.T, ws string) *runnerFixture {
	t.Helper()
	db := testpg.New(t)
	dir := t.TempDir()
	store, err := runner.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tok, _, err := store.CreateRegistration(ctx, ws, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := store.Register(ctx, runner.RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.CreateOnRunner(ctx, "app", "", reg.RunnerID, project.CreateOpts{WorkspaceID: ws})
	if err != nil {
		t.Fatal(err)
	}
	agents := listAgents{{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}}
	f := &runnerFixture{store: store, projects: projects, reg: reg, project: proj,
		runsDir: filepath.Join(dir, "runs"), logsDir: filepath.Join(dir, "logs")}
	f.launcher = newRunnerLauncher(store, projects, func(string) types.AgentSource { return agents },
		f.runsDir, f.logsDir)
	return f
}

type onProject struct {
	*runnerLauncher
	projectID string
}

func (l onProject) LaunchForStage(ctx context.Context, t *task.Task, stage task.Stage, at *types.AgentType,
	env map[string]string, wt, sp map[string]string, ro bool,
) (string, string, string, error) {
	cp := *t
	cp.ProjectIDs = []string{l.projectID}
	return l.runnerLauncher.LaunchForStage(ctx, &cp, stage, at, env, wt, sp, ro)
}

type runnerContractDriver struct{ f *runnerFixture }

func (d runnerContractDriver) AgentType() *types.AgentType {
	return &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}
}

func (d runnerContractDriver) ForceExit(containerID string, code int) {
	ctx := context.Background()
	if _, err := d.f.store.Claim(ctx, d.f.reg.RunnerID, []string{d.f.project.ID}, time.Minute); err != nil {
		return
	}
	_ = d.f.store.Finish(ctx, d.f.reg.RunnerID, runner.JobIDFromRun(containerID), runner.FinishRequest{ExitCode: code})
	d.f.launcher.settle(ctx)
}

func TestRunnerLauncher_Contract(t *testing.T) {
	f := newRunnerFixture(t, launchercontract.Workspace)
	launchercontract.RunSuite(t, onProject{f.launcher, f.project.ID}, runnerContractDriver{f})
}

func TestRoutedLauncherSendsOnlyRunnerTasksToTheRunner(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	base, _ := launchercontract.NewFake()
	route, l := newRoutedLauncher(base, f.projects)
	route.runner = f.launcher
	ctx := context.Background()
	at := &types.AgentType{Name: "dev", Type: "dev"}

	cid, _, _, err := l.LaunchForStage(ctx, &task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t1-in_progress-1"}, nil, nil, false)
	if err != nil || !runner.IsRunnerRun(cid) {
		t.Fatalf("runner task launched as %q, %v; want a runner run", cid, err)
	}
	cid, _, _, err = l.LaunchForStage(ctx, &task.Task{ID: "t2", WorkspaceID: "ws1"},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t2-in_progress-1"}, nil, nil, false)
	if err != nil || runner.IsRunnerRun(cid) {
		t.Fatalf("plain task launched as %q, %v; want the base launcher", cid, err)
	}
	if _, steers := l.(task.RunSteerer); steers {
		t.Error("the routed launcher steers although its base does not")
	}
}

func TestRoutedLauncherWithRunnersOffRefusesRunnerTasks(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	base, _ := launchercontract.NewFake()
	_, l := newRoutedLauncher(base, f.projects)
	ctx := context.Background()
	at := &types.AgentType{Name: "dev", Type: "dev"}

	_, _, _, err := l.LaunchForStage(ctx, &task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t1-in_progress-1"}, nil, nil, false)
	if !errors.Is(err, apperr.Precondition("api.runners.errors.unavailable")) {
		t.Fatalf("runner task with runners off: err = %v, want runners unavailable", err)
	}
	cid, _, _, err := l.LaunchForStage(ctx, &task.Task{ID: "t2", WorkspaceID: "ws1"},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t2-in_progress-1"}, nil, nil, false)
	if err != nil || runner.IsRunnerRun(cid) {
		t.Fatalf("plain task launched as %q, %v; want the base launcher", cid, err)
	}
}

func TestRunnerEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := newRunnerFixture(t, "ws1")
	srv := &Server{runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}, runsDir: f.runsDir}
	mux := http.NewServeMux()
	srv.registerRunnerRoutes(mux, func(_ auth.Role, h http.HandlerFunc) http.HandlerFunc { return h })
	daemon := httptest.NewServer(mux)
	defer daemon.Close()

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	checkout := filepath.Join(tmp, "app")
	gitT(t, tmp, "init", "--quiet", "--bare", "-b", "main", remote)
	gitT(t, tmp, "clone", "--quiet", remote, checkout)
	gitT(t, checkout, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--quiet", "--allow-empty", "-m", "root")
	gitT(t, checkout, "push", "--quiet", "origin", "HEAD:main")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")

	entry := filepath.Join(tmp, "agent.sh")
	script := `set -eu
echo "agent running in $OCTOPUS_ROOT for $TASK_ID"
test "$OCTOPUS_HOST_RUN" = 1
test "$OCTOPUS_LLM_SOURCE" = runner_login
case "$OCTOPUS_API" in "` + daemon.URL + `"*) ;; *) echo "OCTOPUS_API not rewritten: $OCTOPUS_API"; exit 9;; esac
echo hello > "$OCTOPUS_ROOT/projects/app/hello.txt"
mkdir -p "$OCTOPUS_ROOT/runs/$INSTANCE_ID"
echo "done" > "$OCTOPUS_ROOT/runs/$INSTANCE_ID/comment.md"
`
	if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &hostrun.Config{DaemonURL: daemon.URL, RunnerID: f.reg.RunnerID, Credential: f.reg.Credential,
		Projects: map[string]string{f.project.ID: checkout}, WorkDir: filepath.Join(tmp, "work"),
		Concurrency: 1, Entrypoint: entry}

	instance := "dev-task-t1-in_progress-1"
	env := map[string]string{"INSTANCE_ID": instance, "TASK_ID": "t1",
		"OCTOPUS_API": "http://host.docker.internal:8585", task.EnvStagePush: "1"}
	cid, _, outDir, err := f.launcher.LaunchForStage(context.Background(),
		&task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, &types.AgentType{Name: "dev", Type: "dev"}, env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	exits := make(chan int, 1)
	f.launcher.OnExit(context.Background(), cid, func(code int, _, _ string) { exits <- code })
	var report task.RunnerReport
	f.launcher.report = func(_ context.Context, _ string, rep task.RunnerReport) { report = rep }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hostrun.New(cfg, "test").Run(ctx) }()
	deadline := time.After(30 * time.Second)
	var code int
wait:
	for {
		select {
		case code = <-exits:
			break wait
		case <-deadline:
			t.Fatalf("the run never ended; log:\n%s", f.launcher.RunLogs(cid))
		case <-time.After(200 * time.Millisecond):
			f.launcher.settle(context.Background())
		}
	}
	logs := f.launcher.RunLogs(cid)
	if code != 0 {
		t.Fatalf("exit code %d; log:\n%s", code, logs)
	}
	if !strings.Contains(logs, "agent running in") {
		t.Errorf("the agent's output is not in the run log:\n%s", logs)
	}
	if b, err := os.ReadFile(filepath.Join(outDir, "comment.md")); err != nil || strings.TrimSpace(string(b)) != "done" {
		t.Errorf("output dir on the daemon: %q, %v", b, err)
	}
	branch := project.TaskBranchName("t1")
	if len(report.Pushed) != 1 || report.Pushed[0].Branch != branch {
		t.Errorf("report = %+v, want the task branch pushed", report)
	}
	if got := gitT(t, tmp, "--git-dir", remote, "show", branch+":hello.txt"); got != "hello" {
		t.Errorf("remote %s:hello.txt = %q", branch, got)
	}
	if st := gitT(t, checkout, "status", "--porcelain"); st != "" {
		t.Errorf("the machine's own checkout was changed: %q", st)
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunnerLauncherRefusesOtherEngines(t *testing.T) {
	l := &runnerLauncher{}
	_, _, _, err := l.LaunchForStage(t.Context(), &task.Task{}, "done", &types.AgentType{},
		map[string]string{"ENGINE": "codex", "INSTANCE_ID": "i"}, nil, nil, true)
	if err == nil || !strings.Contains(apperr.Text(err), "codex") {
		t.Fatalf("err = %v, want the engine refused", err)
	}
}
