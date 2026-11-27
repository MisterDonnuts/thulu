package task

import (
	"context"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// ErrRunInFlight is a fenced move finding a run starting or running on the task. The task stays
// where it is.
var ErrRunInFlight error = apperr.Conflict("task.errors.run_in_flight")

// errLaunchStale is a launch finding the task gone from the stage it was decided in. It is dropped
// without a trace: whoever moved the task owns what happens next.
var errLaunchStale error = apperr.New("task.errors.launch_stale")

// launchFence orders stage launches against movers outside the dispatcher. A merge used to read the
// task idle while a launch started a container for the stage the task was leaving: the task landed
// in the next stage with the old stage's agent still working on it, next to the new stage's run.
//
// A launch claims the task right before its container starts, and only while the task still sits
// where the launch was decided; the claim lasts until the run is recorded running. A fenced move
// refuses a claimed task and one recorded running. Both checks run under one lock, so either the
// move lands first and the launch sees the task gone, or the launch claims first and the move waits.
type launchFence struct {
	mu     sync.Mutex
	claims map[string]int
	// The stage the latest claim on each task launches for, so a launch for another stage sees a
	// run that is starting but not recorded yet.
	claimStage map[string]Stage
}

// claimLaunch claims t for a launch while it is still in t.Stage, the stage it was in when the
// launch was decided (a continuation may run another stage's agent). Call release once the run is
// recorded running or the launch gives up.
//
// A run of another stage still working on the task, or still starting, refuses the claim with
// errRunBusy: a person moved the task while it worked, and two agents must never share the task's
// branch.
func (d *Dispatcher) claimLaunch(ctx context.Context, t *Task) (release func(), err error) {
	f := &d.fence
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := d.store.GetRow(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if cur.Stage != t.Stage {
		return nil, errLaunchStale
	}
	if f.claims[t.ID] > 0 && f.claimStage[t.ID] != t.Stage {
		return nil, &errRunBusy{Stage: f.claimStage[t.ID]}
	}
	if err := d.busyRun(cur, t.Stage); err != nil {
		return nil, err
	}
	if f.claims == nil {
		f.claims = map[string]int{}
		f.claimStage = map[string]Stage{}
	}
	f.claims[t.ID]++
	f.claimStage[t.ID] = t.Stage
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.claims[t.ID]--
			if f.claims[t.ID] <= 0 {
				delete(f.claims, t.ID)
				delete(f.claimStage, t.ID)
			}
		})
	}, nil
}

// RunInFlight reports whether a launch holds a claim on the task: its container is starting and the
// task does not read running yet.
func (d *Dispatcher) RunInFlight(taskID string) bool {
	d.fence.mu.Lock()
	defer d.fence.mu.Unlock()
	return d.fence.claims[taskID] > 0
}

// UpdateIfStageFenced is UpdateIfStage for a mover outside the dispatcher that must not move a task
// a run is starting or running on: that task returns ErrRunInFlight and stays where it is.
func (d *Dispatcher) UpdateIfStageFenced(ctx context.Context, id string, stage Stage, p Patch) (*Task, error) {
	f := &d.fence
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claims[id] > 0 {
		return nil, ErrRunInFlight
	}
	// Read under the lock: the only write of running is recordRunStarted, made while its claim
	// holds, so the status cannot turn running between this read and the update.
	cur, err := d.store.GetRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Status == StatusRunning {
		return nil, ErrRunInFlight
	}
	return d.store.UpdateIfStage(ctx, id, stage, p)
}
