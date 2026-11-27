package task

import (
	"context"
	"log"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Entering a stage with release_resources frees the group's sandbox and worktrees before Done: the
// branch is merged by then and the release agent works against the remote. A subtask that gets
// there first waits for the rest of its group.
func (d *Dispatcher) releaseStageResources(ctx context.Context, t *Task) {
	if !d.releaseTaskResources(ctx, t) {
		return
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.resources_released", "stage", d.stageNote(t, t.Stage)))
	d.recordReleased(ctx, t)
}

// A task reached Done while the rest of its group waits in a release_resources stage: the last one
// in frees the group, as entering that stage would have.
func (d *Dispatcher) releaseGroupOnDone(ctx context.Context, t *Task) {
	if d.releaseTaskResources(ctx, t) {
		d.recordReleased(ctx, t)
	}
}

func (d *Dispatcher) recordReleased(ctx context.Context, t *Task) {
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventResourcesReleased, t.SandboxRootID(), "octopus",
		string(t.Stage), map[string]string{"stage": string(t.Stage), "group": t.SandboxRootID()}),
		"task.log.record_resources_released", "task", t.ID)
}

// Going back from a stage that freed the worktree would rebuild it from merged work; a broken
// deploy is a new task. True when the stage is such a one and the send-back was refused.
func (d *Dispatcher) refuseKickBackFromReleased(ctx context.Context, t *Task, req kickBackReq) bool {
	if info, _ := d.stagesFor(t).Get(req.From); !info.ReleaseResources {
		return false
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.kick_back_released",
		"stage", d.stageNote(t, req.From), "source", kickBackSource(req.Via)))
	d.block(ctx, t.ID, req.From, BlockInfo{Cause: BlockReleased, Agent: kickBackAgent(req)})
	return true
}

// The group's release already happened and nobody rebuilt a worktree since: the event names the
// group root whichever task triggered it, and releasing clears every task's worktree paths.
func (d *Dispatcher) groupAlreadyReleased(ctx context.Context, t *Task) bool {
	for _, p := range t.WorktreePaths {
		if p != "" {
			return false
		}
	}
	return d.store.hasEventForInstance(ctx, EventResourcesReleased, t.SandboxRootID())
}

// deferFreeForLiveRun keeps a group's sandbox and worktrees while one of its runs still works on
// them: freeing them under it left the agent on a deleted mount, and its next sandbox call built a
// sandbox nobody released. The run's exit frees them (freeAfterRun).
func (d *Dispatcher) deferFreeForLiveRun(ctx context.Context, t *Task) bool {
	root := t.SandboxRootID()
	if !d.groupHasLiveRun(ctx, root, "") {
		return false
	}
	d.freeDeferred.Store(root, struct{}{})
	log.Printf("[task] %s", i18n.Log("task.log.free_deferred_live_run", "task", t.ID, "group", root, "stage", t.Stage))
	return true
}

// freeAfterRun frees what deferFreeForLiveRun kept, once the last run of the group ends. It runs
// before the run is reported, so a release agent the report launches finds the group freed.
func (d *Dispatcher) freeAfterRun(ctx context.Context, taskID, instanceID string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || t == nil {
		return
	}
	root := t.SandboxRootID()
	if _, deferred := d.freeDeferred.Load(root); !deferred {
		return
	}
	if d.groupHasLiveRun(ctx, root, instanceID) {
		return
	}
	d.freeDeferred.Delete(root)
	info, _ := d.stagesFor(t).Get(t.Stage)
	switch {
	case info.Terminal:
		if !d.cleanupDoneTask(ctx, t) {
			d.releaseGroupOnDone(ctx, t)
		}
	case info.ReleaseResources:
		if !d.groupAlreadyReleased(ctx, t) {
			d.releaseStageResources(ctx, t)
		}
	}
}
