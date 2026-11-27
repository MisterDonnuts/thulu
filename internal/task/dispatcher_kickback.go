package task

import (
	"context"
	"fmt"
	"log"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A request to send a task back to an earlier stage, from whichever source decided it.
type kickBackReq struct {
	From, Target Stage
	// ViaKickBack (auditor), ViaChangesRequested or ViaVerdict; lands in the stage_entered payload.
	Via   string
	Actor string
	// The agent the auditor audited, when the auditor asked; names who stopped a refused send-back.
	Agent string
	RefID string
	// Shown in the limit comment so the person who unblocks sees why it came back; may be empty.
	Reason string
	// Posted only when the task actually moves.
	Notes []Note
	// Past the board's max_kick_backs: the return a removed skip asks for is not a send-back.
	Uncapped bool
}

// Where a send-back from `from` lands: the requested stage when it sits before `from`, else the
// nearest earlier "work" stage, else KickbackDefault when it is earlier, else the nearest earlier
// stage that is not the initial one. Never the stage itself, a later one, a terminal, a virtual
// stage or one that may be skipped. "" when nothing qualifies.
func kickBackTarget(stages stagepkg.Registry, from, requested Stage) Stage {
	info, ok := stages.Get(from)
	if !ok {
		return ""
	}
	behind := func(id Stage) bool {
		to, ok := stages.Get(id)
		return ok && !to.Virtual && !to.Terminal && len(to.SkipUnless) == 0 && to.Position < info.Position
	}
	if requested != "" && behind(requested) {
		return requested
	}
	list := stages.List()
	for i := len(list) - 1; i >= 0; i-- {
		if st := list[i]; !st.Initial && st.WipGroup == types.WipGroupWork && behind(st.ID) {
			return st.ID
		}
	}
	if def := stages.KickbackDefault(); def != "" && behind(def) {
		return def
	}
	for i := len(list) - 1; i >= 0; i-- {
		if st := list[i]; !st.Initial && behind(st.ID) {
			return st.ID
		}
	}
	return ""
}

// Whether the auditor may send a run back to its own stage: a fresh run of the same stage, with the
// auditor's notes in the discussion, counted as a return like any other. The audited run just ran
// there, so any stage on the board that is not terminal or virtual qualifies.
func rerunnable(stages stagepkg.Registry, id Stage) bool {
	info, ok := stages.Get(id)
	return ok && !info.Virtual && !info.Terminal
}

// 0 is no cap: boards that never set max_kick_backs keep today's behavior.
func (d *Dispatcher) maxKickBacks(t *Task) int {
	if t == nil {
		return 0
	}
	boardID := t.BoardID
	if boardID == "" {
		boardID = d.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	info, _ := stagepkg.BoardInfoOf(d.boards, boardID)
	return info.MaxKickBacks
}

// The one place a task moves backwards. Over the board's cap the task stays where it is, blocked,
// with a comment saying who tried to send it where and why.
func (d *Dispatcher) kickBack(ctx context.Context, t *Task, req kickBackReq) {
	// A person may have moved the task since the decision was read: neither the send-back nor a
	// refusal that blocks it may touch the task where they put it. The move below checks again.
	if cur, err := d.store.GetRow(ctx, t.ID); err != nil || cur.Stage != req.From {
		return
	}
	if d.refuseKickBackFromReleased(ctx, t, req) {
		return
	}
	if limit := d.maxKickBacks(t); limit > 0 && !req.Uncapped {
		if n := d.store.CountKickBacks(ctx, t.ID); n >= limit {
			d.kickBackRefused(ctx, t, req, n, limit)
			return
		}
	}
	target, idle := req.Target, StatusIdle
	updated, err := d.store.UpdateIfStage(ctx, t.ID, req.From, Patch{Stage: &target, Status: &idle, By: req.Actor})
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.kick_back_update_failed", "err", err))
		return
	}
	if d.onStatus != nil {
		d.onStatus(updated, idle)
	}
	for _, n := range req.Notes {
		d.systemComment(ctx, t.ID, n)
	}
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventStageEntered, req.RefID, req.Actor,
		fmt.Sprintf("%s → %s (%s)", req.From, req.Target, req.Via), //i18n:ignore timeline arrow
		map[string]string{"from": string(req.From), "to": string(req.Target), "via": req.Via}),
		"task.log.record_kick_back", "task", t.ID)
	d.stageMoved(updated, req.From)
	d.HandleStageEntered(ctx, updated, req.From)
}

// Localized per reader, like stageNote.
func kickBackSource(via string) any {
	switch via {
	case ViaChangesRequested:
		return apperr.New("task.labels.kick_back_via_changes_requested")
	case ViaVerdict:
		return apperr.New("task.labels.kick_back_via_verdict")
	}
	return apperr.New("task.labels.kick_back_via_auditor")
}

func (d *Dispatcher) kickBackRefused(ctx context.Context, t *Task, req kickBackReq, count, limit int) {
	reason := ""
	if q := quoteNote(req.Reason); q != "" {
		reason = "\n\n" + q
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.kick_back_limit",
		"from", d.stageNote(t, req.From), "stage", d.stageNote(t, req.Target),
		"source", kickBackSource(req.Via),
		"count", count, "max", limit, "reason", reason))
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventKickBackLimit, req.RefID, req.Actor,
		fmt.Sprintf("%s ↛ %s (%d/%d)", req.From, req.Target, count, limit), //i18n:ignore timeline arrow
		map[string]any{"from": string(req.From), "to": string(req.Target), "via": req.Via,
			"count": count, "max": limit}),
		"task.log.record_kick_back_limit", "task", t.ID)
	d.block(ctx, t.ID, req.From, BlockInfo{Cause: BlockKickBackLimit, Agent: kickBackAgent(req)})
}
