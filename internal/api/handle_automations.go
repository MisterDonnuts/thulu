package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/patch"
)

func (s *Server) automationScope(w http.ResponseWriter, r *http.Request) (automation.Scope, bool) {
	sc := s.automationReach(r)
	if !sc.All && sc.WorkspaceID == "" {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
		return sc, false
	}
	return sc, true
}

func (s *Server) automationsAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.auto.store == nil || s.auto.scheduler == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.automations.errors.disabled")
		return false
	}
	return true
}

// Dev installs with no workspace row keep boards and tasks under defaultWorkspaceID, so recipes go
// there too.
// With two or more workspaces return "" rather than guess a tenant.
func (s *Server) automationWorkspace(r *http.Request) string {
	wid, unrestricted := s.callerWorkspace(r)
	if wid != "" || !unrestricted {
		return wid
	}
	if sole := s.soleWorkspaceID(r.Context()); sole != "" {
		return sole
	}
	if s.noWorkspaces(r.Context()) {
		return defaultWorkspaceID
	}
	return ""
}

func (s *Server) parseWithRefs(ctx context.Context, ws, src string) error {
	spec, err := automation.ParseSpec(src)
	if err != nil {
		return err
	}
	return s.checkAutomationRefs(ctx, ws, spec)
}

func (s *Server) automationReach(r *http.Request) automation.Scope {
	wid, unrestricted := s.callerWorkspace(r)
	if unrestricted {
		return automation.AllWorkspaces
	}
	return automation.InWorkspace(wid)
}

func (s *Server) handleListAutomations(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	wid, ok := s.automationScope(w, r)
	if !ok {
		return
	}
	rows, err := s.auto.store.List(r.Context(), wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	rows = nonNil(rows)
	writeJSON(w, http.StatusOK, map[string]any{
		"automations": rows,
		"events":      automation.EventDescriptionsFor(r.Context()),
	})
}

func (s *Server) automationForCaller(w http.ResponseWriter, r *http.Request) (*automation.Automation, bool) {
	wid, ok := s.automationScope(w, r)
	if !ok {
		return nil, false
	}
	a, err := s.auto.store.Get(r.Context(), r.PathValue("id"), wid)
	switch {
	case errors.Is(err, automation.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.automations.errors.not_found")
		return nil, false
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	return a, true
}

func (s *Server) handleGetAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	a, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type automationBody struct {
	Spec    string `json:"spec"`
	Enabled *bool  `json:"enabled,omitempty"`
}

func (s *Server) handleCreateAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	var body automationBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Spec == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.automations.errors.spec_required")
		return
	}
	wid := s.automationWorkspace(r)
	if wid == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.automations.errors.workspace_undetermined")
		return
	}
	if err := s.parseWithRefs(r.Context(), wid, body.Spec); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := true
	patch.Set(&enabled, body.Enabled)
	a, err := s.auto.store.Create(r.Context(), wid, body.Spec, enabled)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.emit(r, audit.EventKind("automation.created"), "", a.ID, "automation",
		i18n.Text("api.automations.audit.created", "name", a.Name), map[string]string{"automation_id": a.ID})
	s.auto.scheduler.Nudge()
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	cur, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	var body automationBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Spec == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.automations.errors.spec_required")
		return
	}
	if err := s.parseWithRefs(r.Context(), cur.WorkspaceID, body.Spec); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := cur.Enabled
	patch.Set(&enabled, body.Enabled)
	a, err := s.auto.store.Update(r.Context(), cur.ID, s.automationReach(r), body.Spec, enabled)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	s.emit(r, audit.EventKind("automation.updated"), "", a.ID, "automation",
		i18n.Text("api.automations.audit.updated", "name", a.Name), map[string]string{"automation_id": a.ID})
	s.auto.scheduler.Nudge()
	writeJSON(w, http.StatusOK, a)
}

// Separate endpoint so suspending never resends the recipe, which would let a stale editor tab undo
// another edit.
func (s *Server) handleSetAutomationEnabled(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	cur, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	a, err := s.auto.store.SetEnabled(r.Context(), cur.ID, s.automationReach(r), body.Enabled)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.EventKind("automation.updated"), "", a.ID, "automation",
		i18n.Text(map[bool]string{true: "api.automations.audit.resumed", false: "api.automations.audit.suspended"}[body.Enabled], "name", a.Name),
		map[string]string{"automation_id": a.ID})
	s.auto.scheduler.Nudge()
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDeleteAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	cur, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	if err := s.auto.store.Delete(r.Context(), cur.ID, s.automationReach(r)); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.EventKind("automation.deleted"), "", cur.ID, "automation",
		i18n.Text("api.automations.audit.deleted", "name", cur.Name), map[string]string{"automation_id": cur.ID})
	writeStatus(w, "deleted")
}

func (s *Server) handleValidateAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	var body automationBody
	if !decodeBody(w, r, &body) {
		return
	}
	spec, err := automation.ParseSpec(body.Spec)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": apperr.LocalizeCtx(r.Context(), err)})
		return
	}
	if err := s.checkAutomationRefs(r.Context(), s.automationWorkspace(r), spec); err != nil {
		// Return the trigger even when only a reference is wrong, or the guided editor shows
		// "manual" and rewrites the trigger on the next edit.
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": apperr.LocalizeCtx(r.Context(), err),
			"name": spec.Name, "trigger": spec.Trigger.Kind(), "parsed": spec,
		})
		return
	}
	out := map[string]any{"valid": true, "name": spec.Name, "trigger": spec.Trigger.Kind(), "parsed": spec}
	if spec.Trigger.Schedule != "" {
		if sched, err := automation.ParseCron(spec.Trigger.Schedule, spec.Trigger.TZ); err == nil {
			out["next_runs"] = automation.NextRuns(sched, 5)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRunAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	a, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Payload map[string]any `json:"payload,omitempty"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	run, err := s.auto.scheduler.RunNow(r.Context(), a, body.Payload)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	run.Localize(i18n.FromContext(r.Context()))
	s.emit(r, audit.EventKind("automation.run"), "", a.ID, "automation",
		i18n.Text("api.automations.audit.run_by_hand", "name", a.Name), map[string]string{"automation_id": a.ID, "run_id": run.ID})
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListAutomationRuns(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	a, ok := s.automationForCaller(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := s.auto.store.ListRuns(r.Context(), a.ID, s.automationReach(r), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	runs = nonNil(runs)
	locale := i18n.FromContext(r.Context())
	for i := range runs {
		runs[i].Localize(locale)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleGetAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	wid, ok := s.automationScope(w, r)
	if !ok {
		return
	}
	run, err := s.auto.store.GetRun(r.Context(), r.PathValue("runId"), wid)
	switch {
	case errors.Is(err, automation.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.automations.errors.run_not_found")
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		run.Localize(i18n.FromContext(r.Context()))
		writeJSON(w, http.StatusOK, run)
	}
}

func (s *Server) handleCancelAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationsAvailable(w, r) {
		return
	}
	sc, ok := s.automationScope(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("runId")
	if _, err := s.auto.store.GetRun(r.Context(), runID, sc); err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.automations.errors.run_not_found")
		return
	}
	if err := s.auto.engine.Cancel(r.Context(), runID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, "canceled")
}
