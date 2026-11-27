package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/task/launchercontract"
	"github.com/MisterDonnuts/thulu/internal/testgit"
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

// onProject plays the dispatcher: it puts every task on the fixture's project and machine.
type onProject struct {
	*runnerLauncher
	projectID string
	runnerID  string
}

func (l onProject) LaunchForStage(ctx context.Context, t *task.Task, stage task.Stage, at *types.AgentType,
	env map[string]string, wt, sp map[string]string, ro bool,
) (string, string, string, error) {
	cp := *t
	cp.ProjectIDs = []string{l.projectID}
	env = copyEnv(env)
	env[runner.RunnerIDEnv] = l.runnerID
	return l.runnerLauncher.LaunchForStage(ctx, &cp, stage, at, env, wt, sp, ro)
}

type runnerContractDriver struct{ f *runnerFixture }

func (d runnerContractDriver) AgentType() *types.AgentType {
	return &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}
}

func (d runnerContractDriver) ForceExit(containerID string, code int) {
	ctx := context.Background()
	if _, err := d.f.store.Claim(ctx, d.f.reg.RunnerID, runner.ProtocolVersion, []string{d.f.project.ID}, time.Minute); err != nil {
		return
	}
	_ = d.f.store.Finish(ctx, d.f.reg.RunnerID, runner.JobIDFromRun(containerID), runner.FinishRequest{ExitCode: code})
	d.f.launcher.settle(ctx)
}

func TestRunnerLauncher_Contract(t *testing.T) {
	f := newRunnerFixture(t, launchercontract.Workspace)
	launchercontract.RunSuite(t, onProject{f.launcher, f.project.ID, f.reg.RunnerID}, runnerContractDriver{f})
}

// Case 22: only a run the dispatcher placed on a machine goes to it.
func TestRoutedLauncherSendsOnlyRunnerTasksToTheRunner(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	base, _ := launchercontract.NewFake()
	l := newRoutedLauncher(base, f.projects)
	l.runner = f.launcher
	ctx := context.Background()
	at := &types.AgentType{Name: "dev", Type: "dev"}
	placed := func(instance string) map[string]string {
		return map[string]string{"INSTANCE_ID": instance, runner.RunnerIDEnv: f.reg.RunnerID}
	}

	cid, _, _, err := l.LaunchForStage(ctx, &task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, at, placed("dev-task-t1-in_progress-1"), nil, nil, false)
	if err != nil || !runner.IsRunnerRun(cid) {
		t.Fatalf("runner task launched as %q, %v; want a runner run", cid, err)
	}
	cid, _, _, err = l.LaunchForStage(ctx, &task.Task{ID: "t3", WorkspaceID: "ws1"},
		types.StageInProgress, at, placed("dev-task-t3-in_progress-1"), nil, nil, false)
	if err != nil || !runner.IsRunnerRun(cid) {
		t.Fatalf("a task without projects placed by its profile launched as %q, %v; want a runner run", cid, err)
	}
	cid, _, _, err = l.LaunchForStage(ctx, &task.Task{ID: "t2", WorkspaceID: "ws1"},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t2-in_progress-1"}, nil, nil, false)
	if err != nil || runner.IsRunnerRun(cid) {
		t.Fatalf("plain task launched as %q, %v; want the base launcher", cid, err)
	}
	_, _, _, err = l.LaunchForStage(ctx, &task.Task{ID: "t4", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t4-in_progress-1"}, nil, nil, false)
	if apperr.Code(err) != "api.runners.errors.task_not_on_runner" {
		t.Fatalf("a machine task the dispatcher did not place: err = %v, want it refused", err)
	}
}

func TestRoutedLauncherWithRunnersOffRefusesRunnerTasks(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	base, _ := launchercontract.NewFake()
	l := newRoutedLauncher(base, f.projects)
	ctx := context.Background()
	at := &types.AgentType{Name: "dev", Type: "dev"}

	_, _, _, err := l.LaunchForStage(ctx, &task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, at, map[string]string{"INSTANCE_ID": "dev-task-t1-in_progress-1", runner.RunnerIDEnv: f.reg.RunnerID},
		nil, nil, false)
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
	machineEnv(t)

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
	env := map[string]string{"INSTANCE_ID": instance, "TASK_ID": "t1", runner.RunnerIDEnv: f.reg.RunnerID,
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

	runHostRunner(t, cfg)
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

// Case 24: the launcher takes the engines that declare a machine login and refuses the rest.
func TestRunnerLauncherRefusesOtherEngines(t *testing.T) {
	l := &runnerLauncher{}
	launch := func(eng string) error {
		_, _, _, err := l.LaunchForStage(t.Context(), &task.Task{}, "done", &types.AgentType{},
			map[string]string{"ENGINE": eng, "INSTANCE_ID": "i"}, nil, nil, true)
		return err
	}
	for _, eng := range []string{"codex", "opencode", "no-such-engine"} {
		if err := launch(eng); apperr.Code(err) != "api.runners.errors.engine_not_supported" || !strings.Contains(apperr.Text(err), eng) {
			t.Errorf("%s: err = %v, want the engine refused", eng, err)
		}
	}
	if err := launch(engine.DefaultName); apperr.Code(err) == "api.runners.errors.engine_not_supported" {
		t.Errorf("claude-code refused: %v", err)
	}
}

// Case 23: a project kept by Thulu reaches the machine through /git/. The job names it as the /git/
// path does, carries the result branch, needs no folder mapped on the machine and no runner older
// than the protocol that fetches from the daemon.
func TestRunnerJobCarriesAThuluProjectFromTheDaemon(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	f := newRunnerFixture(t, "ws")
	ctx := t.Context()
	web, err := f.projects.Create(ctx, "web", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	sub := &task.Task{ID: "sub", ParentID: "root", WorkspaceID: "ws", ProjectIDs: []string{web.ID},
		WorktreePaths: map[string]string{web.ID: "/daemon/worktree"}}
	want := runner.JobProject{ID: web.ID, Name: filepath.Base(web.Path), DefaultBranch: web.DefaultBranch,
		Branch: project.TaskBranchName("root"), Source: runner.SourceDaemon}
	got, err := f.launcher.runnerFor(ctx, sub, f.reg.RunnerID, false)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runnerFor = %+v, %v; want %+v", got, err, want)
	}
	if spec := (runner.JobSpec{Projects: got}); !spec.SelfContained() || spec.MinProtocol() != runner.ProtocolDaemonCode {
		t.Errorf("self-contained %v, min protocol %d; want a self-contained job for protocol %d",
			spec.SelfContained(), spec.MinProtocol(), runner.ProtocolDaemonCode)
	}
	mention := &task.Task{ID: "plain", WorkspaceID: "ws", ProjectIDs: []string{web.ID}}
	if got, err := f.launcher.runnerFor(ctx, mention, f.reg.RunnerID, true); err != nil || got[0].Branch != "" {
		t.Errorf("read-only run on a task without a worktree = %+v, %v; want the default branch", got, err)
	}

	env := map[string]string{"INSTANCE_ID": "dev-task-sub-1", runner.RunnerIDEnv: f.reg.RunnerID}
	at := &types.AgentType{Name: "dev", Type: "dev"}
	if _, _, _, err := f.launcher.LaunchForStage(ctx, sub, types.StageInProgress, at, env, nil, nil, false); apperr.Code(err) != "api.runners.errors.runner_outdated" {
		t.Errorf("launch on a protocol 1 runner: err = %v, want runner_outdated", err)
	}
	if _, err := f.store.Authenticate(ctx, f.reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	f.launcher.runDeadline = func(string) time.Duration { return 90 * time.Minute }
	cid, _, _, err := f.launcher.LaunchForStage(ctx, sub, types.StageInProgress, at, env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.store.GetJob(ctx, runner.JobIDFromRun(cid))
	if err != nil || job.Spec.ResultRef != project.ResultBranchName("root") {
		t.Errorf("job = %+v, %v; want the root's result branch", job, err)
	}
	// The machine enforces the workspace's run limit too, so a run outlives no daemon by long.
	if job != nil && job.Spec.DeadlineSeconds != 90*60 {
		t.Errorf("job deadline = %d s, want %d", job.Spec.DeadlineSeconds, 90*60)
	}
}
