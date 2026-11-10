package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

type approvalRequest struct {
	Key     string          `json:"key,omitempty"`
	Label   string          `json:"label"`
	Reason  string          `json:"reason,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Each approval fires a webhook, holds the stage and sits in the inbox; caps stop a looping run
// from opening them endlessly.
const (
	maxApprovalRequestBody = 64 << 10
	maxOpenAgentApprovals  = 10
)

type approvalDecision struct {
	Decision task.ApprovalStatus `json:"decision"`
	Note     string              `json:"note,omitempty"`
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	s.listTaskResource(w, r, func(id string) (any, error) {
		list, err := s.taskStore.ListApprovals(r.Context(), id)
		localizeApprovals(r.Context(), list)
		return list, err
	})
}

func (s *Server) handleRequestApproval(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxApprovalRequestBody)
	var in approvalRequest
	if !decodeBody(w, r, &in) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	source := task.ApprovalFromPerson
	if author.Agent {
		source = task.ApprovalFromAgent
		if !s.underAgentApprovalLimit(w, r, t.ID) {
			return
		}
	}
	a := &task.Approval{
		TaskID: t.ID, Key: strings.TrimSpace(in.Key), Label: strings.TrimSpace(in.Label),
		Reason: strings.TrimSpace(in.Reason), Payload: in.Payload, Stage: t.Stage,
		Source: source, Status: task.ApprovalPending, CreatedBy: author.Label(),
	}
	if err := s.taskStore.AddApproval(r.Context(), a); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	// Don't emit approval.requested here: the store hook (wireApprovals) sends it for every
	// approval.
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) underAgentApprovalLimit(w http.ResponseWriter, r *http.Request, taskID string) bool {
	list, err := s.taskStore.ListApprovals(r.Context(), taskID)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return false
	}
	open := 0
	for i := range list {
		if list[i].Source == task.ApprovalFromAgent && list[i].Status == task.ApprovalPending {
			open++
		}
	}
	if open >= maxOpenAgentApprovals {
		writeErrKey(w, r, http.StatusTooManyRequests, "api.approvals.errors.open_limit", "max", maxOpenAgentApprovals)
		return false
	}
	return true
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var in approvalDecision
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.Decision.IsDecision() {
		writeErrKey(w, r, http.StatusBadRequest, "task.approvals.errors.bad_decision", "decision", in.Decision)
		return
	}
	note := strings.TrimSpace(in.Note)
	if in.Decision != task.ApprovalApproved && note == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.approvals.errors.note_required", "decision", in.Decision)
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	if s.taskDispatcher == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.approvals.errors.no_dispatcher")
		return
	}
	a, err := s.taskDispatcher.DecideApproval(launchCtx(r.Context()), t, r.PathValue("aid"), in.Decision, author.Name, note)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	kind := audit.KindApprovalDecided
	if a.Status == task.ApprovalWaived {
		kind = audit.KindApprovalWaived
	}
	s.emit(r, kind, webhook.EventApprovalDecided, a.ID, "approval",
		i18n.Text("api.approvals.audit.decided", "task", t.ID, "approval", a.Label, "decision", a.Status),
		approvalHookData(t, a))
	out := decidedApproval{Approval: a}
	// A metric-mapping proposal's review approval has the id of the flag the proposal links to;
	// approving it activates the mappings.
	if a.Status == task.ApprovalApproved {
		out.MappingsActivated = s.activateMappingsForFlag(r, a.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decideThroughFlag(r *http.Request, t *task.Task, flagID string, p task.FlagPatch, by string) (*task.Flag, error) {
	if p.Label != nil {
		if _, err := s.taskStore.UpdateFlag(r.Context(), flagID, task.FlagPatch{Label: p.Label}); err != nil {
			return nil, err
		}
	}
	existing, err := s.taskStore.GetApproval(r.Context(), flagID)
	if err != nil {
		return nil, err
	}
	note := existing.DecidedNote
	if p.ResolvedNote != nil {
		note = *p.ResolvedNote
	}
	a, err := s.taskDispatcher.DecideFlag(launchCtx(r.Context()), t, flagID, task.ApprovalStatusOf(*p.Status), by, note)
	if err != nil {
		return nil, err
	}
	kind := audit.KindApprovalDecided
	if a.Status == task.ApprovalWaived {
		kind = audit.KindApprovalWaived
	}
	s.emit(r, kind, webhook.EventApprovalDecided, a.ID, "approval",
		i18n.Text("api.approvals.audit.decided", "task", t.ID, "approval", a.Label, "decision", a.Status),
		approvalHookData(t, a))
	return s.taskStore.GetFlag(r.Context(), flagID)
}

type decidedApproval struct {
	*task.Approval
	MappingsActivated int `json:"mappings_activated,omitempty"`
}

func (s *Server) handleDeleteApproval(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.deleteTaskApproval(w, r, t, r.PathValue("aid")) {
		writeStatus(w, "deleted")
	}
}

func (s *Server) deleteTaskApproval(w http.ResponseWriter, r *http.Request, t *task.Task, aid string) bool {
	// An approval of another task answers 404, like a missing one.
	a, err := s.taskStore.GetApproval(r.Context(), aid)
	if err != nil || a.TaskID != t.ID {
		writeErrKey(w, r, http.StatusNotFound, "task.approvals.errors.not_found")
		return false
	}
	// Deleting a stage requirement would waive it without a note; it would only reopen at the next
	// gate check.
	if a.Source == task.ApprovalFromStage {
		writeErrKey(w, r, http.StatusConflict, "api.approvals.errors.stage_requirement", "approval", a.Label)
		return false
	}
	if err := s.taskStore.DeleteApproval(r.Context(), aid); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return false
	}
	by := authorLabel(r)
	s.taskStore.RecordApprovalEvent(r.Context(), task.EventApprovalDeleted, a, by, "task.approvals.events.deleted")
	s.emit(r, audit.KindApprovalDeleted, "", a.ID, "approval",
		i18n.Text("api.approvals.audit.deleted", "task", t.ID, "approval", a.Label), approvalHookData(t, a))
	s.approvalsSettled(r, t.ID)
	return true
}

func (s *Server) approvalsSettled(r *http.Request, taskID string) {
	if s.taskDispatcher != nil {
		s.taskDispatcher.ApprovalsSettled(context.WithoutCancel(r.Context()), taskID)
	}
}

func (s *Server) wireApprovals() {
	if s.taskStore == nil {
		return
	}
	s.taskStore.WithApprovalHook(func(ctx context.Context, a task.Approval) {
		t, err := s.taskStore.GetRow(ctx, a.TaskID)
		if err != nil {
			logx.Warn(err, "api.approvals.log.hook_task", "task", a.TaskID)
			return
		}
		webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventApprovalRequested, approvalHookData(t, &a))
	})
	s.taskStore.WithQuestionCancelHook(func(ctx context.Context, q task.Question) {
		t, err := s.taskStore.GetRow(ctx, q.TaskID)
		if err != nil {
			logx.Warn(err, "api.approvals.log.hook_task", "task", q.TaskID)
			return
		}
		webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventQuestionCanceled, questionHookData(t, &q))
	})
}

func (s *Server) approvalGate(ctx context.Context, t *task.Task, from, to task.Stage, cause task.GateCause) (int, any, bool) {
	v, err := s.taskStore.StageExitGate(ctx, t, from, to, cause)
	if err != nil {
		return apperr.Status(err), errRefusal(ctx, nil, err), false
	}
	if v.Passed {
		return 0, nil, true
	}
	info, _ := s.stagesFor(t).Get(from)
	key := "api.approvals.errors.gate_closed"
	if cause == task.GateCreate {
		key = "api.approvals.errors.gate_closed_create"
	}
	return http.StatusConflict, keyedRefusal(ctx, map[string]any{
		"blocking_approvals": v.Blocking,
		"blocking_flags":     task.FlagsOf(v.Blocking),
	}, key, "stage", stageLabelIn(info, i18n.FromContext(ctx)), "approvals", v.Labels()), false
}

func (s *Server) recordSkippedApprovals(ctx context.Context, t *task.Task, from, to task.Stage, cause task.GateCause, by string) {
	v, err := s.taskStore.StageExitCheck(ctx, t, from, to, cause)
	if err != nil {
		logx.Warn(err, "api.approvals.log.skipped", "task", t.ID)
		return
	}
	if len(v.Blocking) == 0 {
		return
	}
	note := i18n.Text("api.approvals.events.waived_note", "cause", string(cause))
	for i := range v.Blocking {
		a := &v.Blocking[i]
		if a.ID == "" || a.Stage == "" {
			continue
		}
		waived, err := s.taskStore.SetApprovalStatus(ctx, a.ID, task.ApprovalWaived, by, note, "")
		if err != nil {
			logx.Warn(err, "api.approvals.log.skipped", "task", t.ID)
			continue
		}
		webhook.EmitBackground(ctx, s.webhookDisp, t.WorkspaceID, webhook.EventApprovalDecided, approvalHookData(t, waived))
	}
	s.recordEvent(ctx, t.ID, task.EventApprovalWaived, "", by,
		i18n.Text("api.approvals.events.skipped", "approvals", v.Labels(), "cause", string(cause)),
		map[string]any{"cause": string(cause), "from": string(from), "to": string(to), "approvals": v.Blocking})
}

func approvalHookData(t *task.Task, a *task.Approval) map[string]any {
	return map[string]any{
		"task_id":     t.ID,
		"task_title":  t.Title,
		"approval_id": a.ID,
		"key":         a.Key,
		"label":       a.Label,
		"stage":       string(a.Stage),
		"source":      string(a.Source),
		"status":      string(a.Status),
		"created_by":  a.CreatedBy,
		"decided_by":  a.DecidedBy,
		"note":        a.DecidedNote,
	}
}
