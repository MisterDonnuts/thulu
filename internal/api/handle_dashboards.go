package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func (s *Server) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	rows, err := s.envMetrics.ListDashboards(r.Context(), env.ID)
	respondRows(w, r, rows, err)
}

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	d, err := s.envMetrics.GetDashboard(r.Context(), env.ID, r.PathValue("dashId"))
	if errors.Is(err, envmetrics.ErrDashboardNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.dashboards.errors.not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleProposeDashboard(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var body struct {
		envmetrics.DashboardInput
		TaskID string `json:"task_id,omitempty"`
		FlagID string `json:"flag_id,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.proposeDashboard(w, r, env, body.DashboardInput, body.TaskID, body.FlagID)
}

func (s *Server) handleProposeTaskDashboard(w http.ResponseWriter, r *http.Request) {
	t, env, ok := s.taskEnvironment(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var body struct {
		envmetrics.DashboardInput
		FlagID string `json:"flag_id,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.proposeDashboard(w, r, env, body.DashboardInput, t.ID, body.FlagID)
}

func (s *Server) proposeDashboard(w http.ResponseWriter, r *http.Request, env *project.Environment,
	in envmetrics.DashboardInput, taskID, flagID string,
) {
	if !s.checkProposalLinks(w, r, taskID, flagID) {
		return
	}
	by := authorLabel(r)
	if flagID == "" && taskID != "" {
		flagID = s.raiseDashboardFlag(r.Context(), taskID, env, in, by)
	}
	d, err := s.envMetrics.ProposeDashboard(r.Context(), env.ID, in, taskID, flagID, by)
	if errors.Is(err, envmetrics.ErrPanelInvalid) {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) raiseDashboardFlag(ctx context.Context, taskID string, env *project.Environment, in envmetrics.DashboardInput, by string) string {
	if s.taskStore == nil {
		return ""
	}
	types := map[string]int{}
	metrics := map[string]bool{}
	for _, p := range in.Panels {
		types[p.Type]++
		for _, ser := range p.Series {
			metrics[ser.Metric] = true
		}
	}
	payload := map[string]any{
		"environment":    env.Name,
		"environment_id": env.ID,
		"dashboard":      in.Name,
		"panels":         len(in.Panels),
		"panel_types":    types,
		"metrics":        len(metrics),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.dashboards.log.marshal_flag_payload_failed", "err", err))
		return ""
	}
	f := &task.Flag{
		Kind: task.FlagInfraImpact,
		Label: i18n.Text("api.dashboards.comments.proposal_flag",
			"name", in.Name, "environment", env.Name, "panels", len(in.Panels), "metrics", len(metrics)),
		CreatedBy: by,
		Payload:   raw,
	}
	if err := s.taskStore.AddFlagFrom(ctx, taskID, f, task.ApprovalFromSystem); err != nil {
		log.Printf("[api] %s", i18n.Log("api.dashboards.log.raise_flag_failed", "task", taskID, "err", err))
		return ""
	}
	s.recordEvent(ctx, taskID, task.EventFlagRaised, f.ID, by,
		fmt.Sprintf("%s · %s", f.Kind, f.Label), nil)
	return f.ID
}

func (s *Server) handleApproveDashboard(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	dashID := r.PathValue("dashId")
	flagIDs, err := s.envMetrics.PendingDashboardFlagIDs(r.Context(), env.ID, dashID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	d, err := s.envMetrics.ApproveDashboard(r.Context(), env.ID, dashID, authorLabel(r))
	if errors.Is(err, envmetrics.ErrDashboardNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.dashboards.errors.not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.resolveReviewFlags(r, flagIDs, authorLabel(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"dashboard":      d,
		"flags_resolved": len(flagIDs),
	})
}

// Editing keeps the dashboard's status; the route's admin role is the only gate on edits.
func (s *Server) handleUpdateDashboard(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var in envmetrics.DashboardInput
	if !decodeBody(w, r, &in) {
		return
	}
	d, err := s.envMetrics.UpdateDashboard(r.Context(), env.ID, r.PathValue("dashId"), in)
	switch {
	case errors.Is(err, envmetrics.ErrDashboardNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.dashboards.errors.not_found")
	case errors.Is(err, envmetrics.ErrPanelInvalid):
		writeError(w, r, http.StatusBadRequest, err)
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) handleDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	err := s.envMetrics.DeleteDashboard(r.Context(), env.ID, r.PathValue("dashId"))
	switch {
	case errors.Is(err, envmetrics.ErrDashboardNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.dashboards.errors.not_found")
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		writeStatus(w, "deleted")
	}
}

func (s *Server) handleDashboardData(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	d, err := s.envMetrics.GetDashboard(r.Context(), env.ID, r.PathValue("dashId"))
	if errors.Is(err, envmetrics.ErrDashboardNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.dashboards.errors.not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if h := r.URL.Query().Get("range_hours"); h != "" {
		n, convErr := strconv.Atoi(h)
		if convErr != nil || n <= 0 {
			writeErrKey(w, r, http.StatusBadRequest, "api.dashboards.errors.range_hours_invalid")
			return
		}
		for i := range d.Panels {
			d.Panels[i].RangeHours = n
		}
	}
	panels, err := s.envMetrics.ResolveDashboard(r.Context(), env.ID, d, time.Now().UTC())
	if errors.Is(err, envmetrics.ErrPanelInvalid) {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dashboard_id": d.ID,
		"name":         d.Name,
		"status":       d.Status,
		"panels":       panels,
	})
}
