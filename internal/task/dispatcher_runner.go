package task

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

const EnvStagePush = "OCTOPUS_STAGE_PUSH"

func (d *Dispatcher) runnerProject(ctx context.Context, projectID string) *project.Project {
	if d.projects == nil {
		return nil
	}
	p, err := d.projects.Get(ctx, projectID)
	if err != nil {
		return nil
	}
	return p
}

func (d *Dispatcher) projectCodeOnRunner(ctx context.Context, projectID string) bool {
	p := d.runnerProject(ctx, projectID)
	return p != nil && p.CodeOnRunner()
}

func (d *Dispatcher) projectRunsOnRunner(ctx context.Context, projectID string) bool {
	p := d.runnerProject(ctx, projectID)
	return p != nil && p.RunsOnRunner()
}

func (d *Dispatcher) taskRunsOnRunner(ctx context.Context, t *Task) bool {
	if t == nil {
		return false
	}
	for _, id := range t.ProjectIDs {
		if d.projectRunsOnRunner(ctx, id) {
			return true
		}
	}
	return false
}

func applyRunnerLogin(env map[string]string) {
	env[engine.LLMSourceEnv] = engine.LLMSourceRunnerLogin
}

func onRunnerLogin(env map[string]string) bool {
	return env[engine.LLMSourceEnv] == engine.LLMSourceRunnerLogin
}

type RunnerReport struct {
	Runner   string
	Pushed   []RunnerPush
	Lost     bool
	Error    string
	Warnings []string
}

type RunnerPush struct {
	ProjectID string
	Branch    string
	Commit    string
}

func (d *Dispatcher) RecordRunnerReport(ctx context.Context, taskID string, rep RunnerReport) {
	for _, p := range rep.Pushed {
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventBranchCreated, p.Branch,
			"octopus", i18n.Text("task.comments.runner_branch_pushed", "runner", rep.Runner),
			map[string]string{"project": p.ProjectID, "branch": p.Branch, "commit": p.Commit, "runner": rep.Runner}),
			"task.log.record_branch_pushed", "task", taskID)
	}
	if rep.Error != "" {
		d.systemComment(ctx, taskID, NoteOf("task.comments.runner_failed", "runner", rep.Runner, "err", rep.Error))
	}
	if rep.Lost {
		d.systemComment(ctx, taskID, NoteOf("task.comments.runner_lost", "runner", rep.Runner))
	}
	for _, w := range rep.Warnings {
		d.systemComment(ctx, taskID, NoteOf("task.comments.runner_warning", "runner", rep.Runner, "warning", w))
	}
}
