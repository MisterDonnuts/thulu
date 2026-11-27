package task

import (
	"context"
	"errors"
	"log"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A person can move a task while its agent works: the move lands, the agent keeps going and leaves
// its work (commits, sections, reports), but nothing it decides applies to the stage the task is in
// now, and the new stage's agent waits for it. Before, the finished run was handled as if the task
// still sat in its stage: it launched that stage's auditor next to the new stage's run, pointed the
// task at the auditor (the chat lost the run that was working) and could block the task, mark it
// idle or failed, or relaunch the wrong agent.

// errRunBusy is a launch finding another stage's run still working on the task. A stage launch
// waits for that run (deferStageLaunch); any other launch is refused.
type errRunBusy struct {
	RunID string
	Stage Stage
}

func (e *errRunBusy) Error() string {
	return i18n.Log("task.errors.run_busy", "stage", e.Stage)
}

// liveRun names the run working on the task and the stage it works for, when one is alive. A run
// whose instance id does not parse (launched before the ids carried their stage) reads as no stage.
func (d *Dispatcher) liveRun(t *Task) (runID string, stage Stage, ok bool) {
	if t == nil || t.CurrentRunID == "" || !d.launcher.ContainerAlive(t.CurrentRunID) {
		return "", "", false
	}
	if _, s, parsed := parseTaskInstanceID(t.CurrentInstance); parsed {
		return t.CurrentRunID, s, true
	}
	if _, s, parsed := parseAuditInstanceID(t.CurrentInstance); parsed {
		return t.CurrentRunID, s, true
	}
	return t.CurrentRunID, "", true
}

// runOutlived reports whether the task left the stage the run was launched in, or moved on to
// another run, while the run worked. launchedIn is the task's stage when the run was launched,
// which a continuation does not share with the agent's own stage.
func runOutlived(t *Task, launchedIn Stage, runID string) bool {
	if t == nil {
		return false
	}
	if launchedIn != "" && t.Stage != launchedIn {
		return true
	}
	return t.CurrentRunID != "" && runID != "" && t.CurrentRunID != runID
}

type outlivedRun struct {
	Stage       Stage
	Agent       string
	Instance    string
	ContainerID string
	Exit        int
	Auditor     bool
}

// discardOutlivedRun leaves the trail of a run whose result no longer applies and starts the agent
// a move left waiting. The caller has already kept the run's work.
func (d *Dispatcher) discardOutlivedRun(ctx context.Context, t *Task, run outlivedRun) {
	key := "task.comments.run_outlived"
	if run.Auditor {
		key = "task.comments.auditor_outlived"
	}
	d.systemComment(ctx, t.ID, NoteOf(key,
		"agent", run.Agent, "stage", d.stageNote(t, run.Stage), "now", d.stageNote(t, t.Stage)))
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventRunOutlived, run.Instance, "octopus",
		i18n.Text("task.comments.run_outlived_event", "agent", run.Agent, "stage", run.Stage, "now", t.Stage),
		map[string]any{
			"agent":          run.Agent,
			"stage":          string(run.Stage),
			"task_stage":     string(t.Stage),
			"instance_id":    run.Instance,
			"container_id":   run.ContainerID,
			"current_run_id": t.CurrentRunID,
			"exit":           run.Exit,
			"auditor":        run.Auditor,
		}), "task.log.record_run_outlived", "task", t.ID)
	log.Printf("[task] %s", i18n.Log("task.log.run_outlived",
		"task", t.ID, "instance", run.Instance, "stage", run.Stage, "now", t.Stage))
	d.launchDeferred(ctx, t.ID)
}

// deferStageLaunch records that the stage's agent waits for the run still working on the task.
// The task keeps reading running: that run is on it, and the chat keeps showing it.
func (d *Dispatcher) deferStageLaunch(ctx context.Context, t *Task, stage Stage, agent string, busy *errRunBusy) {
	// Status first: whoever reads the deferral, in the timeline or in a test, finds the task running.
	if busy.RunID != "" {
		d.setStatus(ctx, t.ID, StatusRunning)
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.stage_launch_deferred",
		"agent", agent, "stage", d.stageNote(t, stage), "busy", d.stageNote(t, busy.Stage)))
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventStageLaunchDeferred, busy.RunID, "octopus",
		i18n.Text("task.comments.stage_launch_deferred_event", "agent", agent, "stage", stage),
		map[string]string{"agent": agent, "stage": string(stage), "waiting_for": busy.RunID, "busy_stage": string(busy.Stage)}),
		"task.log.record_launch_deferred", "task", t.ID)
}

// launchDeferred starts the agent of the task's current stage when a move left it waiting and
// nothing else works on the task any more.
func (d *Dispatcher) launchDeferred(ctx context.Context, taskID string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || t == nil {
		logx.Warn(err, "task.log.launch_deferred_get_task", "task", taskID)
		return
	}
	if !d.store.DeferredLaunchPending(ctx, t.ID, t.Stage) {
		return
	}
	if _, _, alive := d.liveRun(t); alive {
		return
	}
	d.setStatus(ctx, t.ID, StatusIdle)
	t.Status = StatusIdle
	d.launchAssignedAgent(ctx, t)
}

// busyRun is the run, other than one for stage, that keeps a launch for stage off the task.
func (d *Dispatcher) busyRun(t *Task, stage Stage) error {
	runID, runStage, alive := d.liveRun(t)
	if !alive || runStage == stage {
		return nil
	}
	return &errRunBusy{RunID: runID, Stage: runStage}
}

func asRunBusy(err error) (*errRunBusy, bool) {
	var busy *errRunBusy
	if errors.As(err, &busy) {
		return busy, true
	}
	return nil, false
}

func isRunBusy(err error) bool {
	var busy *errRunBusy
	return errors.As(err, &busy)
}

// heldClaim releases a launch claim taken inside a launch callback, once, whenever the launch ends.
type heldClaim struct{ release func() }

func (c *heldClaim) hold(release func()) { c.release = release }

func (c *heldClaim) done() {
	if c.release != nil {
		c.release()
		c.release = nil
	}
}

// outlivedAfterRun drops the rest of a finished run's handling when the task outlived it.
func (d *Dispatcher) outlivedAfterRun(ctx context.Context, taskID string, run outlivedRun, opts postRunOpts) bool {
	launchedIn := opts.TaskStage
	if launchedIn == "" {
		launchedIn = run.Stage
	}
	fresh, err := d.store.GetRow(ctx, taskID)
	if err != nil || !runOutlived(fresh, launchedIn, run.ContainerID) {
		return false
	}
	d.discardOutlivedRun(ctx, fresh, run)
	return true
}
