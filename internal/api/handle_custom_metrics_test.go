package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
)

func seedCustomMetric(t *testing.T, srv *Server, envID, service, name string, kind envmetrics.MetricKind, labels ...string) string {
	t.Helper()
	reg, _, err := srv.envMetrics.RegisterObservations(context.Background(), envID, []envmetrics.Observation{{
		Service: service, Name: name, Kind: kind, Unit: "count", Labels: labels,
	}})
	if err != nil {
		t.Fatalf("RegisterObservations: %v", err)
	}
	for _, m := range reg {
		return m.ID
	}
	t.Fatal("metric was not registered")
	return ""
}

func createEnvForMetrics(t *testing.T, h http.Handler, token, projID string) string {
	t.Helper()
	w := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token, map[string]any{
		"name": "prod", "provider": "aws", "is_production": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST environments = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decodeEnv(t, w)["id"].(string)
	if id == "" {
		t.Fatal("expected environment id")
	}
	return id
}

func TestIntegration_ChartCatalogIsServed(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "charts@test.com", "C", "SecurePassword1")

	w := sendJSON(t, h, http.MethodGet, "/chart-types", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /chart-types = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Charts []struct {
			Type string `json:"type"`
		} `json:"charts"`
		AggsByKind map[string][]string `json:"aggs_by_kind"`
		Limits     map[string]int      `json:"limits"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Charts) != len(envmetrics.ChartTypes) {
		t.Errorf("served %d chart types, want %d", len(out.Charts), len(envmetrics.ChartTypes))
	}
	if len(out.AggsByKind["counter"]) == 0 || len(out.AggsByKind["gauge"]) == 0 {
		t.Errorf("aggs_by_kind = %v, want both kinds", out.AggsByKind)
	}
	for _, k := range out.AggsByKind["gauge"] {
		if k == "sum" {
			t.Error("gauge must not offer sum")
		}
	}
	if out.Limits["metrics_per_environment"] != envmetrics.MaxMetricsPerEnvironment {
		t.Errorf("limits = %v, want the real ceiling", out.Limits)
	}
}

func TestIntegration_CustomMetricApprovalGate(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "cm@test.com", "C", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "CustomMetrics")
	envID := createEnvForMetrics(t, h, token, projID)
	base := "/projects/" + projID + "/environments/" + envID

	metricID := seedCustomMetric(t, srv, envID, "api", "checkout_errors", envmetrics.KindCounter, "payment_method")

	w := sendJSON(t, h, http.MethodGet, base+"/custom-metrics", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET custom-metrics = %d: %s", w.Code, w.Body.String())
	}
	listed := decodeEnv(t, w)
	if listed["discovered"] != float64(1) {
		t.Errorf("discovered = %v, want 1 — the tray count is what makes someone open it", listed["discovered"])
	}

	w = sendJSON(t, h, http.MethodPost, base+"/dashboards", token, map[string]any{
		"name": "Checkout",
		"panels": []map[string]any{{
			"type": "line", "title": "Errors",
			"series": []map[string]any{{"metric": "checkout_errors", "service": "api", "agg": "sum"}},
		}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("proposing over a discovered metric = %d, want 400: %s", w.Code, w.Body.String())
	}
	if msg := w.Body.String(); !strings.Contains(msg, "not approved") {
		t.Errorf("rejection %q does not say the metric is unapproved", msg)
	}

	w = sendJSON(t, h, http.MethodPatch, base+"/custom-metrics/"+metricID, token,
		map[string]any{"status": "approved"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH custom-metric = %d: %s", w.Code, w.Body.String())
	}
	if got := decodeEnv(t, w)["approved_by"]; got != "cm@test.com" {
		t.Errorf("approved_by = %v, want the caller — the gate needs an audit trail", got)
	}
}

func TestIntegration_DashboardProposeApproveResolve(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "dash@test.com", "D", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "Dashboards")
	envID := createEnvForMetrics(t, h, token, projID)
	base := "/projects/" + projID + "/environments/" + envID

	metricID := seedCustomMetric(t, srv, envID, "api", "checkout_errors", envmetrics.KindCounter, "payment_method")
	w := sendJSON(t, h, http.MethodPatch, base+"/custom-metrics/"+metricID, token,
		map[string]any{"status": "approved"})
	if w.Code != http.StatusOK {
		t.Fatalf("approve metric = %d: %s", w.Code, w.Body.String())
	}

	bucket := envmetrics.TruncateBucket(time.Now().UTC().Add(-10 * time.Minute))
	if err := srv.envMetrics.WriteCustomPoints(context.Background(), []envmetrics.CustomPoint{
		{MetricID: metricID, Kind: envmetrics.KindCounter, Bucket: bucket, Value: 7,
			Labels: map[string]string{"payment_method": "visa"}},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	w = sendJSON(t, h, http.MethodPost, base+"/dashboards", token, map[string]any{
		"name": "Checkout health",
		"panels": []map[string]any{{
			"type": "bar", "title": "Errors by method", "group_by": "payment_method",
			"range_hours": 1,
			"series":      []map[string]any{{"metric": "checkout_errors", "service": "api", "agg": "sum"}},
		}},
		"agent": "devops",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST dashboards = %d: %s", w.Code, w.Body.String())
	}
	dash := decodeEnv(t, w)
	dashID, _ := dash["id"].(string)
	if dash["status"] != "proposed" {
		t.Errorf("status = %v, want proposed — an agent's dashboard is inert until approved", dash["status"])
	}
	if dash["created_by"] != "dash@test.com" {
		t.Errorf("created_by = %v, want the authenticated user", dash["created_by"])
	}

	w = sendJSON(t, h, http.MethodGet, base+"/dashboards/"+dashID+"/data", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET dashboard data = %d: %s", w.Code, w.Body.String())
	}
	var data struct {
		Panels []struct {
			Type     string `json:"type"`
			TimeAxis bool   `json:"time_axis"`
			Series   []struct {
				Label string  `json:"label"`
				Value float64 `json:"value"`
			} `json:"series"`
		} `json:"panels"`
	}
	if err := json.NewDecoder(w.Body).Decode(&data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.Panels) != 1 || len(data.Panels[0].Series) != 1 {
		t.Fatalf("panels = %+v, want one panel with one series", data.Panels)
	}
	if data.Panels[0].Series[0].Label != "visa" || data.Panels[0].Series[0].Value != 7 {
		t.Errorf("series = %+v, want visa at 7", data.Panels[0].Series[0])
	}
	if data.Panels[0].TimeAxis {
		t.Error("a bar panel collapses its range; it must not claim a time axis")
	}

	w = sendJSON(t, h, http.MethodPost, base+"/dashboards/"+dashID+"/approve", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("approve dashboard = %d: %s", w.Code, w.Body.String())
	}
	w = sendJSON(t, h, http.MethodGet, base+"/dashboards/"+dashID, token, nil)
	if got := decodeEnv(t, w)["status"]; got != "active" {
		t.Errorf("status after approval = %v, want active", got)
	}

	w = sendJSON(t, h, http.MethodDelete, base+"/dashboards/"+dashID, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE dashboard = %d: %s", w.Code, w.Body.String())
	}
	w = sendJSON(t, h, http.MethodGet, base+"/dashboards/"+dashID, token, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", w.Code)
	}
}

func TestIntegration_DashboardRejectionNamesTheRule(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "rules@test.com", "R", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "Rules")
	envID := createEnvForMetrics(t, h, token, projID)
	base := "/projects/" + projID + "/environments/" + envID

	metricID := seedCustomMetric(t, srv, envID, "api", "queue_depth", envmetrics.KindGauge, "queue")
	if w := sendJSON(t, h, http.MethodPatch, base+"/custom-metrics/"+metricID, token,
		map[string]any{"status": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}

	w := sendJSON(t, h, http.MethodPost, base+"/dashboards", token, map[string]any{
		"name": "Bad",
		"panels": []map[string]any{{
			"type": "line", "title": "Depth",
			"series": []map[string]any{{"metric": "queue_depth", "service": "api", "agg": "sum"}},
		}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("summing a gauge = %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Depth", "sum", "gauge", "avg", "last"} {
		if !strings.Contains(body, want) {
			t.Errorf("rejection %q is missing %q", body, want)
		}
	}
}

func TestIntegration_CustomSeriesIsReadable(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "series@test.com", "S", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "Series")
	envID := createEnvForMetrics(t, h, token, projID)
	base := "/projects/" + projID + "/environments/" + envID

	metricID := seedCustomMetric(t, srv, envID, "api", "orders", envmetrics.KindCounter)
	if err := srv.envMetrics.WriteCustomPoints(context.Background(), []envmetrics.CustomPoint{
		{MetricID: metricID, Kind: envmetrics.KindCounter,
			Bucket: envmetrics.TruncateBucket(time.Now().UTC().Add(-time.Hour)), Value: 3},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	w := sendJSON(t, h, http.MethodGet, base+"/custom-metrics/"+metricID+"/series", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET series = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Points []struct {
			Value float64 `json:"value"`
		} `json:"points"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Points) != 1 || out.Points[0].Value != 3 {
		t.Errorf("points = %+v, want one point of 3", out.Points)
	}

	if w := sendJSON(t, h, http.MethodGet, base+"/custom-metrics/nope/series", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown metric = %d, want 404", w.Code)
	}
}
