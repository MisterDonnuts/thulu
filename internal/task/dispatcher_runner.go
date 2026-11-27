package task

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
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

type RunnerReport struct {
	Runner string
	// The run the job carried (runner.Job.RunID) and its instance, for the events the report
	// records (runner_warning).
	RunID    string
	Instance string
	Pushed   []RunnerPush
	Lost     bool
	Error    string
	Warnings []string
	// Output files that did not fit the upload; the rest reached the daemon.
	OutputsSkipped []string
	// Why the outputs upload failed: none of the run's files reached the daemon.
	OutputsError string
	// What the runner read of the locked tests (JobSpec.TestLock); the run's post-run takes it.
	TestFiles []runner.TestFiles
	// Why the runner stopped the run on its own (runner.StopDeadline, runner.StopDaemonLost).
	StopReason string
}

type RunnerPush struct {
	ProjectID string
	Branch    string
	Commit    string
}

func (d *Dispatcher) RecordRunnerReport(ctx context.Context, taskID string, rep RunnerReport) {
	for _, p := range rep.Pushed {
		// The result branch is the daemon's inbox, integrated and deleted after the run; the task's
		// branch is what the person sees.
		if strings.HasPrefix(p.Branch, project.ResultBranchName("")) {
			continue
		}
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
	switch rep.StopReason {
	case runner.StopDeadline:
		d.systemComment(ctx, taskID, NoteOf("task.comments.runner_stopped_deadline", "runner", rep.Runner))
	case runner.StopDaemonLost:
		d.systemComment(ctx, taskID, NoteOf("task.comments.runner_stopped_daemon_lost", "runner", rep.Runner))
	}
	// A warning did not stop the run: it belongs on the run card, not in the discussion.
	for _, w := range rep.Warnings {
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventRunnerWarning, rep.RunID, "runner:"+rep.Runner,
			NoteOf("task.comments.runner_warning", "runner", rep.Runner, "warning", w).Text(),
			map[string]string{"run_id": rep.RunID, "message": w}),
			"task.log.record_runner_warning", "task", taskID)
	}
	d.recordOutputsDelivery(ctx, taskID, rep)
	if rep.Instance != "" && len(rep.TestFiles) > 0 {
		d.runnerTests.put(rep.Instance, rep.TestFiles)
	}
}

// Outputs delivery values of a runner_warning payload.
const (
	OutputsPartial = "partial"
	OutputsFailed  = "failed"
)

// A run whose outputs did not all arrive leaves a runner_warning: the run card shows it, and a
// missing verdict.md then reads as lost in delivery instead of never written.
func (d *Dispatcher) recordOutputsDelivery(ctx context.Context, taskID string, rep RunnerReport) {
	var delivery, msg string
	switch {
	case rep.OutputsError != "":
		delivery = OutputsFailed
		msg = i18n.Text("task.comments.runner_outputs_failed", "runner", rep.Runner, "err", rep.OutputsError)
	case len(rep.OutputsSkipped) > 0:
		delivery = OutputsPartial
		msg = i18n.Text("task.comments.runner_outputs_skipped", "runner", rep.Runner,
			"count", len(rep.OutputsSkipped), "files", strings.Join(rep.OutputsSkipped, ", "))
	default:
		return
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventRunnerWarning, rep.RunID, "octopus", msg,
		map[string]any{"run_id": rep.RunID, "instance_id": rep.Instance, "message": msg,
			"outputs": delivery, "skipped": rep.OutputsSkipped}),
		"task.log.record_runner_warning", "task", taskID)
}
