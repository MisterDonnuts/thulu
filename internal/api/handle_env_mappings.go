package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func (s *Server) requireEnvMetrics(w http.ResponseWriter, r *http.Request) bool {
	if s.envMetrics == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.environments.errors.metrics_store_unavailable")
		return false
	}
	return true
}

func (s *Server) handleListEnvMappings(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	rows, err := s.envMetrics.ListMappings(r.Context(), env.ID)
	respondRows(w, r, rows, err)
}

func (s *Server) handleProposeEnvMappings(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var body struct {
		Mappings []envmetrics.MappingInput `json:"mappings"`
		TaskID   string                    `json:"task_id,omitempty"`
		FlagID   string                    `json:"flag_id,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.proposeEnvMappings(w, r, env, body.Mappings, body.TaskID, body.FlagID)
}

// A run's key never holds projects:write (auth.runKeyPermissions), so agents propose here under
// /tasks,
// which gets tasks:write by the path-prefix rule. Approval stays on the project route.
func (s *Server) handleProposeTaskEnvMappings(w http.ResponseWriter, r *http.Request) {
	t, env, ok := s.taskEnvironment(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var body struct {
		Mappings []envmetrics.MappingInput `json:"mappings"`
		FlagID   string                    `json:"flag_id,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.proposeEnvMappings(w, r, env, body.Mappings, t.ID, body.FlagID)
}

func (s *Server) taskEnvironment(w http.ResponseWriter, r *http.Request) (*task.Task, *project.Environment, bool) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return nil, nil, false
	}
	envID := r.PathValue("envId")
	for _, pid := range t.ProjectIDs {
		if env, err := s.projectStore.GetEnvironment(r.Context(), pid, envID); err == nil {
			return t, env, true
		}
	}
	writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.not_found")
	return nil, nil, false
}

func (s *Server) proposeEnvMappings(w http.ResponseWriter, r *http.Request, env *project.Environment,
	mappings []envmetrics.MappingInput, taskID, flagID string,
) {
	if len(mappings) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.environments.errors.mappings_required")
		return
	}
	if !s.checkProposalLinks(w, r, taskID, flagID) {
		return
	}
	// The flag is raised here because approval resolves only the flag ids stored on the mappings,
	// and an agent's flags.yaml flag does not exist yet at propose time.
	if flagID == "" && taskID != "" {
		flagID = s.raiseMappingFlag(r.Context(), taskID, env, mappings, authorLabel(r))
	}
	out, err := s.envMetrics.ProposeMappings(r.Context(), env.ID, mappings,
		taskID, flagID, authorLabel(r))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) raiseMappingFlag(ctx context.Context, taskID string, env *project.Environment, in []envmetrics.MappingInput, by string) string {
	if s.taskStore == nil {
		return ""
	}
	covered := make(map[envmetrics.Signal]bool, len(in))
	declaredNA := make(map[envmetrics.Signal]bool)
	services := make(map[string]bool, len(in))
	mapped := 0
	for _, m := range in {
		services[m.Service] = true
		if m.NotApplicable {
			declaredNA[m.Signal] = true
			continue
		}
		covered[m.Signal] = true
		mapped++
	}
	missing := make([]string, 0, len(envmetrics.ValidSignals))
	notApplicable := make([]string, 0, len(declaredNA))
	for _, sig := range envmetrics.ValidSignals {
		switch {
		case covered[sig]:
		case declaredNA[sig]:
			notApplicable = append(notApplicable, string(sig))
		default:
			missing = append(missing, string(sig))
		}
	}
	payload := map[string]any{
		"environment":            env.Name,
		"environment_id":         env.ID,
		"provider":               env.Provider,
		"is_production":          env.IsProduction,
		"mappings":               mapped,
		"services":               len(services),
		"signals_mapped":         len(covered),
		"signals_missing":        missing,
		"signals_not_applicable": notApplicable,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.environments.log.marshal_flag_payload_failed", "err", err))
		return ""
	}
	f := &task.Flag{
		Kind: task.FlagInfraImpact,
		Label: i18n.Text("api.environments.comments.mapping_flag",
			"environment", env.Name, "mappings", mapped, "services", len(services)),
		CreatedBy: by,
		Payload:   raw,
	}
	if err := s.taskStore.AddFlagFrom(ctx, taskID, f, task.ApprovalFromSystem); err != nil {
		log.Printf("[api] %s", i18n.Log("api.environments.log.raise_flag_failed", "task", taskID, "err", err))
		return ""
	}
	s.recordEvent(ctx, taskID, task.EventFlagRaised, f.ID, by,
		fmt.Sprintf("%s · %s", f.Kind, f.Label), nil)
	return f.ID
}

func (s *Server) handleCollectEnvNow(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	if s.envCollector == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.environments.errors.collector_unavailable")
		return
	}
	n, err := s.envCollector.CollectNow(r.Context(), env.ID)
	switch {
	case errors.Is(err, envmetrics.ErrNoActiveMappings):
		writeJSON(w, http.StatusOK, map[string]any{
			"environment_id": env.ID, "points": 0,
			"note": i18n.TCtx(r.Context(), "api.environments.hints.no_active_mappings"),
		})
	case errors.Is(err, envmetrics.ErrCollectThrottled):
		writeError(w, r, http.StatusTooManyRequests, err)
	case err != nil:
		writeError(w, r, http.StatusBadGateway, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"environment_id": env.ID, "points": n})
	}
}

func (s *Server) handleApproveEnvMappings(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	flagIDs, err := s.envMetrics.PendingFlagIDs(r.Context(), env.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	n, err := s.envMetrics.ApproveMappings(r.Context(), env.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.resolveReviewFlags(r, flagIDs, authorLabel(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"environment_id": env.ID,
		"activated":      n,
		"flags_resolved": len(flagIDs),
	})
}

// Approval resolves stored flag ids, so an unchecked flag_id could approve another tenant's review
// gate,
// and an unchecked task_id could raise flags on another tenant's task.
func (s *Server) checkProposalLinks(w http.ResponseWriter, r *http.Request, taskID, flagID string) bool {
	if taskID == "" && flagID == "" {
		return true
	}
	if taskID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.environments.errors.flag_needs_task")
		return false
	}
	if s.taskStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.environments.errors.task_store_unavailable")
		return false
	}
	if _, ok := s.taskRowForCaller(w, r, taskID); !ok {
		return false
	}
	if flagID == "" {
		return true
	}
	if f, err := s.taskStore.GetFlag(r.Context(), flagID); err != nil || f.TaskID != taskID {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.flag_not_on_task", "flag", flagID, "task", taskID)
		return false
	}
	return true
}

// Skip flags whose task the approver cannot see: rows stored before checkProposalLinks may name any
// flag.
func (s *Server) resolveReviewFlags(r *http.Request, flagIDs []string, by string) {
	if s.taskStore == nil {
		return
	}
	ctx := r.Context()
	wid, unrestricted := s.callerWorkspace(r)
	approved := task.FlagApproved
	note := i18n.Text("api.environments.comments.flag_approved")
	for _, id := range flagIDs {
		f, err := s.taskStore.GetFlag(ctx, id)
		if err != nil {
			logx.Warn(err, "api.environments.log.resolve_flag_failed", "id", id)
			continue
		}
		t, err := s.taskStore.GetRow(ctx, f.TaskID)
		if err != nil || !workspaceVisible(t.WorkspaceID, wid, unrestricted) {
			continue
		}
		_, err = s.taskStore.UpdateFlag(ctx, id, task.FlagPatch{
			Status:       &approved,
			ResolvedBy:   &by,
			ResolvedNote: &note,
		})
		logx.Warn(err, "api.environments.log.resolve_flag_failed", "id", id)
		s.approvalsSettled(r, t.ID)
	}
}

func (s *Server) handleDeleteEnvMapping(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	err := s.envMetrics.DeleteMapping(r.Context(), env.ID, r.PathValue("mapId"))
	if errors.Is(err, envmetrics.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.environments.errors.mapping_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, "deleted")
}

func (s *Server) handleEnvSeries(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	f := s.parseFilter(r)
	to, from := f.To, f.From
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	q := r.URL.Query()
	signal := envmetrics.Signal(q.Get("signal"))
	if signal != "" && !signal.Valid() {
		writeErrKey(w, r, http.StatusBadRequest, "api.environments.errors.unknown_signal")
		return
	}
	pts, err := s.envMetrics.Series(r.Context(), env.ID, signal, q.Get("service"), from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	pts = nonNil(pts)
	writeJSON(w, http.StatusOK, map[string]any{
		"environment_id": env.ID,
		"from":           from,
		"to":             to,
		"points":         pts,
	})
}

func (s *Server) handleEnvSignals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, envmetrics.ValidSignals)
}
