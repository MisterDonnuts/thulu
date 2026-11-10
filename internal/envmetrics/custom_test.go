package envmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func obs(service, name string, kind MetricKind, labels ...string) Observation {
	return Observation{Service: service, Name: name, Kind: kind, Unit: "count", Labels: labels}
}

func mustRegister(t *testing.T, s *Store, envID string, o Observation) Metric {
	t.Helper()
	reg, _, err := s.RegisterObservations(context.Background(), envID, []Observation{o})
	if err != nil {
		t.Fatalf("RegisterObservations: %v", err)
	}
	m, ok := reg[metricKey(o.Service, o.Name)]
	if !ok {
		t.Fatalf("metric %s/%s was not registered", o.Service, o.Name)
	}
	return m
}

func approve(t *testing.T, s *Store, envID, id string) Metric {
	t.Helper()
	st := MetricApproved
	m, err := s.UpdateCustomMetric(context.Background(), envID, id, MetricPatch{Status: &st}, "someone@example.com")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return *m
}

func TestRegisterObservationsDiscoversAndMerges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m := mustRegister(t, s, "env-1", obs("api", "checkout_errors", KindCounter, "payment_method"))
	if m.Status != MetricDiscovered {
		t.Errorf("a metric arriving over the wire must land as discovered, got %q", m.Status)
	}
	if m.LabelLimit != DefaultLabelLimit {
		t.Errorf("label limit = %d, want the default %d", m.LabelLimit, DefaultLabelLimit)
	}

	again := mustRegister(t, s, "env-1", obs("api", "checkout_errors", KindCounter, "region"))
	if again.ID != m.ID {
		t.Error("re-observing a metric must reuse its row, not register a second one")
	}
	if !again.HasLabel("payment_method") || !again.HasLabel("region") {
		t.Errorf("labels were replaced instead of merged: %v", again.Labels)
	}

	all, err := s.ListCustomMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListCustomMetrics: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("registry has %d metrics, want 1", len(all))
	}
}

func TestUnlabelledMetricKeepsAnEmptyList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mustRegister(t, s, "env-1", obs("api", "uptime", KindGauge))
	again := mustRegister(t, s, "env-1", obs("api", "uptime", KindGauge))
	assertLabelsJSON(t, again)

	all, err := s.ListCustomMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListCustomMetrics: %v", err)
	}
	assertLabelsJSON(t, all[0])

	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE environment_custom_metrics SET labels = 'null' WHERE id = ?`), again.ID); err != nil {
		t.Fatalf("seed null labels: %v", err)
	}
	all, err = s.ListCustomMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListCustomMetrics: %v", err)
	}
	assertLabelsJSON(t, all[0])
}

func assertLabelsJSON(t *testing.T, m Metric) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(got.Labels) != "[]" {
		t.Errorf("labels = %s, want []", got.Labels)
	}
}

func TestRegisterObservationsHonoursTheCeiling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	batch := make([]Observation, 0, MaxMetricsPerEnvironment+5)
	for i := 0; i < MaxMetricsPerEnvironment+5; i++ {
		batch = append(batch, obs("api", fmt.Sprintf("metric_%02d", i), KindCounter))
	}
	reg, over, err := s.RegisterObservations(ctx, "env-full", batch)
	if err != nil {
		t.Fatalf("RegisterObservations: %v", err)
	}
	if len(reg) != MaxMetricsPerEnvironment {
		t.Errorf("registered %d metrics, want the ceiling of %d", len(reg), MaxMetricsPerEnvironment)
	}
	if over != 5 {
		t.Errorf("over = %d, want 5 — the count is what makes a full registry visible instead of inferred", over)
	}

	if _, _, err := s.RegisterObservations(ctx, "env-full", []Observation{batch[0]}); err != nil {
		t.Fatalf("re-observing an already registered metric must still work: %v", err)
	}
}

func TestLabelsBeyondTheCeilingAreDropped(t *testing.T) {
	s := newTestStore(t)
	labels := make([]string, 0, MaxLabelsPerMetric+3)
	for i := 0; i < MaxLabelsPerMetric+3; i++ {
		labels = append(labels, fmt.Sprintf("label_%d", i))
	}
	m := mustRegister(t, s, "env-1", obs("api", "wide", KindCounter, labels...))
	if len(m.Labels) != MaxLabelsPerMetric {
		t.Errorf("metric kept %d labels, want the ceiling of %d", len(m.Labels), MaxLabelsPerMetric)
	}
}

func TestCountersAddAndGaugesReplace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	bucket := TruncateBucket(time.Now().UTC().Add(-time.Hour))

	counter := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))
	gauge := mustRegister(t, s, "env-1", obs("api", "queue_depth", KindGauge))

	for _, v := range []float64{3, 4} {
		if err := s.WriteCustomPoints(ctx, []CustomPoint{
			{MetricID: counter.ID, Kind: KindCounter, Bucket: bucket, Value: v},
			{MetricID: gauge.ID, Kind: KindGauge, Bucket: bucket, Value: v},
		}); err != nil {
			t.Fatalf("WriteCustomPoints: %v", err)
		}
	}

	from, to := bucket.Add(-time.Hour), bucket.Add(time.Hour)
	cpts, err := s.CustomSeries(ctx, counter.ID, from, to)
	if err != nil {
		t.Fatalf("CustomSeries: %v", err)
	}
	if len(cpts) != 1 || cpts[0].Value != 7 {
		t.Errorf("counter = %+v, want a single point of 7", cpts)
	}
	gpts, err := s.CustomSeries(ctx, gauge.ID, from, to)
	if err != nil {
		t.Fatalf("CustomSeries: %v", err)
	}
	if len(gpts) != 1 || gpts[0].Value != 4 {
		t.Errorf("gauge = %+v, want a single point of 4", gpts)
	}
}

func TestLabelledSeriesStaySeparate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	bucket := TruncateBucket(time.Now().UTC().Add(-time.Hour))

	m := mustRegister(t, s, "env-1", obs("api", "checkout_errors", KindCounter, "payment_method"))
	pts := []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Labels: map[string]string{"payment_method": "visa"}, Bucket: bucket, Value: 2},
		{MetricID: m.ID, Kind: KindCounter, Labels: map[string]string{"payment_method": "amex"}, Bucket: bucket, Value: 5},
	}
	if err := s.WriteCustomPoints(ctx, pts); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}
	got, err := s.CustomSeries(ctx, m.ID, bucket.Add(-time.Hour), bucket.Add(time.Hour))
	if err != nil {
		t.Fatalf("CustomSeries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d series in the bucket, want 2 — label sets must not collide", len(got))
	}
	keys, err := s.LabelKeysFor(ctx, m.ID)
	if err != nil {
		t.Fatalf("LabelKeysFor: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("LabelKeysFor returned %d combinations, want 2", len(keys))
	}
}

func TestCanonicalLabelsIsOrderIndependent(t *testing.T) {
	a := CanonicalLabels(map[string]string{"b": "2", "a": "1"})
	b := CanonicalLabels(map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Errorf("canonical forms differ: %q vs %q", a, b)
	}
	if CanonicalLabels(nil) != "{}" {
		t.Errorf("empty labels = %q, want {}", CanonicalLabels(nil))
	}
}

func TestBackToDiscoveredUndoesApproval(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))
	approve(t, s, "env-1", m.ID)

	waiting := MetricDiscovered
	got, err := s.UpdateCustomMetric(ctx, "env-1", m.ID, MetricPatch{Status: &waiting}, "someone@example.com")
	if err != nil {
		t.Fatalf("back to discovered: %v", err)
	}
	if got.Status != MetricDiscovered || got.ApprovedBy != "" {
		t.Errorf("status = %q, approved_by = %q; want discovered and nobody", got.Status, got.ApprovedBy)
	}
	approvedOnly, err := s.ApprovedMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ApprovedMetrics: %v", err)
	}
	if len(approvedOnly) != 0 {
		t.Errorf("ApprovedMetrics returned %d, want 0", len(approvedOnly))
	}
}

func TestApproveAndReject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))

	got := approve(t, s, "env-1", m.ID)
	if got.Status != MetricApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
	if got.ApprovedBy != "someone@example.com" {
		t.Errorf("approved_by = %q — who approved is the audit trail of the gate", got.ApprovedBy)
	}

	approvedOnly, err := s.ApprovedMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ApprovedMetrics: %v", err)
	}
	if len(approvedOnly) != 1 {
		t.Errorf("ApprovedMetrics returned %d, want 1", len(approvedOnly))
	}

	rejected := MetricRejected
	if _, err := s.UpdateCustomMetric(ctx, "env-1", m.ID, MetricPatch{Status: &rejected}, "someone@example.com"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	after, err := s.ApprovedMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ApprovedMetrics: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("a rejected metric is still listed as approved: %+v", after)
	}
}

func TestUpdateCustomMetricRejectsNonsense(t *testing.T) {
	s := newTestStore(t)
	m := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))
	bad := MetricStatus("maybe")
	if _, err := s.UpdateCustomMetric(context.Background(), "env-1", m.ID, MetricPatch{Status: &bad}, "x"); err == nil {
		t.Error("an unknown status should be rejected")
	}
	zero := 0
	if _, err := s.UpdateCustomMetric(context.Background(), "env-1", m.ID, MetricPatch{LabelLimit: &zero}, "x"); err == nil {
		t.Error("a label limit of zero should be rejected")
	}
}

func TestDeleteCustomMetricTakesItsPoints(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	bucket := TruncateBucket(time.Now().UTC())
	m := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))
	if err := s.WriteCustomPoints(ctx, []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Bucket: bucket, Value: 1},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}
	if err := s.DeleteCustomMetric(ctx, "env-1", m.ID); err != nil {
		t.Fatalf("DeleteCustomMetric: %v", err)
	}
	keys, err := s.LabelKeysFor(ctx, m.ID)
	if err != nil {
		t.Fatalf("LabelKeysFor: %v", err)
	}
	if len(keys) != 0 {
		t.Error("deleting a metric must take its points — otherwise the next observation resurrects it with a gap")
	}
	if err := s.DeleteCustomMetric(ctx, "env-1", m.ID); err == nil {
		t.Error("deleting a metric twice should report it missing")
	}
}

func TestDeleteForEnvironmentCascadesToCustom(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := mustRegister(t, s, "env-doomed", obs("api", "orders", KindCounter))
	if _, err := s.ProposeDashboard(ctx, "env-doomed", DashboardInput{
		Name: "D", Panels: []Panel{{Type: "line", Title: "T", Series: []PanelSeries{
			{Metric: "orders", Service: "api", Agg: AggSum},
		}}},
	}, "", "", "tester"); err == nil {
		t.Fatal("a dashboard over an unapproved metric should have been rejected")
	}
	approve(t, s, "env-doomed", m.ID)
	if _, err := s.ProposeDashboard(ctx, "env-doomed", DashboardInput{
		Name: "D", Panels: []Panel{{Type: "line", Title: "T", Series: []PanelSeries{
			{Metric: "orders", Service: "api", Agg: AggSum},
		}}},
	}, "", "", "tester"); err != nil {
		t.Fatalf("ProposeDashboard: %v", err)
	}

	if err := s.DeleteForEnvironment(ctx, "env-doomed"); err != nil {
		t.Fatalf("DeleteForEnvironment: %v", err)
	}
	metrics, err := s.ListCustomMetrics(ctx, "env-doomed")
	if err != nil {
		t.Fatalf("ListCustomMetrics: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("registry survived the cascade: %+v", metrics)
	}
	dashboards, err := s.ListDashboards(ctx, "env-doomed")
	if err != nil {
		t.Fatalf("ListDashboards: %v", err)
	}
	if len(dashboards) != 0 {
		t.Errorf("dashboards survived the cascade: %+v", dashboards)
	}
}

func TestPurgeCustomOlderThan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := mustRegister(t, s, "env-1", obs("api", "orders", KindCounter))
	old := TruncateBucket(time.Now().UTC().Add(-100 * 24 * time.Hour))
	recent := TruncateBucket(time.Now().UTC().Add(-time.Hour))
	if err := s.WriteCustomPoints(ctx, []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Bucket: old, Value: 1},
		{MetricID: m.ID, Kind: KindCounter, Bucket: recent, Value: 2},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}
	n, err := s.PurgeCustomOlderThan(ctx, time.Now().UTC().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("PurgeCustomOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d rows, want 1", n)
	}
	left, err := s.CustomSeries(ctx, m.ID, old.Add(-time.Hour), time.Now().UTC())
	if err != nil {
		t.Fatalf("CustomSeries: %v", err)
	}
	if len(left) != 1 || !left[0].Bucket.Equal(recent) {
		t.Errorf("purge kept the wrong points: %+v", left)
	}
}
