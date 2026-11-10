package envmetrics

import (
	"context"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type ChartType struct {
	Type        string       `json:"type"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	MinSeries   int          `json:"min_series"`
	MaxSeries   int          `json:"max_series"`
	GroupBy     GroupByRule  `json:"group_by"`
	Kinds       []MetricKind `json:"kinds,omitempty"`
	TimeAxis    bool         `json:"time_axis"`

	titleKey, descriptionKey string
}

type GroupByRule string

const (
	GroupByForbidden GroupByRule = "forbidden"
	GroupByOptional  GroupByRule = "optional"
	GroupByRequired  GroupByRule = "required"
)

var chartCatalog = []ChartType{
	{
		Type:     "line",
		titleKey: "envmetrics.charts.line_title", descriptionKey: "envmetrics.charts.line_description",
		MinSeries: 1, MaxSeries: 4,
		GroupBy:  GroupByOptional,
		TimeAxis: true,
	},
	{
		Type:     "area",
		titleKey: "envmetrics.charts.area_title", descriptionKey: "envmetrics.charts.area_description",
		MinSeries: 1, MaxSeries: 1,
		GroupBy:  GroupByRequired,
		Kinds:    []MetricKind{KindCounter},
		TimeAxis: true,
	},
	{
		Type:     "bar",
		titleKey: "envmetrics.charts.bar_title", descriptionKey: "envmetrics.charts.bar_description",
		MinSeries: 1, MaxSeries: 1,
		GroupBy:  GroupByRequired,
		TimeAxis: false,
	},
	{
		Type:     "stat",
		titleKey: "envmetrics.charts.stat_title", descriptionKey: "envmetrics.charts.stat_description",
		MinSeries: 1, MaxSeries: 1,
		GroupBy:  GroupByForbidden,
		TimeAxis: true,
	},
	{
		Type:     "table",
		titleKey: "envmetrics.charts.table_title", descriptionKey: "envmetrics.charts.table_description",
		MinSeries: 1, MaxSeries: 6,
		GroupBy:  GroupByRequired,
		TimeAxis: false,
	},
}

// Rendered once at init in the daemon's language; request handlers must use ChartTypesFor.
var ChartTypes = ChartTypesFor(context.Background())

func ChartTypesFor(ctx context.Context) []ChartType {
	out := make([]ChartType, len(chartCatalog))
	copy(out, chartCatalog)
	for i := range out {
		out[i].Title = i18n.TCtx(ctx, out[i].titleKey)
		out[i].Description = i18n.TCtx(ctx, out[i].descriptionKey)
	}
	return out
}

func ChartTypeByName(t string) (ChartType, bool) {
	for i := range ChartTypes {
		if ChartTypes[i].Type == t {
			return ChartTypes[i], true
		}
	}
	return ChartType{}, false
}

func (c ChartType) acceptsKind(k MetricKind) bool {
	if len(c.Kinds) == 0 {
		return true
	}
	for _, want := range c.Kinds {
		if want == k {
			return true
		}
	}
	return false
}

type Agg string

const (
	AggSum  Agg = "sum"
	AggAvg  Agg = "avg"
	AggMax  Agg = "max"
	AggMin  Agg = "min"
	AggLast Agg = "last"
)

// Summing gauges or taking the last value of a counter yields plausible but meaningless numbers.
var aggsByKind = map[MetricKind][]Agg{
	KindCounter: {AggSum, AggAvg, AggMax, AggMin},
	KindGauge:   {AggAvg, AggMax, AggMin, AggLast},
}

func AggsFor(k MetricKind) []Agg { return aggsByKind[k] }

func aggAllowed(k MetricKind, a Agg) bool {
	for _, ok := range aggsByKind[k] {
		if ok == a {
			return true
		}
	}
	return false
}

type PanelSeries struct {
	Metric  string            `json:"metric"`
	Service string            `json:"service"`
	Agg     Agg               `json:"agg"`
	Where   map[string]string `json:"where,omitempty"`
	Label   string            `json:"label,omitempty"`
}

type Panel struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"`
	Title      string        `json:"title"`
	Series     []PanelSeries `json:"series"`
	GroupBy    string        `json:"group_by,omitempty"`
	RangeHours int           `json:"range_hours,omitempty"`
	Unit       string        `json:"unit,omitempty"`
	Width      int           `json:"width,omitempty"`
}

type MetricLookup interface {
	Find(service, name string) (Metric, bool)
}

type MetricMap map[string]Metric

func metricKey(service, name string) string { return service + "\x00" + name }

func (m MetricMap) Find(service, name string) (Metric, bool) {
	v, ok := m[metricKey(service, name)]
	return v, ok
}

func NewMetricMap(metrics []Metric) MetricMap {
	out := make(MetricMap, len(metrics))
	for i := range metrics {
		out[metricKey(metrics[i].Service, metrics[i].Name)] = metrics[i]
	}
	return out
}

var ErrPanelInvalid error = apperr.Invalid("envmetrics.errors.invalid_panel")

func panelErr(key string, args ...any) error {
	return apperr.WrapKind(apperr.KindInvalid, apperr.Invalid(key, args...), "envmetrics.errors.invalid_panel")
}

func ValidatePanel(p Panel, reg MetricLookup) error {
	where := p.Title
	if where == "" {
		where = p.ID
	}
	if where == "" {
		where = p.Type
	}

	spec, ok := ChartTypeByName(p.Type)
	if !ok {
		return panelErr("envmetrics.errors.panel_unknown_chart", "panel", where, "type", p.Type, "types", chartTypeNames())
	}
	if len(p.Series) < spec.MinSeries {
		return panelErr("envmetrics.errors.panel_too_few_series", "panel", where, "type", spec.Type, "min", spec.MinSeries, "got", len(p.Series))
	}
	if len(p.Series) > spec.MaxSeries {
		return panelErr("envmetrics.errors.panel_too_many_series", "panel", where, "type", spec.Type, "max", spec.MaxSeries, "got", len(p.Series))
	}

	if err := validateGroupBy(spec, p, where); err != nil {
		return err
	}
	for i := range p.Series {
		if err := validateSeries(spec, p, p.Series[i], i, where, reg); err != nil {
			return err
		}
	}
	if p.RangeHours < 0 {
		return panelErr("envmetrics.errors.panel_negative_range", "panel", where)
	}
	if p.RangeHours > maxRangeHours {
		return panelErr("envmetrics.errors.panel_range_past_retention", "panel", where, "hours", p.RangeHours, "max", maxRangeHours)
	}
	return nil
}

func validateGroupBy(spec ChartType, p Panel, where string) error {
	switch spec.GroupBy {
	case GroupByRequired:
		if strings.TrimSpace(p.GroupBy) == "" {
			return panelErr("envmetrics.errors.panel_group_by_required", "panel", where, "type", spec.Type)
		}
	case GroupByForbidden:
		if strings.TrimSpace(p.GroupBy) != "" {
			return panelErr("envmetrics.errors.panel_group_by_forbidden", "panel", where, "type", spec.Type, "label", p.GroupBy)
		}
	case GroupByOptional:
	}
	return nil
}

func validateSeries(spec ChartType, p Panel, s PanelSeries, i int, where string, reg MetricLookup) error {
	n := i + 1
	if strings.TrimSpace(s.Metric) == "" {
		return panelErr("envmetrics.errors.series_metric_required", "panel", where, "series", n)
	}
	m, found := reg.Find(s.Service, s.Metric)
	if !found {
		return panelErr("envmetrics.errors.series_metric_not_found", "panel", where, "series", n, "metric", s.Metric, "service", s.Service)
	}
	if m.Status != MetricApproved {
		return panelErr("envmetrics.errors.series_metric_not_approved",
			"panel", where, "series", n, "metric", s.Metric, "status", m.Status)
	}
	if !spec.acceptsKind(m.Kind) {
		return panelErr("envmetrics.errors.series_wrong_kind",
			"panel", where, "series", n, "type", spec.Type, "kinds", kindList(spec.Kinds), "metric", s.Metric, "kind", m.Kind)
	}
	if s.Agg == "" {
		return panelErr("envmetrics.errors.series_agg_required",
			"panel", where, "series", n, "metric", s.Metric, "kind", m.Kind, "aggs", aggList(aggsByKind[m.Kind]))
	}
	if !aggAllowed(m.Kind, s.Agg) {
		return panelErr("envmetrics.errors.series_agg_not_allowed",
			"panel", where, "series", n, "agg", s.Agg, "kind", m.Kind, "metric", s.Metric, "aggs", aggList(aggsByKind[m.Kind]))
	}
	if p.GroupBy != "" && !m.HasLabel(p.GroupBy) {
		if len(m.Labels) == 0 {
			return panelErr("envmetrics.errors.series_group_label_missing_no_labels",
				"panel", where, "series", n, "metric", s.Metric, "label", p.GroupBy)
		}
		return panelErr("envmetrics.errors.series_group_label_missing",
			"panel", where, "series", n, "metric", s.Metric, "label", p.GroupBy, "labels", labelList(m.Labels))
	}
	for k := range s.Where {
		if !m.HasLabel(k) {
			if len(m.Labels) == 0 {
				return panelErr("envmetrics.errors.series_filter_label_missing_no_labels",
					"panel", where, "series", n, "label", k, "metric", s.Metric)
			}
			return panelErr("envmetrics.errors.series_filter_label_missing",
				"panel", where, "series", n, "label", k, "metric", s.Metric, "labels", labelList(m.Labels))
		}
	}
	return nil
}

// 90 days: the retention the janitor keeps.
const maxRangeHours = 90 * 24

const DefaultRangeHours = 24

func chartTypeNames() string {
	names := make([]string, 0, len(ChartTypes))
	for i := range ChartTypes {
		names = append(names, ChartTypes[i].Type)
	}
	return strings.Join(names, ", ")
}

func kindList(kinds []MetricKind) string {
	if len(kinds) == 0 {
		return "counter, gauge"
	}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

func aggList(aggs []Agg) string {
	out := make([]string, 0, len(aggs))
	for _, a := range aggs {
		out = append(out, string(a))
	}
	return strings.Join(out, ", ")
}

func labelList(labels []string) string {
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}
