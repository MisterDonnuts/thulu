package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

// <input type=datetime-local> omits seconds ("2026-07-30T17:46"); without that layout the parse
// fails and silently falls back to the 30-day default.
// Offset-less bounds parse as UTC; the dashboard converts to an absolute instant client-side.
var dashboardTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// Scope must come from the caller's workspace here; a handler building its own Filter literal would
// leak or report nothing.
func (s *Server) parseFilter(r *http.Request) usage.Filter {
	q := r.URL.Query()
	parseTime := func(raw string) time.Time {
		if raw == "" {
			return time.Time{}
		}
		for _, layout := range dashboardTimeLayouts {
			if t, err := time.Parse(layout, raw); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	return usage.Filter{
		Scope:       s.usageScope(r),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		Engine:      q.Get("engine"),
		Model:       q.Get("model"),
		AgentType:   q.Get("agent_type"),
		ProjectID:   q.Get("project_id"),
		TaskID:      q.Get("task_id"),
		Stage:       q.Get("stage"),
		ServiceTier: q.Get("service_tier"),
	}
}

func respondRows[T any](w http.ResponseWriter, r *http.Request, rows []T, err error) {
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	rows = nonNil(rows)
	writeJSON(w, http.StatusOK, rows)
}

func limitParam(r *http.Request, def, maxVal int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxVal {
			return n
		}
	}
	return def
}

func intParam(q url.Values, key string) int {
	n, err := strconv.Atoi(q.Get(key))
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) handleUsageTimeseries(w http.ResponseWriter, r *http.Request) {
	f := s.parseFilter(r)
	bucket := usage.BucketDay
	if r.URL.Query().Get("bucket") == "hour" {
		bucket = usage.BucketHour
	}
	points, err := s.usageStore.TimeSeries(r.Context(), f, bucket)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	points = nonNil(points)
	writeJSON(w, http.StatusOK, map[string]any{
		"bucket": bucket,
		"points": points,
	})
}

func (s *Server) handleUsageByDimension(col string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.usageStore.ByDimension(r.Context(), s.parseFilter(r), col)
		if err == nil && col == "project_id" {
			s.labelProjectRows(r.Context(), rows)
		}
		respondRows(w, r, rows, err)
	}
}

// Unscoped project Get is safe: rows are already confined to the caller's workspace.
func (s *Server) labelProjectRows(ctx context.Context, rows []usage.DimensionRow) {
	if s.projectStore == nil {
		return
	}
	for i := range rows {
		if rows[i].Key == "" || rows[i].Key == "(none)" {
			continue
		}
		if p, err := s.projectStore.Get(ctx, rows[i].Key); err == nil {
			rows[i].Label = p.Name
		}
	}
}

func (s *Server) titleTaskRows(ctx context.Context, rows []usage.TaskRow) {
	if s.taskStore == nil || len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].TaskID
	}
	titles, err := s.taskStore.Titles(ctx, ids)
	logx.Warn(err, "api.metrics.log.top_task_titles")
	for i := range rows {
		rows[i].Title = titles[rows[i].TaskID]
	}
}

func (s *Server) handleUsageByTool(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.ByTool(r.Context(), s.parseFilter(r))
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageBySkill(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.BySkill(r.Context(), s.parseFilter(r))
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageByMCPServer(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.ByMCPServer(r.Context(), s.parseFilter(r))
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageByTokenOptimizer(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.ByTokenOptimizer(r.Context(), s.parseFilter(r))
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageDelegations(w http.ResponseWriter, r *http.Request) {
	rep, err := s.usageStore.Delegations(r.Context(), s.parseFilter(r), limitParam(r, 25, 200))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleUsageSessionBreakdown(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	b, err := s.usageStore.Breakdown(r.Context(), sess.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleUsageTopTasks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.TopTasks(r.Context(), s.parseFilter(r), limitParam(r, 25, 200))
	if err == nil {
		s.titleTaskRows(r.Context(), rows)
	}
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageEfficiency(w http.ResponseWriter, r *http.Request) {
	eff, err := s.usageStore.Efficiency(r.Context(), s.parseFilter(r))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, eff)
}

func (s *Server) handleUsageBurnRate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	winH := intParam(q, "window_hours")
	baseD := intParam(q, "baseline_days")
	br, err := s.usageStore.BurnRate(r.Context(), s.parseFilter(r), winH, baseD)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, br)
}

func (s *Server) handleUsageFilteredSessions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.FilterSessions(r.Context(), s.parseFilter(r), limitParam(r, 200, 1000))
	respondRows(w, r, rows, err)
}

// Unscoped on purpose: prices are install-wide configuration, not tenant data.
func (s *Server) handleUsagePricing(w http.ResponseWriter, r *http.Request) {
	rows, err := s.usageStore.AllPricing(r.Context())
	respondRows(w, r, rows, err)
}

func (s *Server) handleUsageSessionToolEvents(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	evs, err := s.usageStore.ToolEventsForSession(r.Context(), sess.ID)
	respondRows(w, r, evs, err)
}
