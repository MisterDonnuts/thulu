package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
)

func (s *Server) handleListCustomMetrics(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	rows, err := s.envMetrics.ListCustomMetrics(r.Context(), env.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	discovered := 0
	for i := range rows {
		if rows[i].Status == envmetrics.MetricDiscovered {
			discovered++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"environment_id": env.ID,
		"metrics":        rows,
		"discovered":     discovered,
		"registered":     len(rows),
		"capacity":       envmetrics.MaxMetricsPerEnvironment,
	})
}

func (s *Server) handleUpdateCustomMetric(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	var body envmetrics.MetricPatch
	if !decodeBody(w, r, &body) {
		return
	}
	m, err := s.envMetrics.UpdateCustomMetric(r.Context(), env.ID, r.PathValue("metricId"), body, authorLabel(r))
	switch {
	case errors.Is(err, envmetrics.ErrMetricNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.metrics.errors.custom_metric_not_found")
	case err != nil:
		writeError(w, r, http.StatusBadRequest, err)
	default:
		writeJSON(w, http.StatusOK, m)
	}
}

func (s *Server) handleDeleteCustomMetric(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	err := s.envMetrics.DeleteCustomMetric(r.Context(), env.ID, r.PathValue("metricId"))
	switch {
	case errors.Is(err, envmetrics.ErrMetricNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.metrics.errors.custom_metric_not_found")
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		writeStatus(w, "deleted")
	}
}

func (s *Server) handleCustomSeries(w http.ResponseWriter, r *http.Request) {
	env, ok := s.requireEnv(w, r)
	if !ok || !s.requireEnvMetrics(w, r) {
		return
	}
	m, err := s.envMetrics.GetCustomMetric(r.Context(), env.ID, r.PathValue("metricId"))
	if errors.Is(err, envmetrics.ErrMetricNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.metrics.errors.custom_metric_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
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
	pts, err := s.envMetrics.CustomSeries(r.Context(), m.ID, from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"metric": m,
		"from":   from,
		"to":     to,
		"points": pts,
	})
}

func (s *Server) handleChartTypes(w http.ResponseWriter, r *http.Request) {
	aggs := map[envmetrics.MetricKind][]envmetrics.Agg{
		envmetrics.KindCounter: envmetrics.AggsFor(envmetrics.KindCounter),
		envmetrics.KindGauge:   envmetrics.AggsFor(envmetrics.KindGauge),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"charts":       envmetrics.ChartTypesFor(r.Context()),
		"aggs_by_kind": aggs,
		"limits": map[string]int{
			"panels_per_dashboard":    envmetrics.MaxPanelsPerDashboard,
			"series_per_panel":        envmetrics.MaxSeriesPerPanel,
			"metrics_per_environment": envmetrics.MaxMetricsPerEnvironment,
			"labels_per_metric":       envmetrics.MaxLabelsPerMetric,
			"default_range_hours":     envmetrics.DefaultRangeHours,
		},
	})
}
