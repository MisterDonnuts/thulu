package task

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// LiveTaskAgent is an agent run whose container (or pod) still runs. StartedAt comes from the
// runtime, so a deadline survives a daemon restart.
type LiveTaskAgent struct {
	ExitedTaskAgent
	StartedAt time.Time
}

// LiveRunScanner lists the runs still alive. A launcher that cannot list them leaves their exits to
// Reconcile.
type LiveRunScanner interface {
	ScanLiveTaskAgents(ctx context.Context) ([]LiveTaskAgent, error)
}

// ErrLiveScanUnsupported is what a wrapping launcher answers when the launcher it wraps cannot list
// live runs.
var ErrLiveScanUnsupported = errors.New("live run scan unsupported") //i18n:ignore sentinel compared with errors.Is, never shown

// ExitWatchCanceler drops an exit callback. A run that exited between the scan and the callback's
// registration lost its exit event; the callback would wait forever and hide the run from Reconcile.
type ExitWatchCanceler interface {
	CancelExitWatch(containerID string)
}

// SandboxReaper kills, inside a task's sandbox, what a run left running there: a dev server started
// in the background, a watcher, a copy of the repo being served. ReapAll also kills what carries no
// run at all, and runs only when no run of the sandbox's group is alive.
type SandboxReaper interface {
	ReapRun(ctx context.Context, ownerID, instanceID string) error
	ReapAll(ctx context.Context, ownerID string) error
}

// RearmLiveRuns gives the runs a restart found alive back what the restart took from them: the exit
// callback, which lived in memory, and the deadline, which was a timer. Without them a run that
// ends after the restart is never finished, and one that hangs runs forever.
func (d *Dispatcher) RearmLiveRuns(ctx context.Context) {
	scanner, ok := d.launcher.(LiveRunScanner)
	if !ok {
		return
	}
	live, err := scanner.ScanLiveTaskAgents(ctx)
	if errors.Is(err, ErrLiveScanUnsupported) {
		return
	}
	if err != nil {
		logx.Warn(err, "task.log.rearm_scan_failed")
		return
	}
	rearmed := 0
	for i := range live {
		if d.rearmRun(ctx, live[i]) {
			rearmed++
		}
	}
	log.Printf("[task] %s", i18n.Log("task.log.rearm_summary", "live", len(live), "rearmed", rearmed))
}

func (d *Dispatcher) rearmRun(ctx context.Context, run LiveTaskAgent) bool {
	taskID, ok := InstanceTaskID(run.InstanceID)
	if !ok {
		return false
	}
	t, err := d.store.GetRow(ctx, taskID)
	if err != nil {
		// A run of a deleted task: the janitor removes its container after the orphan grace.
		return false
	}
	stop := d.rearmDeadline(ctx, t.WorkspaceID, taskID, run)
	d.launcher.OnExit(ctx, run.ContainerID, func(exitCode int, outputDir, _ string) {
		stop()
		e := run.ExitedTaskAgent
		e.ExitCode = exitCode
		if outputDir != "" {
			e.OutputDir = outputDir
		}
		d.reconcileMu.Lock()
		defer d.reconcileMu.Unlock()
		d.reconcileExit(ctx, e)
	})
	if !d.launcher.ContainerAlive(run.ContainerID) {
		stop()
		if c, ok := d.launcher.(ExitWatchCanceler); ok {
			c.CancelExitWatch(run.ContainerID)
		}
		return false
	}
	log.Printf("[task] %s", i18n.Log("task.log.rearmed_run",
		"instance", run.InstanceID, "task", taskID, "container", run.ContainerID, "started", run.StartedAt.Format(time.RFC3339)))
	return true
}

// The deadline counts from the run's start, not from the restart. One already past kills the run
// now; its exit callback, armed just after, finishes it.
func (d *Dispatcher) rearmDeadline(ctx context.Context, workspaceID, taskID string, run LiveTaskAgent) (stop func()) {
	if d.runDeadline == nil || run.StartedAt.IsZero() {
		return func() {}
	}
	limit := d.runDeadline(workspaceID)
	if limit <= 0 {
		return func() {}
	}
	minutes := int(limit.Round(time.Minute) / time.Minute)
	expired := func() {
		killer, ok := d.launcher.(RunKiller)
		if !ok {
			log.Printf("[task] %s", i18n.Log("task.log.run_deadline_no_killer", "run", run.ContainerID, "task", taskID, "minutes", minutes))
			return
		}
		d.systemComment(ctx, taskID, NoteOf("task.comments.run_deadline_exceeded_rearmed",
			"agent", run.AgentType, "minutes", minutes, "instance", run.InstanceID))
		logx.Warn(killer.KillRun(ctx, run.ContainerID), "task.log.run_deadline_kill", "run", run.ContainerID, "task", taskID)
	}
	left := time.Until(run.StartedAt.Add(limit))
	if left <= 0 {
		go expired()
		return func() {}
	}
	timer := time.AfterFunc(left, expired)
	return func() { timer.Stop() }
}

// reapRunSandbox kills what the run left in its task's sandbox, and, once no run of the sandbox's
// group is alive, everything else the sandbox still runs outside init_run. It runs on every end of a
// run: its exit callback, a replay after a restart, a stop.
func (d *Dispatcher) reapRunSandbox(ctx context.Context, taskID, instanceID string) {
	reaper, ok := d.sandbox.(SandboxReaper)
	if !ok || reaper == nil {
		return
	}
	t, err := d.store.GetRow(ctx, taskID)
	if err != nil {
		// A deleted task's teardown destroyed its sandbox.
		return
	}
	owner := t.SandboxRootID()
	logx.Warn(reaper.ReapRun(ctx, owner, instanceID), "task.log.sandbox_reap_run_failed",
		"task", taskID, "instance", instanceID)
	if d.groupHasLiveRun(ctx, owner, instanceID) {
		return
	}
	logx.Warn(reaper.ReapAll(ctx, owner), "task.log.sandbox_reap_all_failed", "task", owner)
}

// groupHasLiveRun reports whether a run other than except still works on the sandbox group of
// owner. When it cannot tell, it says yes: killing a live run's server is worse than leaving a
// leftover to the next run's end.
func (d *Dispatcher) groupHasLiveRun(ctx context.Context, owner, except string) bool {
	if scanner, ok := d.launcher.(LiveRunScanner); ok {
		live, err := scanner.ScanLiveTaskAgents(ctx)
		if errors.Is(err, ErrLiveScanUnsupported) {
			return d.groupRunAlive(ctx, owner, except)
		}
		if err != nil {
			return true
		}
		for i := range live {
			if live[i].InstanceID == except {
				continue
			}
			taskID, ok := InstanceTaskID(live[i].InstanceID)
			if !ok {
				continue
			}
			if taskID == owner {
				return true
			}
			if row, err := d.store.GetRow(ctx, taskID); err == nil && row.SandboxRootID() == owner {
				return true
			}
		}
		return false
	}
	return d.groupRunAlive(ctx, owner, except)
}

// groupRunAlive asks about the current run of each task of the group. A mention's run is not one of
// them.
func (d *Dispatcher) groupRunAlive(ctx context.Context, owner, except string) bool {
	root, err := d.store.GetRow(ctx, owner)
	if err != nil {
		return true
	}
	children, err := d.store.ListChildren(ctx, owner)
	if err != nil {
		return true
	}
	group := make([]Task, 0, 1+len(children))
	group = append(group, *root)
	group = append(group, children...)
	for i := range group {
		if group[i].CurrentInstance == except || group[i].CurrentRunID == "" {
			continue
		}
		if d.launcher.ContainerAlive(group[i].CurrentRunID) {
			return true
		}
	}
	return false
}
