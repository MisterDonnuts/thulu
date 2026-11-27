package task

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type GateCause string

const (
	GateManual         GateCause = "manual"
	GateAutoAdvance    GateCause = "auto_advance"
	GateAutomation     GateCause = "automation"
	GateCreate         GateCause = "create"
	GateKickBack       GateCause = "kick_back"
	GateStageDeleted   GateCause = "stage_deleted"
	GateBoardDeleted   GateCause = "board_deleted"
	GateTypeRelocation GateCause = "type_relocation"
	// The person already decided on GitHub: the move passes and waives what it skips, with the
	// merger as author, so the approval does not stay pending for good.
	GateMerge GateCause = "merge"
	// A release agent moving a batch: the gate holds, as for an automation.
	GateRelease GateCause = "release"
)

func (c GateCause) Administrative() bool {
	return c == GateStageDeleted || c == GateBoardDeleted || c == GateTypeRelocation || c == GateMerge
}

// For an administrative move Blocking lists what the move skipped.
// A required approval not opened yet has an empty ID; StageExitGate relies on that.
// When Checks is set, the approved approvals in Blocking wait for the required checks of those pull
// requests.
type GateVerdict struct {
	Passed   bool
	Blocking []Approval
	Checks   []ChecksHold
}

// OnlyChecks says whether the move waits for nothing but the required checks: every approval it
// lists is already decided.
func (v GateVerdict) OnlyChecks() bool {
	return len(v.Checks) > 0 && !slices.ContainsFunc(v.Blocking, func(a Approval) bool { return !a.Status.Settled() })
}

func (v GateVerdict) Labels() string {
	labels := make([]string, len(v.Blocking))
	for i := range v.Blocking {
		labels[i] = v.Blocking[i].Label
	}
	return strings.Join(labels, ", ")
}

// A target off this board counts as forward, or moving to another board would skip the gate.
func isForward(from, to types.StageInfo, toKnown bool) bool {
	if !toKnown || to.Virtual {
		return true
	}
	return to.Position > from.Position
}

const OffBoard Stage = ""

func (s *Store) StageExitCheck(ctx context.Context, t *Task, from, to Stage, cause GateCause) (GateVerdict, error) {
	pass := GateVerdict{Passed: true}
	if t == nil || from == to || cause == GateKickBack {
		return pass, nil
	}
	reg := s.board(t.BoardID)
	fromInfo, ok := reg.Get(from)
	if !ok {
		return pass, nil
	}
	toInfo, toKnown := reg.Get(to)
	if !isForward(fromInfo, toInfo, toKnown) {
		return pass, nil
	}
	var list []Approval
	if t.ID != "" {
		var err error
		if list, err = s.ListApprovals(ctx, t.ID); err != nil {
			return GateVerdict{}, err
		}
	}
	skipped := jumpedOver(reg, fromInfo, toInfo, toKnown)
	blocking := holdingApprovals(t, fromInfo, list, skipped)
	if cause.Administrative() {
		return GateVerdict{Passed: true, Blocking: blocking}, nil
	}
	if len(blocking) > 0 {
		return GateVerdict{Blocking: blocking}, nil
	}
	// Only asked once nothing else holds: it reads GitHub.
	if held, holds := s.checksHeld(ctx, t, reg, fromInfo, list, skipped); len(held) > 0 {
		return GateVerdict{Blocking: held, Checks: holds}, nil
	}
	return pass, nil
}

// Already-opened approvals of jumped stages hold the move, or stepping back then jumping forward
// skips their gate.
// Requirements a jumped stage never opened do not hold.
func jumpedOver(reg stagepkg.Registry, from, to types.StageInfo, toKnown bool) map[Stage]bool {
	out := map[Stage]bool{}
	stages := reg.List()
	for i := range stages {
		st := &stages[i]
		if st.Position > from.Position && (!toKnown || to.Virtual || st.Position < to.Position) {
			out[st.ID] = true
		}
	}
	return out
}

func holdingApprovals(t *Task, from types.StageInfo, list []Approval, skipped map[Stage]bool) []Approval {
	var out []Approval
	opened := map[string]bool{}
	for i := range list {
		a := list[i]
		if a.Stage == from.ID && a.Source == ApprovalFromStage {
			opened[a.Key] = true
		}
		if a.Status.Settled() {
			continue
		}
		if a.Stage == from.ID || from.BlockedByFlags || (a.Stage != "" && skipped[a.Stage]) {
			out = append(out, a)
		}
	}
	for _, req := range from.RequiredApprovals {
		if opened[req.Key] || !requirementApplies(req, t.TypeID) {
			continue
		}
		out = append(out, Approval{
			TaskID: t.ID, Key: req.Key, Label: req.Label, Stage: from.ID,
			Source: ApprovalFromStage, Status: ApprovalPending,
		})
	}
	return out
}

func (s *Store) StageExitGate(ctx context.Context, t *Task, from, to Stage, cause GateCause) (GateVerdict, error) {
	v, err := s.StageExitCheck(ctx, t, from, to, cause)
	if err != nil || v.Passed || t.ID == "" || t.Stage != from {
		return v, err
	}
	if !slices.ContainsFunc(v.Blocking, func(a Approval) bool { return a.ID == "" }) {
		return v, nil
	}
	// Ignoring the error is deliberate: if opening fails, the placeholders still refuse the move.
	info, _ := s.board(t.BoardID).Get(from)
	opened, ensureErr := s.EnsureStageApprovals(ctx, t, info)
	logx.Warn(ensureErr, "task.approvals.log.ensure", "task", t.ID)
	if len(opened) == 0 {
		return v, nil
	}
	return s.StageExitCheck(ctx, t, from, to, cause)
}

func (s *Store) stageEntered(ctx context.Context, t *Task) {
	s.claimAwaitingApproval(ctx, t.ID)
	s.settleQuestionsLeftBehind(ctx, t)
	info, ok := s.board(t.BoardID).Get(t.Stage)
	if !ok || info.Virtual {
		return
	}
	reopened, err := s.ReopenChangesRequested(ctx, t.ID, t.Stage, time.Time{})
	logx.Warn(err, "task.approvals.log.reopen", "task", t.ID)
	for i := range reopened {
		s.recordApprovalEvent(ctx, EventApprovalReset, &reopened[i], "octopus", "task.approvals.events.reset_reentered")
	}
	_, err = s.EnsureStageApprovals(ctx, t, info)
	logx.Warn(err, "task.approvals.log.ensure", "task", t.ID)
}

// Two decisions landing together both see the gate open; only the caller that wins this UPDATE may
// move the task.
func (s *Store) claimAwaitingApproval(ctx context.Context, taskID string) bool {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE tasks SET status = ? WHERE id = ? AND status = ?`),
		string(StatusIdle), taskID, string(StatusAwaitingApproval))
	if err != nil {
		logx.Warn(err, "task.approvals.log.claim", "task", taskID)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func (d *Dispatcher) holdForApprovals(ctx context.Context, t *Task, taskID string, from, to Stage) bool {
	if t == nil {
		return false
	}
	v, err := d.store.StageExitGate(ctx, t, from, to, GateAutoAdvance)
	if err != nil {
		// Fail closed: unreadable approvals park the task instead of opening the gate.
		logx.Warn(err, "task.approvals.log.gate", "task", taskID)
		d.setStatus(ctx, taskID, StatusAwaitingApproval)
		return true
	}
	if v.Passed {
		return false
	}
	note := NoteOf("task.approvals.comments.awaiting", "stage", d.stageNote(t, from), "approvals", v.Labels())
	if v.OnlyChecks() {
		note = NoteOf("task.approvals.comments.awaiting_checks", "stage", d.stageNote(t, from),
			"approvals", v.Labels(), "checks", ChecksText(ctx, v.Checks))
	}
	d.systemComment(ctx, taskID, note)
	d.setStatus(ctx, taskID, StatusAwaitingApproval)
	// The last approval may have landed between the gate and the status write, with nothing parked
	// yet for it to move.
	d.ApprovalsSettled(ctx, taskID)
	return true
}

func (d *Dispatcher) ApprovalsSettled(ctx context.Context, taskID string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || t.Status != StatusAwaitingApproval {
		return
	}
	stages := d.stagesFor(t)
	next, _ := d.nextStageFor(t, t.Stage)
	if next != "" {
		v, err := d.store.StageExitCheck(ctx, t, t.Stage, next, GateAutoAdvance)
		if err != nil || !v.Passed {
			return
		}
	}
	if !d.store.claimAwaitingApproval(ctx, taskID) {
		return
	}
	t.Status = StatusIdle
	if info, _ := stages.Get(t.Stage); info.AutoAdvance && next != "" {
		d.systemComment(ctx, taskID, NoteOf("task.approvals.comments.settled_advancing", "stage", d.stageNote(t, next)))
		d.autoAdvanceStage(ctx, t, taskID, t.Stage, 0, "")
		return
	}
	d.setStatus(ctx, taskID, StatusIdle)
}

func (d *Dispatcher) DecideApproval(ctx context.Context, t *Task, id string, status ApprovalStatus, by, note string) (*Approval, error) {
	return d.decideApproval(ctx, t, id, status, by, note, true)
}

// Compatibility: a rejected flag via the flags alias never resumed or kicked back the task, and
// must not start now.
func (d *Dispatcher) DecideFlag(ctx context.Context, t *Task, id string, status ApprovalStatus, by, note string) (*Approval, error) {
	return d.decideApproval(ctx, t, id, status, by, note, false)
}

func (d *Dispatcher) decideApproval(
	ctx context.Context, t *Task, id string, status ApprovalStatus, by, note string, actOnChanges bool,
) (*Approval, error) {
	if !status.IsDecision() {
		return nil, apperr.Invalid("task.approvals.errors.bad_decision", "decision", status)
	}
	a, err := d.store.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.TaskID != t.ID {
		return nil, apperr.NotFound("task.approvals.errors.not_found")
	}
	// A stage's own approval vouches for what its agent produced: approving it while that agent
	// still works approves a definition, a design or a change that does not exist yet.
	if status.Settled() && a.Source == ApprovalFromStage && a.Stage == t.Stage {
		if _, runStage, alive := d.liveRun(t); alive && runStage == a.Stage {
			return nil, apperr.Conflict("task.approvals.errors.stage_agent_working",
				"approval", a.Label, "stage", d.stageNote(t, a.Stage))
		}
	}
	// Nothing acts on changes requested in a stage the task has not reached, and entering it sends
	// the approval back to pending with the note cleared: the request would be lost.
	if status == ApprovalChangesRequested && d.stageAhead(t, a.Stage) {
		return nil, apperr.Conflict("task.approvals.errors.changes_ahead",
			"approval", a.Label, "stage", d.stageNote(t, a.Stage))
	}
	ref := ""
	if status == ApprovalApproved {
		ref = d.currentRef(ctx, t)
	}
	decided, err := d.store.SetApprovalStatus(ctx, id, status, by, note, ref)
	if err != nil {
		return nil, err
	}
	kind, summary := EventApprovalDecided, "task.approvals.events.approved"
	switch status {
	case ApprovalWaived:
		kind, summary = EventApprovalWaived, "task.approvals.events.waived"
	case ApprovalChangesRequested:
		summary = "task.approvals.events.changes_requested"
	case ApprovalApproved, ApprovalPending:
	}
	d.store.recordApprovalEvent(ctx, kind, decided, by, summary)
	d.relockTestChange(ctx, decided)
	switch {
	case status != ApprovalChangesRequested:
		d.ApprovalsSettled(ctx, t.ID)
	case actOnChanges:
		d.changesRequested(ctx, t.ID, decided)
	}
	return decided, nil
}

// While a run works on the task, do nothing: a resume would be refused and a kick_back would move
// the task under its container.
// The decision stays changes_requested and deliverLateChanges delivers it when the run ends.
// Whether stage comes after the task's current one on its board (an approval an agent asked to be
// decided there).
func (d *Dispatcher) stageAhead(t *Task, stage Stage) bool {
	if stage == "" || stage == t.Stage {
		return false
	}
	stages := d.stagesFor(t)
	to, ok := stages.Get(stage)
	from, fromOK := stages.Get(t.Stage)
	return ok && fromOK && to.Position > from.Position
}

func (d *Dispatcher) changesRequested(ctx context.Context, taskID string, a *Approval) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || a.Stage == "" || a.Stage != t.Stage {
		return
	}
	if info, _ := d.stagesFor(t).Get(t.Stage); info.OnChangesRequested == types.ChangesStay {
		return
	}
	if t.Status == StatusRunning {
		d.systemComment(ctx, taskID, NoteOf("task.approvals.comments.changes_deferred",
			"approval", a.Label, "by", a.DecidedBy))
		return
	}
	d.applyChangesRequested(ctx, t, []Approval{*a}, nil)
}

func (d *Dispatcher) applyChangesRequested(ctx context.Context, t *Task, list []Approval, onAbort func()) bool {
	stages := d.stagesFor(t)
	info, _ := stages.Get(t.Stage)
	switch info.OnChangesRequested {
	case types.ChangesStay:
		return false
	case types.ChangesKickBack:
		// Only a stage before this one qualifies: a forward target would carry the task past the
		// approvals holding it.
		target := kickBackTarget(stages, t.Stage, info.ChangesKickBackTo)
		if target == "" {
			return false
		}
		notes := make([]Note, len(list))
		for i := range list {
			notes[i] = NoteOf("task.approvals.comments.kicked_back",
				"approval", list[i].Label, "by", list[i].DecidedBy, "stage", d.stageNote(t, target), "note", quoteNote(list[i].DecidedNote))
		}
		last := list[len(list)-1]
		d.kickBack(ctx, t, kickBackReq{From: t.Stage, Target: target, Via: ViaChangesRequested,
			Actor: last.DecidedBy, RefID: last.ID, Reason: last.DecidedNote, Notes: notes})
		return true
	default:
		msgs := make([]string, len(list))
		for i := range list {
			msgs[i] = i18n.Text("task.approvals.resume_message",
				"approval", list[i].Label, "by", list[i].DecidedBy, "note", list[i].DecidedNote)
		}
		if err := d.resumeStage(ctx, t, strings.Join(msgs, "\n\n"), LaunchChangesResume, onAbort); err != nil {
			for i := range list {
				d.systemComment(ctx, t.ID, NoteOf("task.approvals.comments.resume_failed",
					"approval", list[i].Label, "err", err, "note", quoteNote(list[i].DecidedNote)))
			}
			d.resumeFailed(ctx, t, Note{}, err)
			return false
		}
		return true
	}
}

func quoteNote(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	lines := strings.Split(note, "\n")
	for i := range lines {
		lines[i] = "> " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// When it returns true the run's end must not settle or move the task.
// The instance's launch time separates requests the run never saw from ones it answered.
func (d *Dispatcher) deliverLateChanges(ctx context.Context, taskID, instance string) bool {
	started := instanceStartedAt(instance)
	if started.IsZero() {
		return false
	}
	t, err := d.store.Get(ctx, taskID)
	if err != nil {
		return false
	}
	var late []Approval
	for i := range t.Approvals {
		a := t.Approvals[i]
		if a.Stage == t.Stage && a.Status == ApprovalChangesRequested && a.DecidedAt != nil && !a.DecidedAt.Before(started) {
			late = append(late, a)
		}
	}
	if len(late) == 0 {
		return false
	}
	// The task still reads running here; a launch that gives up must leave it idle.
	settle := func() { d.store.casStatus(context.WithoutCancel(ctx), taskID, StatusRunning, StatusIdle) }
	return d.applyChangesRequested(ctx, t, late, settle)
}

// Must match the instance id format "<agent>-task-<task>-<stage>-<unixms>" built at launch.
func instanceStartedAt(instance string) time.Time {
	i := strings.LastIndex(instance, "-")
	if i < 0 {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(instance[i+1:], 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// Changes requested after the run started stay: the run never saw them, and deliverLateChanges
// sends them.
func (d *Dispatcher) resetApprovalsAfterRun(ctx context.Context, t *Task, stage Stage, exitCode int, instance string) {
	if t == nil || exitCode != 0 {
		return
	}
	reopened, err := d.store.ReopenChangesRequested(ctx, t.ID, stage, instanceStartedAt(instance))
	logx.Warn(err, "task.approvals.log.reopen", "task", t.ID)
	for i := range reopened {
		d.store.recordApprovalEvent(ctx, EventApprovalReset, &reopened[i], "octopus", "task.approvals.events.reset_answered")
	}
	info, _ := d.stagesFor(t).Get(stage)
	stale, err := d.store.ResetStaleApprovals(ctx, t.ID, stage, info.RequiredApprovals, d.currentRef(ctx, t))
	logx.Warn(err, "task.approvals.log.reset", "task", t.ID)
	for i := range stale {
		d.store.recordApprovalEvent(ctx, EventApprovalReset, &stale[i], "octopus", "task.approvals.events.reset_changed")
	}
}

func (s *Store) casStatus(ctx context.Context, taskID string, from, to RunStatus) bool {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE tasks SET status = ? WHERE id = ? AND status = ?`),
		string(to), taskID, string(from))
	if err != nil {
		logx.Warn(err, "task.approvals.log.claim", "task", taskID)
		return false
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		s.notifyTask(ctx, taskID)
	}
	return n == 1
}

// Stored as the approval ref and compared by ResetStaleApprovals: keep the format and sort order
// stable. Empty resets nothing.
func (d *Dispatcher) currentRef(ctx context.Context, t *Task) string {
	if d.projects == nil || t == nil || len(t.ProjectIDs) == 0 {
		return ""
	}
	ids := slices.Clone(t.ProjectIDs)
	slices.Sort(ids)
	owner := t.SandboxRootID()
	var parts []string
	for _, pid := range ids {
		wg, err := d.projects.HostWorktreeGit(ctx, pid, owner)
		if err != nil {
			continue
		}
		out, err := wg.Exec.Output(wg.Dir, "rev-parse", "HEAD")
		if err != nil {
			continue
		}
		parts = append(parts, pid+"@"+strings.TrimSpace(out))
	}
	return strings.Join(parts, ",")
}

// JumpedRequirements lists the labels of the approvals that the stages strictly between from and to
// would ask a task of typeID for. Moving past them never opens them, so the gate cannot hold the
// move; a caller that must never skip an approval refuses such a move instead.
func JumpedRequirements(reg stagepkg.Registry, typeID string, from, to Stage) []string {
	fromInfo, okFrom := reg.Get(from)
	toInfo, okTo := reg.Get(to)
	if !okFrom || !okTo {
		return nil
	}
	var out []string
	stages := reg.List()
	for i := range stages {
		st := &stages[i]
		if st.Position <= fromInfo.Position || st.Position >= toInfo.Position {
			continue
		}
		for _, req := range st.RequiredApprovals {
			if requirementApplies(req, typeID) {
				out = append(out, req.Label)
			}
		}
	}
	return out
}
