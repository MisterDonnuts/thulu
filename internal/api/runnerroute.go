package api

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type routedLauncher struct {
	base     task.AgentLauncher
	projects *project.Store
	runner   *runnerLauncher
}

var (
	_ task.AgentLauncher = (*routedLauncher)(nil)
	_ task.RunLogReader  = (*routedLauncher)(nil)
	_ task.RunKiller     = (*routedLauncher)(nil)
	_ task.RunSteerer    = (*routedLauncher)(nil)
	_ task.SteerChecker  = (*routedLauncher)(nil)

	_ task.LiveRunScanner    = (*routedLauncher)(nil)
	_ task.ExitWatchCanceler = (*routedLauncher)(nil)
	_ task.LiveRunScanner    = (*taskLauncher)(nil)
)

// Guard only: the dispatcher already chose the machine. A task with code on a machine must never
// fall back to a container that has none of it.
func (l *routedLauncher) needsRunner(ctx context.Context, t *task.Task) bool {
	if l.projects == nil || t == nil {
		return false
	}
	for _, id := range t.ProjectIDs {
		if p, err := l.projects.Get(ctx, id); err == nil && p.RunsOnRunner() {
			return true
		}
	}
	return false
}

func (l *routedLauncher) side(runID string) task.AgentLauncher {
	if l.runner != nil && runner.IsRunnerRun(runID) {
		return l.runner
	}
	return l.base
}

func (l *routedLauncher) LaunchForStage(ctx context.Context, t *task.Task, stage task.Stage,
	agentType *types.AgentType, env map[string]string, worktreePaths, stagePaths map[string]string,
	workspaceReadOnly bool,
) (string, string, string, error) {
	if env[runner.RunnerIDEnv] != "" {
		if l.runner == nil {
			// Runners are off (OCTOPUS_RUNNERS): refuse, never fall back to base.
			return "", "", "", apperr.Precondition("api.runners.errors.unavailable")
		}
		return l.runner.LaunchForStage(ctx, t, stage, agentType, env, worktreePaths, stagePaths, workspaceReadOnly)
	}
	if l.needsRunner(ctx, t) {
		return "", "", "", apperr.Precondition("api.runners.errors.task_not_on_runner")
	}
	return l.base.LaunchForStage(ctx, t, stage, agentType, env, worktreePaths, stagePaths, workspaceReadOnly)
}

func (l *routedLauncher) OnExit(ctx context.Context, containerID string, cb func(int, string, string)) {
	l.side(containerID).OnExit(ctx, containerID, cb)
}

func (l *routedLauncher) FindAgentType(workspaceID, name string) *types.AgentType {
	return l.base.FindAgentType(workspaceID, name)
}

func (l *routedLauncher) ContainerAlive(containerID string) bool {
	return l.side(containerID).ContainerAlive(containerID)
}

func (l *routedLauncher) ScanExitedTaskAgents(ctx context.Context) ([]task.ExitedTaskAgent, error) {
	out, err := l.base.ScanExitedTaskAgents(ctx)
	if err != nil || l.runner == nil {
		return out, err
	}
	jobs, err := l.runner.ScanExitedTaskAgents(ctx)
	return append(out, jobs...), err
}

// Only the base launcher's runs: a machine's runs keep their own records and callbacks.
func (l *routedLauncher) ScanLiveTaskAgents(ctx context.Context) ([]task.LiveTaskAgent, error) {
	if s, ok := l.base.(task.LiveRunScanner); ok {
		return s.ScanLiveTaskAgents(ctx)
	}
	return nil, task.ErrLiveScanUnsupported
}

func (l *routedLauncher) CancelExitWatch(containerID string) {
	if c, ok := l.side(containerID).(task.ExitWatchCanceler); ok {
		c.CancelExitWatch(containerID)
	}
}

func newRoutedLauncher(base task.AgentLauncher, projects *project.Store) *routedLauncher {
	return &routedLauncher{base: base, projects: projects}
}

// CanSteer answers per run: a machine takes mid-run messages even when the base launcher cannot
// (Kubernetes has no pods/exec).
func (l *routedLauncher) CanSteer(env map[string]string) bool {
	if env[runner.RunnerIDEnv] != "" {
		return l.runner != nil
	}
	_, ok := l.base.(task.RunSteerer)
	return ok
}

func (l *routedLauncher) SteerRun(ctx context.Context, runID string, payload []byte) error {
	if steerer, ok := l.side(runID).(task.RunSteerer); ok {
		return steerer.SteerRun(ctx, runID, payload)
	}
	return apperr.NotImplemented("task.errors.launcher_cannot_steer")
}

func (l *routedLauncher) RunLogs(runID string) string {
	if reader, ok := l.side(runID).(task.RunLogReader); ok {
		return reader.RunLogs(runID)
	}
	return ""
}

func (l *routedLauncher) KillRun(ctx context.Context, runID string) error {
	if killer, ok := l.side(runID).(task.RunKiller); ok {
		return killer.KillRun(ctx, runID)
	}
	return apperr.Precondition("api.runners.errors.no_kill")
}
