package task

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type queuedRun struct {
	Stage      string            `json:"stage"`
	Agent      string            `json:"agent"`
	LaunchKind string            `json:"launch_kind"`
	PrevStatus string            `json:"prev_status"`
	Turns      string            `json:"turns,omitempty"`
	Reason     string            `json:"reason"`
	ReasonI18n map[string]string `json:"reason_i18n,omitempty"`
	// Read by the run card, which links each value (room, install, workspace, line) to where it is
	// set.
	Wait string `json:"wait"`

	triggeredBy string
}

func waitKind(err error) string {
	switch {
	case errors.Is(err, ErrNoCapacity):
		return "room"
	case apperr.Code(err) == "task.errors.slots_full_install":
		return "install"
	case apperr.Code(err) == "task.errors.slots_full_workspace":
		return "workspace"
	}
	return "line"
}

type slotWait struct{ cancel context.CancelFunc }

func (d *Dispatcher) acquireStageSlot(ctx context.Context, t *Task, stage Stage, agent string, opts launchOpts, onWait func()) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &slotWait{cancel: cancel}
	if prev, loaded := d.slotWaits.Swap(t.ID, w); loaded {
		if pw, isWait := prev.(*slotWait); isWait {
			pw.cancel()
		}
	}
	defer d.slotWaits.CompareAndDelete(t.ID, w)
	waited := false
	err := d.slots.acquireOr(wctx, t.WorkspaceID, func(full slotsFull) {
		waited = true
		d.parkStage(ctx, t, stage, agent, opts, full.err())
		onWait()
	})
	if err != nil {
		if waited && ctx.Err() == nil {
			d.store.casStatus(ctx, t.ID, StatusQueued, StatusIdle)
			return errRoomDropped
		}
		return err
	}
	if waited && !d.stillQueued(ctx, t.ID, stage) {
		d.slots.release(t.WorkspaceID)
		return errRoomDropped
	}
	return nil
}

func (d *Dispatcher) dropSlotWait(taskID string) {
	if v, ok := d.slotWaits.Load(taskID); ok {
		if w, isWait := v.(*slotWait); isWait {
			w.cancel()
		}
	}
}

func (d *Dispatcher) waitingSlot(taskID string) bool {
	_, ok := d.slotWaits.Load(taskID)
	return ok
}

func (d *Dispatcher) CancelQueued(ctx context.Context, t *Task, by string) error {
	if t.Status != StatusQueued && !d.room.waitingTask(t.ID) && !d.waitingSlot(t.ID) {
		return apperr.Conflict("task.errors.not_queued")
	}
	q, ok := d.store.latestQueuedRun(ctx, t.ID)
	prev := StatusIdle
	if ok && q.PrevStatus != "" && RunStatus(q.PrevStatus) != StatusQueued {
		prev = RunStatus(q.PrevStatus)
	}
	// Status first: the wait's own cleanup only resets a task still queued.
	d.store.casStatus(ctx, t.ID, StatusQueued, prev)
	d.room.drop(t.ID)
	d.dropSlotWait(t.ID)
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventRunQueueCanceled, t.ID, by,
		NoteOf("task.comments.run_queue_canceled_event", "stage", d.stageNote(t, Stage(q.Stage)), "agent", q.Agent).Text(),
		map[string]string{"stage": q.Stage, "agent": q.Agent}), "task.log.record_run_queue_canceled", "task", t.ID)
	stage := d.stageNote(t, Stage(q.Stage))
	note := NoteOf("task.comments.run_queue_canceled", "who", by, "stage", stage, "agent", q.Agent)
	if anonymous(by) {
		note = NoteOf("task.comments.run_queue_canceled_anon", "stage", stage, "agent", q.Agent)
	}
	d.systemComment(ctx, t.ID, note)
	return nil
}

func (d *Dispatcher) parkStage(ctx context.Context, t *Task, stage Stage, agentName string, opts launchOpts, err error) {
	prev := t.Status
	if cur, gerr := d.store.Get(ctx, t.ID); gerr == nil {
		prev = cur.Status
	}
	if prev == StatusQueued {
		if q, ok := d.store.latestQueuedRun(ctx, t.ID); ok {
			prev = RunStatus(q.PrevStatus)
		}
	}
	d.setStatus(ctx, t.ID, StatusQueued)
	logx.Warn(d.store.recordRunEvent(ctx, t.ID, EventRunQueued, t.ID, "octopus",
		NoteOf("task.comments.run_queued_event", "stage", d.stageNote(t, stage), "agent", agentName).Text(),
		queuedRun{
			Stage: string(stage), Agent: agentName, LaunchKind: string(opts.kind()),
			PrevStatus: string(prev), Turns: opts.Turns, Reason: apperr.Text(err),
			ReasonI18n: localizedTexts(err), Wait: waitKind(err),
		}, TriggeredBy(ctx)), "task.log.record_run_queued", "task", t.ID)
	n := NoteOf("task.comments.run_queued", "stage", d.stageNote(t, stage), "agent", agentName, "reason", err)
	if waitKind(err) == "room" {
		n = n.Add("task.comments.run_queued_gives_up", "minutes", d.maxQueueMinutes())
	}
	d.systemComment(ctx, t.ID, n)
}

func (d *Dispatcher) stillQueued(ctx context.Context, taskID string, stage Stage) bool {
	t, err := d.store.Get(ctx, taskID)
	return err == nil && t.Stage == stage && t.Status == StatusQueued
}

// The queue lives in memory. Call once at startup, before ReconcileQuestions, which reads the
// statuses this restores.
func (d *Dispatcher) ResumeQueued(ctx context.Context) {
	ids, err := d.store.tasksWithStatus(ctx, StatusQueued)
	if err != nil {
		logx.Warn(err, "task.log.list_queued")
		return
	}
	for _, id := range ids {
		d.resumeQueued(ctx, id)
	}
}

func (d *Dispatcher) resumeQueued(ctx context.Context, taskID string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.resume_queued", "task", taskID)
		return
	}
	q, ok := d.store.latestQueuedRun(ctx, taskID)
	if ok && ParseLaunchKind(q.LaunchKind) == LaunchStage && Stage(q.Stage) == t.Stage {
		if err := d.RelaunchStage(WithTriggeredBy(ctx, q.triggeredBy), t, turnCount(q.Turns)); err != nil {
			logx.Warn(err, "task.log.resume_queued", "task", taskID)
			d.giveBackQueued(ctx, taskID, q, ok)
		}
		return
	}
	d.giveBackQueued(ctx, taskID, q, ok)
}

func turnCount(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (d *Dispatcher) giveBackQueued(ctx context.Context, taskID string, q queuedRun, ok bool) {
	prev := StatusIdle
	if ok && q.PrevStatus != "" && RunStatus(q.PrevStatus) != StatusQueued {
		prev = RunStatus(q.PrevStatus)
	}
	if d.store.casStatus(ctx, taskID, StatusQueued, prev) {
		d.systemComment(ctx, taskID, NoteOf("task.comments.run_queue_lost",
			"stage", d.stageNoteOf(ctx, taskID, Stage(q.Stage)), "agent", q.Agent))
	}
}

func (s *Store) latestQueuedRun(ctx context.Context, taskID string) (queuedRun, bool) {
	var payload, triggeredBy string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT payload, triggered_by FROM workflow_events WHERE task_id = ? AND kind = ? ORDER BY created_at DESC LIMIT 1`),
		taskID, EventRunQueued,
	).Scan(&payload, &triggeredBy)
	if err != nil {
		return queuedRun{}, false
	}
	var q queuedRun
	if json.Unmarshal([]byte(payload), &q) != nil {
		return queuedRun{}, false
	}
	q.triggeredBy = triggeredBy
	return q, true
}

func (s *Store) tasksWithStatus(ctx context.Context, status RunStatus) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM tasks WHERE status = ?`), string(status))
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_by_status", "status", status)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Wrap(err, "task.errors.list_by_status", "status", status)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) lastRunBudget(ctx context.Context, taskID string, stage Stage) int {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT payload FROM workflow_events WHERE task_id = ? AND kind = ? ORDER BY created_at DESC LIMIT 20`),
		taskID, EventAgentRunStarted)
	if err != nil {
		return 0
	}
	defer rows.Close()
	budget := 0
	for rows.Next() {
		var payload string
		if rows.Scan(&payload) != nil {
			return 0
		}
		var run struct {
			Stage    string `json:"stage"`
			MaxTurns string `json:"max_turns"`
		}
		if json.Unmarshal([]byte(payload), &run) != nil || run.Stage != string(stage) {
			continue
		}
		budget = turnCount(run.MaxTurns)
		break
	}
	if rows.Err() != nil {
		return 0
	}
	return budget
}
