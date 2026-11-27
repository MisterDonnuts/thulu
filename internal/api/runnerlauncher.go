package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type runnerLauncher struct {
	store    *runner.Store
	projects *project.Store
	agents   func(workspaceID string) types.AgentSource
	runsDir  string
	logsDir  string
	baseCtx  context.Context

	logSink   func(ctx context.Context, a *agent.Agent, fullLogs string)
	report    func(ctx context.Context, taskID string, rep task.RunnerReport)
	reconcile func(ctx context.Context)
	// Reads a chat's task for its code; nil leaves task chats without it.
	task func(ctx context.Context, id string) (*task.Task, error)
	// What a stage run reads of the task's locked tests at its end; nil asks nothing.
	testLock func(ctx context.Context, t *task.Task, stage task.Stage) *runner.TestLock
	// The workspace's run time limit, sent with each job so the machine enforces it too; nil sends
	// none.
	runDeadline func(workspaceID string) time.Duration

	claimTimeout time.Duration
	started      time.Time

	mu        sync.Mutex
	callbacks map[string]func(int, string, string)
	steer     *steerHub
}

var (
	_ task.AgentLauncher       = (*runnerLauncher)(nil)
	_ task.RunSteerer          = (*runnerLauncher)(nil)
	_ chatruntime.MachineTurns = (*runnerLauncher)(nil)
)

func newRunnerLauncher(store *runner.Store, projects *project.Store,
	agents func(workspaceID string) types.AgentSource, runsDir, logsDir string,
) *runnerLauncher {
	timeout := runner.DefaultClaimTimeout
	if v, err := time.ParseDuration(os.Getenv("OCTOPUS_RUNNER_CLAIM_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return &runnerLauncher{
		store: store, projects: projects, agents: agents,
		runsDir: runsDir, logsDir: logsDir, baseCtx: context.Background(),
		claimTimeout: timeout, started: time.Now().UTC(),
		callbacks: map[string]func(int, string, string){}, steer: newSteerHub(),
	}
}

const settleEvery = 2 * time.Second

func (l *runnerLauncher) start(ctx context.Context) {
	l.baseCtx = ctx
	// Before the first settle: its Expire would call lost every run whose lease ran out while no
	// daemon answered.
	l.adoptRunning(ctx)
	go func() {
		l.cancelOrphanTurns(l.baseCtx)
		t := time.NewTicker(settleEvery)
		defer t.Stop()
		for {
			select {
			case <-l.baseCtx.Done():
				return
			case <-t.C:
				l.settle(l.baseCtx)
			}
		}
	}()
}

// runnerFor builds the job's projects for the machine the dispatcher placed the run on. A task may
// have no project (a machine profile alone sends it). A project kept by Thulu travels by the daemon's
// /git/; one on a machine must be on this machine.
func (l *runnerLauncher) runnerFor(ctx context.Context, t *task.Task, runnerID string, readOnly bool) ([]runner.JobProject, error) {
	srcs, err := task.CodeSources(ctx, t, l.projects, readOnly)
	if err != nil {
		return nil, err
	}
	out := make([]runner.JobProject, 0, len(srcs))
	for _, src := range srcs {
		p := src.Project
		jp := runner.JobProject{ID: p.ID, Name: src.Name, DefaultBranch: p.DefaultBranch, Branch: src.Branch,
			GatesProfile: p.GatesProfile}
		switch {
		case !src.OnMachine():
			jp.Source = runner.SourceDaemon
		case p.RunnerID != runnerID:
			return nil, apperr.Invalid("api.runners.errors.task_mixes_runners")
		default:
			jp.RepoURL, jp.Path, jp.Push = p.RepoURL, p.RunnerPath, !p.CodeOnRunner()
		}
		out = append(out, jp)
	}
	return out, nil
}

func (l *runnerLauncher) FindAgentType(workspaceID, name string) *types.AgentType {
	return types.FindAgent(l.agents(workspaceID), name)
}

func (l *runnerLauncher) LaunchForStage(
	ctx context.Context,
	t *task.Task,
	stage task.Stage,
	agentType *types.AgentType,
	env map[string]string,
	_ map[string]string,
	_ map[string]string,
	workspaceReadOnly bool,
) (string, string, string, error) {
	if env == nil {
		env = map[string]string{}
	}
	env["STAGE"] = string(stage)
	if env[task.EnvPersonalLoginVolume] != "" {
		return "", "", "", apperr.Precondition("api.engine_logins.errors.unavailable")
	}
	if err := machineEngine(env); err != nil {
		return "", "", "", err
	}
	instanceID := env["INSTANCE_ID"]
	if instanceID == "" {
		return "", "", "", apperr.Invalid("api.launcher.errors.missing_instance_id")
	}
	runnerID := env[runner.RunnerIDEnv]
	if runnerID == "" {
		return "", "", "", apperr.Invalid("api.runners.errors.task_without_runner")
	}
	projects, err := l.runnerFor(ctx, t, runnerID, workspaceReadOnly)
	if err != nil {
		return "", "", "", err
	}
	r, err := l.liveRunner(ctx, t.WorkspaceID, runnerID)
	if err != nil {
		return "", "", "", err
	}
	// A runner without the steer loop still takes the run; its messages queue for the next one.
	// Disarming here also disarms the dispatcher, which reads this same map.
	if r.Protocol < runner.ProtocolSteer {
		delete(env, runner.SteerEnabledEnv)
	}
	spec := runner.JobSpec{
		InstanceID: instanceID, TaskID: t.ID, Stage: string(stage), AgentName: agentType.Name,
		LaunchKind: env[task.EnvLaunchKind], ReadOnly: workspaceReadOnly,
		Env: copyEnv(env), Projects: projects, ResultRef: project.ResultBranchName(t.SandboxRootID()),
	}
	// An audit or a mention reads the code without writing it: nothing to lock or check.
	if l.testLock != nil && !workspaceReadOnly {
		spec.TestLock = l.testLock(ctx, t, stage)
	}
	if l.runDeadline != nil {
		spec.DeadlineSeconds = int(l.runDeadline(t.WorkspaceID) / time.Second)
	}
	job := &runner.Job{
		RunnerID: runnerID, WorkspaceID: t.WorkspaceID, TaskID: t.ID,
		InstanceID: instanceID, AgentName: agentType.Name, AgentType: agentType.Type,
		Engine: env["ENGINE"], Stage: string(stage), LaunchKind: env[task.EnvLaunchKind], Spec: spec,
	}
	if err := l.enqueue(ctx, r, job); err != nil {
		return "", "", "", err
	}
	outputDir := runOutputDir(l.runsDir, instanceID)
	if outputDir != "" {
		logx.Warn(os.MkdirAll(outputDir, 0o750), "api.runners.log.output_dir", "dir", outputDir)
	}
	l.appendLog(job, []byte(i18n.Log("api.runners.log.queued", "runner", r.Name)+"\n"))
	return job.RunID(), instanceID, outputDir, nil
}

func machineEngine(env map[string]string) error {
	if eng := env["ENGINE"]; engine.MachineLoginOf(eng) == nil {
		return apperr.Precondition("api.runners.errors.engine_not_supported", "engine", eng)
	}
	return nil
}

// liveRunner is the machine a job may be queued for: the workspace's, and not revoked.
func (l *runnerLauncher) liveRunner(ctx context.Context, workspaceID, runnerID string) (*runner.Runner, error) {
	r, err := l.store.Get(ctx, workspaceID, runnerID)
	if err != nil {
		return nil, err
	}
	if r.RevokedAt != nil {
		return nil, apperr.Precondition("api.runners.errors.runner_revoked", "runner", r.Name)
	}
	return r, nil
}

// Checked here, not when placing: only the finished job knows what it needs. Every job passes here,
// so this is also where a key is kept off the machine, whatever placed the run.
func (l *runnerLauncher) enqueue(ctx context.Context, r *runner.Runner, job *runner.Job) error {
	if need := job.Spec.MinProtocol(); r.Protocol < need {
		return apperr.Precondition("api.runners.errors.runner_outdated", "runner", r.Name, "got", r.Protocol, "want", need)
	}
	task.ClearMachineCredentials(job.Spec.Env)
	job.ProjectID = claimProject(job.Spec.Projects)
	return l.store.Enqueue(ctx, job)
}

// claimProject is the project Claim matches against the runner's mapped ones: a machine project
// without a folder, which only the runner's config lays out. A Thulu project is never mapped, so it
// only names a job that needs no mapping (Claim takes a self-contained job whatever its project).
func claimProject(projects []runner.JobProject) string {
	for i := range projects {
		if p := &projects[i]; p.Path == "" && !p.FromDaemon() {
			return p.ID
		}
	}
	if len(projects) > 0 {
		return projects[0].ID
	}
	return ""
}

func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func (l *runnerLauncher) OnExit(_ context.Context, containerID string, cb func(int, string, string)) {
	l.mu.Lock()
	l.callbacks[runner.JobIDFromRun(containerID)] = cb
	l.mu.Unlock()
}

func (l *runnerLauncher) ContainerAlive(containerID string) bool {
	j, err := l.store.GetJob(context.Background(), runner.JobIDFromRun(containerID))
	return err == nil && !j.Done()
}

func (l *runnerLauncher) ScanExitedTaskAgents(ctx context.Context) ([]task.ExitedTaskAgent, error) {
	jobs, err := l.store.Settled(ctx)
	if err != nil {
		return nil, err
	}
	var out []task.ExitedTaskAgent
	for i := range jobs {
		j := &jobs[i]
		// A chat turn is no run of its task; settle reaps the ones nobody waits for.
		if j.ChatTurn() || l.hasCallback(j.ID) {
			continue
		}
		code := l.finalize(ctx, j)
		out = append(out, task.ExitedTaskAgent{
			ContainerID: j.RunID(), InstanceID: j.InstanceID, AgentType: j.AgentName,
			OutputDir: runOutputDir(l.runsDir, j.InstanceID), ExitCode: code,
			LaunchKind: task.ParseLaunchKind(j.LaunchKind),
		})
	}
	return out, nil
}

func (l *runnerLauncher) hasCallback(jobID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.callbacks[jobID]
	return ok
}

// adoptGrace is how long a runner has, after the daemon starts, to heartbeat a run a previous daemon
// handed it. Four heartbeats fit in it, so one lost to a slow restart does not cost the run.
const adoptGrace = runner.DefaultLease

// adoptRunning keeps the runs a previous daemon left on the machines. The runner goes on with the
// process through a restart and keeps calling; its next heartbeat renews the job here, and the run
// ends through Reconcile like any orphan (settle). A chat turn is canceled instead
// (cancelOrphanTurns): its waiter went with the old daemon.
func (l *runnerLauncher) adoptRunning(ctx context.Context) {
	n, err := l.store.AdoptRunning(ctx, adoptGrace)
	if err != nil {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.adopt_failed", "err", err))
		return
	}
	if n > 0 {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.adopted", "count", n, "grace", adoptGrace))
	}
}

// cancelOrphanTurns stops the chat turns a previous daemon left queued or running: their waiter went
// with it, and a machine that ran one would bill its login and write into the chat's session for an
// answer nobody reads. settle reaps them once they end.
func (l *runnerLauncher) cancelOrphanTurns(ctx context.Context) {
	ids, err := l.store.PendingChatTurns(ctx, l.started)
	if err != nil {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.orphan_turns_failed", "err", err))
		return
	}
	for _, id := range ids {
		logx.Warn(l.store.RequestCancel(ctx, id), "api.runners.log.turn_cancel_failed", "job", id)
	}
}

// A job created by this process gets its callback right after launch, so it must never go to
// Reconcile:
// its callback releases the run's slot and key. Only jobs created before start are orphans.
func (l *runnerLauncher) settle(ctx context.Context) {
	if n, err := l.store.Expire(ctx, l.claimTimeout); err != nil {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.expire_failed", "err", err))
	} else if n > 0 {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.expired", "count", n))
	}
	jobs, err := l.store.Settled(ctx)
	if err != nil {
		log.Printf("[runner] %s", i18n.Log("api.runners.log.settle_failed", "err", err))
		return
	}
	orphans := false
	for i := range jobs {
		j := &jobs[i]
		l.mu.Lock()
		cb, ok := l.callbacks[j.ID]
		delete(l.callbacks, j.ID)
		l.mu.Unlock()
		switch {
		case !ok && j.ChatTurn():
			// Its waiter went with a daemon restart.
			l.finalize(ctx, j)
			l.dropTurn(j)
			continue
		case !ok:
			if j.CreatedAt.Before(l.started) {
				orphans = true
			}
			continue
		}
		code := l.finalize(ctx, j)
		cb(code, runOutputDir(l.runsDir, j.InstanceID), "")
	}
	if orphans && l.reconcile != nil {
		l.reconcile(ctx)
	}
}

func (l *runnerLauncher) finalize(ctx context.Context, j *runner.Job) int {
	l.steer.end(j.ID)
	logx.Warn(l.store.MarkReaped(ctx, j.ID), "api.runners.log.reap_failed", "job", j.ID)
	code := runner.ExitLost
	if j.ExitCode != nil {
		code = *j.ExitCode
	}
	runnerName := j.RunnerID
	if r, err := l.store.Get(ctx, "", j.RunnerID); err == nil {
		runnerName = r.Name
	}
	if j.State == runner.StateLost {
		l.appendLog(j, []byte(i18n.Log("api.runners.log.lost", "runner", runnerName)+"\n"))
	}
	// A chat turn answers its chat alone: nothing reaches the task or the recorded runs.
	if j.ChatTurn() {
		return code
	}
	if l.report != nil && j.TaskID != "" {
		rep := task.RunnerReport{Runner: runnerName, Lost: j.State == runner.StateLost, Error: j.Result.Error,
			Warnings: j.Result.Warnings, RunID: j.RunID(), Instance: j.InstanceID, OutputsError: j.Result.OutputsError,
			TestFiles: j.Result.TestFiles, StopReason: j.Result.StopReason}
		for _, s := range j.Result.OutputsSkipped {
			rep.OutputsSkipped = append(rep.OutputsSkipped, s.Path)
		}
		for _, p := range j.Result.Pushed {
			rep.Pushed = append(rep.Pushed, task.RunnerPush{ProjectID: p.ProjectID, Branch: p.Branch, Commit: p.Commit})
		}
		l.report(ctx, j.TaskID, rep)
	}
	if l.logSink != nil {
		data, err := os.ReadFile(l.logPath(j))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logx.Warn(err, "api.runners.log.read_log_failed", "job", j.ID)
		}
		l.logSink(ctx, l.agentOf(j), string(data))
	}
	return code
}

func (l *runnerLauncher) agentOf(j *runner.Job) *agent.Agent {
	return &agent.Agent{
		ID: j.RunID(), Name: j.AgentName, Type: j.AgentType, Engine: j.Engine,
		TaskID: j.TaskID, ProjectID: j.ProjectID, WorkspaceID: j.WorkspaceID, Stage: j.Stage,
		StartedAt: j.CreatedAt, OutputDir: runOutputDir(l.runsDir, j.InstanceID),
	}
}

// Must match the name usage.Collector.RecordRun gives a run's log: the same file is the live log
// and the recorded one.
func (l *runnerLauncher) logPath(j *runner.Job) string {
	if l.logsDir == "" {
		return ""
	}
	return filepath.Join(l.logsDir, fmt.Sprintf("%s_%s_%s.log",
		j.AgentName, j.CreatedAt.UTC().Format("2006-01-02_15-04-05"), j.RunID()))
}

func (l *runnerLauncher) appendLog(j *runner.Job, chunk []byte) {
	p := l.logPath(j)
	if p == "" || len(chunk) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		logx.Warn(err, "api.runners.log.write_log_failed", "job", j.ID)
		return
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640) //nolint:gosec // G304: see logPath
	if err != nil {
		logx.Warn(err, "api.runners.log.write_log_failed", "job", j.ID)
		return
	}
	defer logx.Close(f, p)
	_, err = f.Write(chunk)
	logx.Warn(err, "api.runners.log.write_log_failed", "job", j.ID)
}

func (l *runnerLauncher) RunLogs(runID string) string {
	j, err := l.store.GetJob(context.Background(), runner.JobIDFromRun(runID))
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(l.logPath(j))
	if err != nil {
		return ""
	}
	return string(data)
}

func (l *runnerLauncher) KillRun(ctx context.Context, runID string) error {
	return l.store.RequestCancel(ctx, runner.JobIDFromRun(runID))
}
