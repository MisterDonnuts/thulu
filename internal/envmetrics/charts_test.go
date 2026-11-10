package envmetrics

import (
	"strings"
	"testing"
)

func approved(service, name string, kind MetricKind, labels ...string) Metric {
	return Metric{
		ID: name + "-id", Service: service, Name: name, Kind: kind,
		Labels: labels, Status: MetricApproved, LabelLimit: DefaultLabelLimit,
	}
}

func registry(metrics ...Metric) MetricMap { return NewMetricMap(metrics) }

func TestChartCatalogIsClosed(t *testing.T) {
	if _, ok := ChartTypeByName("sankey"); ok {
		t.Error("the catalog must not answer for a type it does not define")
	}
	for _, want := range []string{"line", "area", "bar", "stat", "table"} {
		if _, ok := ChartTypeByName(want); !ok {
			t.Errorf("catalog is missing %q", want)
		}
	}
	// Every catalog entry needs a client renderer; one missing there validates cleanly and draws
	// nothing.
	if len(ChartTypes) != 5 {
		t.Errorf("ChartTypes has %d entries, want 5", len(ChartTypes))
	}
}

func TestValidatePanel(t *testing.T) {
	reg := registry(
		approved("api", "checkout_errors", KindCounter, "payment_method"),
		approved("api", "queue_depth", KindGauge, "queue"),
		Metric{ID: "x", Service: "api", Name: "seats_sold", Kind: KindCounter, Status: MetricDiscovered},
	)

	tests := []struct {
		name    string
		panel   Panel
		wantErr string
	}{
		{
			name: "counter summed on a line chart",
			panel: Panel{Type: "line", Title: "Errors", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
		},
		{
			name: "grouped bar",
			panel: Panel{Type: "bar", Title: "By method", GroupBy: "payment_method",
				Series: []PanelSeries{{Metric: "checkout_errors", Service: "api", Agg: AggSum}}},
		},
		{
			name: "unknown chart type",
			panel: Panel{Type: "pie", Title: "P", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
			wantErr: "unknown chart type",
		},
		{
			name: "summing a gauge is meaningless",
			panel: Panel{Type: "line", Title: "Depth", Series: []PanelSeries{
				{Metric: "queue_depth", Service: "api", Agg: AggSum},
			}},
			wantErr: "not a meaningful aggregation",
		},
		{
			name: "last value of a counter",
			panel: Panel{Type: "line", Title: "Errors", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggLast},
			}},
			wantErr: "not a meaningful aggregation",
		},
		{
			name: "area needs a group",
			panel: Panel{Type: "area", Title: "Split", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
			wantErr: "needs group_by",
		},
		{
			name: "area refuses a gauge",
			panel: Panel{Type: "area", Title: "Split", GroupBy: "queue", Series: []PanelSeries{
				{Metric: "queue_depth", Service: "api", Agg: AggAvg},
			}},
			wantErr: "only accepts counter",
		},
		{
			name: "stat cannot group",
			panel: Panel{Type: "stat", Title: "Now", GroupBy: "payment_method", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
			wantErr: "cannot group by",
		},
		{
			name: "group by a label the metric does not have",
			panel: Panel{Type: "bar", Title: "By region", GroupBy: "region", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
			wantErr: `has no label "region"`,
		},
		{
			name: "filter on a label the metric does not have",
			panel: Panel{Type: "line", Title: "Errors", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum, Where: map[string]string{"tenant": "acme"}},
			}},
			wantErr: "filter on label",
		},
		{
			name: "unapproved metric",
			panel: Panel{Type: "line", Title: "Seats", Series: []PanelSeries{
				{Metric: "seats_sold", Service: "api", Agg: AggSum},
			}},
			wantErr: "not approved",
		},
		{
			name: "unknown metric",
			panel: Panel{Type: "line", Title: "Ghost", Series: []PanelSeries{
				{Metric: "nope", Service: "api", Agg: AggSum},
			}},
			wantErr: "no metric",
		},
		{
			name: "too many series for a stat",
			panel: Panel{Type: "stat", Title: "Two", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
				{Metric: "queue_depth", Service: "api", Agg: AggAvg},
			}},
			wantErr: "at most 1 series",
		},
		{
			name:    "no series at all",
			panel:   Panel{Type: "line", Title: "Empty"},
			wantErr: "at least 1 series",
		},
		{
			name: "missing agg names the allowed set",
			panel: Panel{Type: "line", Title: "Errors", Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api"},
			}},
			wantErr: "agg is required",
		},
		{
			name: "range past retention",
			panel: Panel{Type: "line", Title: "Errors", RangeHours: 10000, Series: []PanelSeries{
				{Metric: "checkout_errors", Service: "api", Agg: AggSum},
			}},
			wantErr: "retention horizon",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePanel(tc.panel, reg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the panel to validate, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidationErrorsNameTheAlternative(t *testing.T) {
	reg := registry(approved("api", "queue_depth", KindGauge, "queue"))
	err := ValidatePanel(Panel{Type: "line", Title: "Depth", Series: []PanelSeries{
		{Metric: "queue_depth", Service: "api", Agg: AggSum},
	}}, reg)
	if err == nil {
		t.Fatal("expected sum on a gauge to be rejected")
	}
	for _, want := range []string{"avg", "max", "min", "last"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection does not offer %q: %s", want, err)
		}
	}
}

func TestValidateDashboard(t *testing.T) {
	reg := registry(approved("api", "checkout_errors", KindCounter, "payment_method"))
	ok := DashboardInput{Name: "Checkout", Panels: []Panel{{
		Type: "line", Title: "Errors",
		Series: []PanelSeries{{Metric: "checkout_errors", Service: "api", Agg: AggSum}},
	}}}
	if err := ValidateDashboard(ok, reg); err != nil {
		t.Fatalf("expected the dashboard to validate: %v", err)
	}
	if err := ValidateDashboard(DashboardInput{Panels: ok.Panels}, reg); err == nil {
		t.Error("a dashboard with no name should be rejected")
	}
	if err := ValidateDashboard(DashboardInput{Name: "Empty"}, reg); err == nil {
		t.Error("a dashboard with no panels should be rejected")
	}
	tooMany := DashboardInput{Name: "Wall"}
	for i := 0; i <= MaxPanelsPerDashboard; i++ {
		tooMany.Panels = append(tooMany.Panels, ok.Panels[0])
	}
	if err := ValidateDashboard(tooMany, reg); err == nil {
		t.Errorf("more than %d panels should be rejected", MaxPanelsPerDashboard)
	}
}
