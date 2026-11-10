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

	claimTimeout time.Duration
	started      time.Time

	mu        sync.Mutex
	callbacks map[string]func(int, string, string)
}

var _ task.AgentLauncher = (*runnerLauncher)(nil)

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
		callbacks: map[string]func(int, string, string){},
	}
}

const settleEvery = 2 * time.Second

func (l *runnerLauncher) start(ctx context.Context) {
	l.baseCtx = ctx
	go func() {
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

func (l *runnerLauncher) runnerFor(ctx context.Context, t *task.Task) (string, []runner.JobProject, error) {
	runnerID := ""
	var out []runner.JobProject
	branchOwner := t.SandboxRootID()
	for _, id := range t.ProjectIDs {
		p, err := l.projects.Get(ctx, id)
		if err != nil {
			return "", nil, err
		}
		if !p.RunsOnRunner() {
			return "", nil, apperr.Invalid("api.runners.errors.task_mixes_projects", "project", p.Name)
		}
		if runnerID != "" && runnerID != p.RunnerID {
			return "", nil, apperr.Invalid("api.runners.errors.task_mixes_runners")
		}
		runnerID = p.RunnerID
		out = append(out, runner.JobProject{
			ID: p.ID, Name: filepath.Base(p.Path), DefaultBranch: p.DefaultBranch,
			Branch: project.TaskBranchName(branchOwner), RepoURL: p.RepoURL,
			Push: !p.CodeOnRunner(),
		})
	}
	if runnerID == "" {
		return "", nil, apperr.Invalid("api.runners.errors.task_without_runner")
	}
	return runnerID, out, nil
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
	if eng := env["ENGINE"]; eng != "" && eng != engine.DefaultName {
		return "", "", "", apperr.Precondition("api.runners.errors.engine_not_supported", "engine", eng)
	}
	instanceID := env["INSTANCE_ID"]
	if instanceID == "" {
		return "", "", "", apperr.Invalid("api.launcher.errors.missing_instance_id")
	}
	runnerID, projects, err := l.runnerFor(ctx, t)
	if err != nil {
		return "", "", "", err
	}
	r, err := l.store.Get(ctx, t.WorkspaceID, runnerID)
	if err != nil {
		return "", "", "", err
	}
	if r.RevokedAt != nil {
		return "", "", "", apperr.Precondition("api.runners.errors.runner_revoked", "runner", r.Name)
	}
	// Runners have no channel into the process; steering off makes mid-run messages queue for the
	// next run.
	env["STEER_ENABLED"] = "0"
	spec := runner.JobSpec{
		InstanceID: instanceID, TaskID: t.ID, Stage: string(stage), AgentName: agentType.Name,
		LaunchKind: env[task.EnvLaunchKind], ReadOnly: workspaceReadOnly,
		Env: copyEnv(env), Projects: projects,
	}
	projectID := ""
	if len(projects) > 0 {
		projectID = projects[0].ID
	}
	job := &runner.Job{
		RunnerID: runnerID, WorkspaceID: t.WorkspaceID, TaskID: t.ID, ProjectID: projectID,
		InstanceID: instanceID, AgentName: agentType.Name, AgentType: agentType.Type,
		Engine: env["ENGINE"], Stage: string(stage), LaunchKind: env[task.EnvLaunchKind], Spec: spec,
	}
	if err := l.store.Enqueue(ctx, job); err != nil {
		return "", "", "", err
	}
	outputDir := runOutputDir(l.runsDir, instanceID)
	if outputDir != "" {
		logx.Warn(os.MkdirAll(outputDir, 0o750), "api.runners.log.output_dir", "dir", outputDir)
	}
	l.appendLog(job, []byte(i18n.Log("api.runners.log.queued", "runner", r.Name)+"\n"))
	return job.RunID(), instanceID, outputDir, nil
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
		if l.hasCallback(j.ID) {
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
		if !ok {
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
	if l.report != nil && j.TaskID != "" {
		rep := task.RunnerReport{Runner: runnerName, Lost: j.State == runner.StateLost, Error: j.Result.Error,
			Warnings: j.Result.Warnings}
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
