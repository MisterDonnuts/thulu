package envmetrics

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedMetric(t *testing.T, s *Store, envID, service, name string, kind MetricKind, labels ...string) Metric {
	t.Helper()
	m := mustRegister(t, s, envID, obs(service, name, kind, labels...))
	return approve(t, s, envID, m.ID)
}

func linePanel(metric string) Panel {
	return Panel{
		Type: "line", Title: "Errors", RangeHours: 2,
		Series: []PanelSeries{{Metric: metric, Service: "api", Agg: AggSum}},
	}
}

func TestProposeApproveArchive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedMetric(t, s, "env-1", "api", "checkout_errors", KindCounter, "payment_method")

	first, err := s.ProposeDashboard(ctx, "env-1", DashboardInput{
		Name: "Checkout", Panels: []Panel{linePanel("checkout_errors")},
	}, "task-1", "flag-1", "agent:devops")
	if err != nil {
		t.Fatalf("ProposeDashboard: %v", err)
	}
	if first.Status != DashProposed {
		t.Errorf("a proposal must land inert, got status %q", first.Status)
	}
	if first.Panels[0].ID == "" {
		t.Error("panels need server-assigned ids; an agent has no way to invent stable ones")
	}

	flags, err := s.PendingDashboardFlagIDs(ctx, "env-1", first.ID)
	if err != nil {
		t.Fatalf("PendingDashboardFlagIDs: %v", err)
	}
	if len(flags) != 1 || flags[0] != "flag-1" {
		t.Errorf("pending flags = %v, want [flag-1] so approving resolves the review in one action", flags)
	}

	approvedDash, err := s.ApproveDashboard(ctx, "env-1", first.ID, "boss@example.com")
	if err != nil {
		t.Fatalf("ApproveDashboard: %v", err)
	}
	if approvedDash.Status != DashActive || approvedDash.ApprovedBy != "boss@example.com" {
		t.Errorf("approved dashboard = %+v", approvedDash)
	}

	second, err := s.ProposeDashboard(ctx, "env-1", DashboardInput{
		Name: "Checkout", Panels: []Panel{linePanel("checkout_errors")},
	}, "task-2", "", "agent:devops")
	if err != nil {
		t.Fatalf("ProposeDashboard (second): %v", err)
	}
	if _, err := s.ApproveDashboard(ctx, "env-1", second.ID, "boss@example.com"); err != nil {
		t.Fatalf("ApproveDashboard (second): %v", err)
	}
	all, err := s.ListDashboards(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListDashboards: %v", err)
	}
	active := 0
	for _, d := range all {
		if d.Status == DashActive {
			active++
		}
	}
	if active != 1 {
		t.Errorf("%d active dashboards named Checkout, want 1", active)
	}
}

func TestProposeRejectsPanelsAgainstTheRegistry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedMetric(t, s, "env-1", "api", "queue_depth", KindGauge, "queue")

	_, err := s.ProposeDashboard(ctx, "env-1", DashboardInput{
		Name: "Bad", Panels: []Panel{{
			Type: "line", Title: "Depth",
			Series: []PanelSeries{{Metric: "queue_depth", Service: "api", Agg: AggSum}},
		}},
	}, "", "", "agent:devops")
	if err == nil {
		t.Fatal("summing a gauge should be rejected at propose time, not discovered on the screen")
	}
	if !strings.Contains(err.Error(), "not a meaningful aggregation") {
		t.Errorf("unhelpful rejection: %v", err)
	}
}

func TestResolvePanelGroupsByLabel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "checkout_errors", KindCounter, "payment_method")

	now := time.Now().UTC()
	b1 := TruncateBucket(now.Add(-20 * time.Minute))
	b2 := TruncateBucket(now.Add(-10 * time.Minute))
	pts := []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Labels: map[string]string{"payment_method": "visa"}, Bucket: b1, Value: 2},
		{MetricID: m.ID, Kind: KindCounter, Labels: map[string]string{"payment_method": "visa"}, Bucket: b2, Value: 3},
		{MetricID: m.ID, Kind: KindCounter, Labels: map[string]string{"payment_method": "amex"}, Bucket: b2, Value: 10},
	}
	if err := s.WriteCustomPoints(ctx, pts); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	panel := Panel{
		Type: "bar", Title: "By method", GroupBy: "payment_method", RangeHours: 1,
		Series: []PanelSeries{{Metric: "checkout_errors", Service: "api", Agg: AggSum}},
	}
	reg := NewMetricMap([]Metric{m})
	data, err := s.ResolvePanel(ctx, panel, reg, now)
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != 2 {
		t.Fatalf("got %d series, want one per payment_method: %+v", len(data.Series), data.Series)
	}
	if data.Series[0].Label != "amex" || data.Series[0].Value != 10 {
		t.Errorf("first series = %+v, want amex at 10", data.Series[0])
	}
	if data.Series[1].Label != "visa" || data.Series[1].Value != 5 {
		t.Errorf("second series = %+v, want visa at 5", data.Series[1])
	}
	if data.TimeAxis {
		t.Error("a bar chart collapses the range; it must not claim a time axis")
	}
}

func TestResolvePanelKeepsGapsForGauges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "queue_depth", KindGauge)

	now := time.Now().UTC()
	bucket := TruncateBucket(now.Add(-10 * time.Minute))
	if err := s.WriteCustomPoints(ctx, []CustomPoint{
		{MetricID: m.ID, Kind: KindGauge, Bucket: bucket, Value: 7},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	panel := Panel{
		Type: "line", Title: "Depth", RangeHours: 1,
		Series: []PanelSeries{{Metric: "queue_depth", Service: "api", Agg: AggLast}},
	}
	data, err := s.ResolvePanel(ctx, panel, NewMetricMap([]Metric{m}), now)
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != 1 {
		t.Fatalf("got %d series, want 1", len(data.Series))
	}
	filled, nils := 0, 0
	for _, p := range data.Series[0].Points {
		if p == nil {
			nils++
			continue
		}
		filled++
		if *p != 7 {
			t.Errorf("point = %v, want 7", *p)
		}
	}
	if filled != 1 {
		t.Errorf("%d buckets carry a value, want exactly 1", filled)
	}
	if nils == 0 {
		t.Error("unobserved buckets must stay nil so the chart can draw a gap")
	}
}

func TestResolveStatComparesWithThePreviousWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "orders", KindCounter)

	now := time.Now().UTC()
	current := TruncateBucket(now.Add(-10 * time.Minute))
	previous := TruncateBucket(now.Add(-90 * time.Minute))
	if err := s.WriteCustomPoints(ctx, []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Bucket: current, Value: 12},
		{MetricID: m.ID, Kind: KindCounter, Bucket: previous, Value: 4},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	panel := Panel{
		Type: "stat", Title: "Orders", RangeHours: 1,
		Series: []PanelSeries{{Metric: "orders", Service: "api", Agg: AggSum}},
	}
	data, err := s.ResolvePanel(ctx, panel, NewMetricMap([]Metric{m}), now)
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != 1 || data.Series[0].Value != 12 {
		t.Fatalf("series = %+v, want a single value of 12", data.Series)
	}
	if data.Series[0].Previous == nil || *data.Series[0].Previous != 4 {
		t.Errorf("previous = %v, want 4 — the comparison is the whole point of a stat", data.Series[0].Previous)
	}
}

func TestResolvePanelFoldsTheTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "checkout_errors", KindCounter, "payment_method")

	now := time.Now().UTC()
	bucket := TruncateBucket(now.Add(-10 * time.Minute))
	pts := []CustomPoint{}
	for i := 0; i < MaxSeriesPerPanel+4; i++ {
		pts = append(pts, CustomPoint{
			MetricID: m.ID, Kind: KindCounter,
			Labels: map[string]string{"payment_method": string(rune('a' + i))},
			Bucket: bucket, Value: float64(i + 1),
		})
	}
	if err := s.WriteCustomPoints(ctx, pts); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	panel := Panel{
		Type: "bar", Title: "By method", GroupBy: "payment_method", RangeHours: 1,
		Series: []PanelSeries{{Metric: "checkout_errors", Service: "api", Agg: AggSum}},
	}
	data, err := s.ResolvePanel(ctx, panel, NewMetricMap([]Metric{m}), now)
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != MaxSeriesPerPanel+1 {
		t.Fatalf("got %d series, want %d plus one `other`", len(data.Series), MaxSeriesPerPanel)
	}
	last := data.Series[len(data.Series)-1]
	if last.Label != OtherLabelValue {
		t.Errorf("tail series = %q, want %q", last.Label, OtherLabelValue)
	}
	if last.Value != 10 {
		t.Errorf("`other` = %v, want 10 (the folded tail, not a rounding of it)", last.Value)
	}
	if data.Note == "" {
		t.Error("folding must be stated on the panel; a chart that silently drops groups is the failure mode")
	}
}

func TestResolvePanelReportsAMetricThatWentAway(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "orders", KindCounter)
	panel := linePanel("orders")

	rejected := MetricRejected
	if _, err := s.UpdateCustomMetric(ctx, "env-1", m.ID, MetricPatch{Status: &rejected}, "boss"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	metrics, err := s.ListCustomMetrics(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListCustomMetrics: %v", err)
	}
	data, err := s.ResolvePanel(ctx, panel, NewMetricMap(metrics), time.Now().UTC())
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != 0 {
		t.Errorf("a rejected metric must not render: %+v", data.Series)
	}
	if !strings.Contains(data.Note, "orders") {
		t.Errorf("note = %q, want it to name the missing metric", data.Note)
	}
}

func TestUpdateDashboardKeepsStatusAndRevalidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedMetric(t, s, "env-1", "api", "orders", KindCounter)
	seedMetric(t, s, "env-1", "api", "queue_depth", KindGauge)

	d, err := s.ProposeDashboard(ctx, "env-1", DashboardInput{
		Name: "Ops", Panels: []Panel{linePanel("orders")},
	}, "", "", "agent:devops")
	if err != nil {
		t.Fatalf("ProposeDashboard: %v", err)
	}
	if _, err := s.ApproveDashboard(ctx, "env-1", d.ID, "boss"); err != nil {
		t.Fatalf("ApproveDashboard: %v", err)
	}

	updated, err := s.UpdateDashboard(ctx, "env-1", d.ID, DashboardInput{
		Name: "Ops", Panels: []Panel{linePanel("orders"), {
			Type: "stat", Title: "Depth",
			Series: []PanelSeries{{Metric: "queue_depth", Service: "api", Agg: AggLast}},
		}},
	})
	if err != nil {
		t.Fatalf("UpdateDashboard: %v", err)
	}
	if updated.Status != DashActive {
		t.Errorf("editing an active dashboard sent it back to %q; the editor is already the gate", updated.Status)
	}
	if len(updated.Panels) != 2 {
		t.Errorf("got %d panels, want 2", len(updated.Panels))
	}

	if _, err := s.UpdateDashboard(ctx, "env-1", d.ID, DashboardInput{
		Name: "Ops", Panels: []Panel{{
			Type: "line", Title: "Bad",
			Series: []PanelSeries{{Metric: "queue_depth", Service: "api", Agg: AggSum}},
		}},
	}); err == nil {
		t.Error("the editor must validate too — otherwise the catalog only binds agents")
	}
}

func TestDashboardNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.GetDashboard(ctx, "env-1", "nope"); err == nil {
		t.Error("expected ErrDashboardNotFound")
	}
	if err := s.DeleteDashboard(ctx, "env-1", "nope"); err == nil {
		t.Error("expected ErrDashboardNotFound on delete")
	}
}

func TestResolveBarLastIsChronological(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "queue_depth", KindGauge, "queue")

	now := time.Now().UTC()
	for i, v := range []float64{5, 9, 2} {
		bucket := TruncateBucket(now.Add(time.Duration(-30+i*10) * time.Minute))
		if err := s.WriteCustomPoints(ctx, []CustomPoint{{
			MetricID: m.ID, Kind: KindGauge, Bucket: bucket, Value: v,
			Labels: map[string]string{"queue": "emails"},
		}}); err != nil {
			t.Fatalf("WriteCustomPoints: %v", err)
		}
	}

	panel := Panel{
		Type: "bar", Title: "Depth", GroupBy: "queue", RangeHours: 1,
		Series: []PanelSeries{{Metric: "queue_depth", Service: "api", Agg: AggLast}},
	}
	for i := 0; i < 10; i++ {
		data, err := s.ResolvePanel(ctx, panel, NewMetricMap([]Metric{m}), now)
		if err != nil {
			t.Fatalf("ResolvePanel: %v", err)
		}
		if len(data.Series) != 1 || data.Series[0].Value != 2 {
			t.Fatalf("run %d: series = %+v, want the newest bucket's 2", i, data.Series)
		}
	}
}

func TestResolvePanelFillsInteriorCounterGapsOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMetric(t, s, "env-1", "api", "checkout_errors", KindCounter)

	now := time.Now().UTC()
	at := func(min int) time.Time { return TruncateBucket(now.Add(time.Duration(-min) * time.Minute)) }
	if err := s.WriteCustomPoints(ctx, []CustomPoint{
		{MetricID: m.ID, Kind: KindCounter, Bucket: at(30), Value: 4},
		{MetricID: m.ID, Kind: KindCounter, Bucket: at(20), Value: 2},
		{MetricID: m.ID, Kind: KindCounter, Bucket: at(10), Value: 6},
	}); err != nil {
		t.Fatalf("WriteCustomPoints: %v", err)
	}

	panel := Panel{
		Type: "line", Title: "Errors", RangeHours: 1,
		Series: []PanelSeries{{Metric: "checkout_errors", Service: "api", Agg: AggSum}},
	}
	data, err := s.ResolvePanel(ctx, panel, NewMetricMap([]Metric{m}), now)
	if err != nil {
		t.Fatalf("ResolvePanel: %v", err)
	}
	if len(data.Series) != 1 {
		t.Fatalf("got %d series, want 1", len(data.Series))
	}
	pts := data.Series[0].Points

	first, last := -1, -1
	for i, p := range pts {
		if p != nil && *p != 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last <= first {
		t.Fatalf("expected three observed buckets, got points %v", pts)
	}
	for i := first; i <= last; i++ {
		if pts[i] == nil {
			t.Errorf("bucket %d inside the reporting window is nil; a counter that saw nothing saw zero", i)
		}
	}
	if first == 0 {
		t.Fatal("the window must start before the first observation for this test to mean anything")
	}
	for i := 0; i < first; i++ {
		if pts[i] != nil {
			t.Errorf("bucket %d predates the first observation and must stay nil, got %v", i, *pts[i])
		}
	}
	if last == len(pts)-1 {
		t.Fatal("the window must extend past the last observation for this test to mean anything")
	}
	for i := last + 1; i < len(pts); i++ {
		if pts[i] != nil {
			t.Errorf("bucket %d is after the last observation and must stay nil, got %v", i, *pts[i])
		}
	}
	if data.Series[0].Value != 12 {
		t.Errorf("collapsed value = %v, want 12", data.Series[0].Value)
	}
}
