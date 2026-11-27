package task

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
)

// The stage auto-advance moves t to from `from`, passing over the conditional stages (skip_unless)
// whose values t's flow flags lack and the columns t skips (stage_skips), plus the ones it passed.
// Never enters them, so a skipped stage launches nothing and creates no worktree. "" when from is
// terminal or last.
func nextStageFor(stages stagepkg.Registry, t *Task, from Stage) (next Stage, skipped []Stage) {
	var flags []string
	if t != nil {
		flags = t.FlowFlags
	}
	cur := from
	for {
		cand := stages.Next(cur)
		if cand == "" {
			return "", skipped
		}
		info, _ := stages.Get(cand)
		if info.Terminal {
			return cand, skipped
		}
		if _, byTask := activeSkip(t, info); !byTask && stageRunsFor(info, flags) {
			return cand, skipped
		}
		skipped = append(skipped, cand)
		cur = cand
	}
}

func (d *Dispatcher) nextStageFor(t *Task, from Stage) (Stage, []Stage) {
	return nextStageFor(d.stagesFor(t), t, from)
}

// One event and one note per stage passed over, so the timeline says why QA never ran: the task's
// own skip with its reason, else the surface the column waits on.
func (d *Dispatcher) recordSkipped(ctx context.Context, t *Task, skipped []Stage) {
	if t == nil {
		return
	}
	stages := d.stagesFor(t)
	for _, id := range skipped {
		info, _ := stages.Get(id)
		if sk, ok := activeSkip(t, info); ok {
			d.systemComment(ctx, t.ID, NoteOf("task.comments.stage_skipped_task",
				"stage", d.stageNote(t, id), "reason", quoteNote(sk.Reason), "by", personArg(sk.By)))
			logx.Warn(d.store.RecordEvent(ctx, t.ID, EventStageSkipped, "", "octopus",
				string(id),
				map[string]any{"stage": string(id), "task_skip": true, "reason": sk.Reason, "by": sk.By}),
				"task.log.record_stage_skipped", "task", t.ID)
			continue
		}
		required := strings.Join(info.SkipUnless, ", ")
		d.systemComment(ctx, t.ID, NoteOf("task.comments.stage_skipped",
			"stage", d.stageNote(t, id), "required", required, "flags", flowFlagsText(t.FlowFlags)))
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventStageSkipped, "", "octopus",
			string(id),
			map[string]any{"stage": string(id), "required": info.SkipUnless, "task_flags": t.FlowFlags}),
			"task.log.record_stage_skipped", "task", t.ID)
	}
}

// What taking a skip away did.
type SkipRemoval struct {
	Removed StageSkip `json:"removed"`
	// The task had already passed the column.
	Passed bool `json:"passed"`
	// The task went back to the column.
	MovedBack bool `json:"moved_back"`
	// The task goes back when the run that asked ends.
	Deferred bool  `json:"deferred"`
	Task     *Task `json:"-"`
}

// RemoveStageSkip takes the task's skip of stage away. A column ahead simply runs when the task
// reaches it. A column the task already passed sends the task back there, the way a send-back
// does, and it goes forward again from there; when the agent asking is the one running the task's
// stage, the move waits until its run ends, so the task does not leave under a live run.
func (d *Dispatcher) RemoveStageSkip(ctx context.Context, t *Task, stage Stage, a SkipActor, reason string) (SkipRemoval, error) {
	cur, err := d.store.GetRow(ctx, t.ID)
	if err != nil {
		return SkipRemoval{}, err
	}
	if refused := MayRemoveSkips(cur, a); refused != nil {
		return SkipRemoval{}, refused
	}
	deferred := a.Agent && a.RunTask == cur.ID && a.RunStage == cur.Stage && cur.Status == StatusRunning
	removed, passed, after, err := d.store.RemoveStageSkip(ctx, cur.ID, stage, a.By, reason, SkipCauseRemoved, deferred)
	if err != nil {
		return SkipRemoval{}, err
	}
	out := SkipRemoval{Removed: removed, Passed: passed, Deferred: passed && deferred, Task: after}
	switch {
	case !passed:
	case out.Deferred:
		d.systemComment(ctx, cur.ID, NoteOf("task.comments.skip_return_pending",
			"who", personArg(a.By), "stage", d.stageNote(after, stage)))
	default:
		d.kickBack(ctx, after, kickBackReq{From: after.Stage, Target: stage, Via: ViaSkipRemoved, Actor: a.By,
			Reason: reason, Uncapped: true,
			Notes: []Note{NoteOf("task.comments.skip_removed_back", "who", personArg(a.By),
				"stage", d.stageNote(after, stage), "reason", quoteNote(reason))}})
		if moved, err := d.store.GetRow(ctx, cur.ID); err == nil {
			out.Task, out.MovedBack = moved, moved.Stage == stage
		}
	}
	return out, nil
}

// The earliest column a skip removal left waiting for the end of stage's run, still on the board
// and before stage; "" when none.
func (d *Dispatcher) skipReturnTarget(ctx context.Context, t *Task, stage Stage) Stage {
	if t == nil {
		return ""
	}
	stages := d.stagesFor(t)
	here, ok := stages.Get(stage)
	if !ok {
		return ""
	}
	best, bestPos := Stage(""), here.Position
	for _, id := range d.store.PendingSkipReturns(ctx, t.ID, stage) {
		if info, ok := stages.Get(id); ok && !info.Virtual && !info.Terminal && info.Position < bestPos {
			best, bestPos = id, info.Position
		}
	}
	return best
}

// returnForSkip moves the task back to a column whose skip was removed during the run that just
// ended cleanly; true when it did.
func (d *Dispatcher) returnForSkip(ctx context.Context, t *Task, stage Stage) bool {
	target := d.skipReturnTarget(ctx, t, stage)
	if target == "" {
		return false
	}
	d.kickBack(ctx, t, kickBackReq{From: stage, Target: target, Via: ViaSkipRemoved, Actor: "octopus", Uncapped: true,
		Notes: []Note{NoteOf("task.comments.skip_return_now", "stage", d.stageNote(t, target))}})
	return true
}

// Whether a comes before b on t's board.
func (d *Dispatcher) stageBefore(t *Task, a, b Stage) bool {
	stages := d.stagesFor(t)
	ia, okA := stages.Get(a)
	ib, okB := stages.Get(b)
	return okA && okB && ia.Position < ib.Position
}
