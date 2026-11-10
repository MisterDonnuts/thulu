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
)

func (l *routedLauncher) onRunner(ctx context.Context, t *task.Task) bool {
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
	if l.onRunner(ctx, t) {
		if l.runner == nil {
			// Runners are off (OCTOPUS_RUNNERS) and the project's code may exist only on its
			// runner: refuse, never fall back to base.
			return "", "", "", apperr.Precondition("api.runners.errors.unavailable")
		}
		return l.runner.LaunchForStage(ctx, t, stage, agentType, env, worktreePaths, stagePaths, workspaceReadOnly)
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

// Implements task.RunSteerer only when base does: the dispatcher type-asserts it to decide whether
// runs take mid-run messages.
func newRoutedLauncher(base task.AgentLauncher, projects *project.Store) (*routedLauncher, task.AgentLauncher) {
	l := &routedLauncher{base: base, projects: projects}
	if steerer, ok := base.(task.RunSteerer); ok {
		return l, &steeringRoutedLauncher{routedLauncher: l, steerer: steerer}
	}
	return l, l
}

type steeringRoutedLauncher struct {
	*routedLauncher
	steerer task.RunSteerer
}

func (l *steeringRoutedLauncher) SteerRun(ctx context.Context, runID string, payload []byte) error {
	if runner.IsRunnerRun(runID) {
		return apperr.Precondition("api.runners.errors.no_steering")
	}
	return l.steerer.SteerRun(ctx, runID, payload)
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
