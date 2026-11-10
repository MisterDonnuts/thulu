package ingest

import (
	"context"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
)

type memCustom struct {
	metrics map[string]envmetrics.Metric
	points  []envmetrics.CustomPoint
	keys    map[string]map[string]bool
	status  map[string]envmetrics.MetricStatus
	limit   int
}

func newMemCustom() *memCustom {
	return &memCustom{
		metrics: map[string]envmetrics.Metric{},
		keys:    map[string]map[string]bool{},
		status:  map[string]envmetrics.MetricStatus{},
	}
}

func (m *memCustom) RegisterObservations(_ context.Context, envID string, obs []envmetrics.Observation) (map[string]envmetrics.Metric, int, error) {
	out := map[string]envmetrics.Metric{}
	over := 0
	for _, o := range obs {
		key := o.Service + "\x00" + o.Name
		cur, seen := m.metrics[key]
		if !seen {
			if len(m.metrics) >= envmetrics.MaxMetricsPerEnvironment {
				over++
				continue
			}
			limit := m.limit
			if limit == 0 {
				limit = envmetrics.DefaultLabelLimit
			}
			status := envmetrics.MetricDiscovered
			if s, ok := m.status[o.Name]; ok {
				status = s
			}
			cur = envmetrics.Metric{
				ID: "m-" + o.Name, EnvironmentID: envID, Service: o.Service, Name: o.Name,
				Kind: o.Kind, Unit: o.Unit, Labels: o.Labels, LabelLimit: limit, Status: status,
			}
		}
		for _, l := range o.Labels {
			if !cur.HasLabel(l) {
				cur.Labels = append(cur.Labels, l)
			}
		}
		m.metrics[key] = cur
		out[key] = cur
	}
	return out, over, nil
}

func (m *memCustom) WriteCustomPoints(_ context.Context, pts []envmetrics.CustomPoint) error {
	m.points = append(m.points, pts...)
	for _, p := range pts {
		if m.keys[p.MetricID] == nil {
			m.keys[p.MetricID] = map[string]bool{}
		}
		m.keys[p.MetricID][envmetrics.CanonicalLabels(p.Labels)] = true
	}
	return nil
}

func (m *memCustom) LabelKeysFor(_ context.Context, metricID string) (map[string]bool, error) {
	out := map[string]bool{}
	for k := range m.keys[metricID] {
		out[k] = true
	}
	return out, nil
}

func (m *memCustom) total(name string) float64 {
	sum := 0.0
	for _, p := range m.points {
		if p.MetricID == "m-"+name {
			sum += p.Value
		}
	}
	return sum
}

func customDecoder() (*OTLPDecoder, *memCustom) {
	cs := newMemCustom()
	d := NewOTLPDecoder(&memWriter{})
	d.SetCustomStore(cs)
	return d, cs
}

func metricPayload(t *testing.T, service string, metrics ...*otlpmetrics.Metric) []byte {
	t.Helper()
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otlpmetrics.ResourceMetrics{{
			Resource:     &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", service)}},
			ScopeMetrics: []*otlpmetrics.ScopeMetrics{{Metrics: metrics}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	return body
}

func gaugeMetric(name string, at time.Time, value float64, attrs ...*commonpb.KeyValue) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name: name,
		Data: &otlpmetrics.Metric_Gauge{Gauge: &otlpmetrics.Gauge{
			DataPoints: []*otlpmetrics.NumberDataPoint{{
				TimeUnixNano: uint64(at.UnixNano()),
				Attributes:   attrs,
				Value:        &otlpmetrics.NumberDataPoint_AsDouble{AsDouble: value},
			}},
		}},
	}
}

func sumMetric(name string, at time.Time, value float64, cumulative bool, attrs ...*commonpb.KeyValue) *otlpmetrics.Metric {
	temporality := otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	if cumulative {
		temporality = otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	}
	return &otlpmetrics.Metric{
		Name: name,
		Data: &otlpmetrics.Metric_Sum{Sum: &otlpmetrics.Sum{
			IsMonotonic:            true,
			AggregationTemporality: temporality,
			DataPoints: []*otlpmetrics.NumberDataPoint{{
				TimeUnixNano: uint64(at.UnixNano()),
				Attributes:   attrs,
				Value:        &otlpmetrics.NumberDataPoint_AsDouble{AsDouble: value},
			}},
		}},
	}
}

func TestCustom_UnknownMetricIsCapturedAsDiscovered(t *testing.T) {
	d, cs := customDecoder()
	body := metricPayload(t, "api", sumMetric("checkout_errors", past(), 3, false,
		strAttr("payment_method", "visa")))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	m, ok := cs.metrics["api\x00checkout_errors"]
	if !ok {
		t.Fatal("the metric was not registered")
	}
	if m.Kind != envmetrics.KindCounter {
		t.Errorf("kind = %q, want counter — a monotonic sum counts events", m.Kind)
	}
	if m.Status != envmetrics.MetricDiscovered {
		t.Errorf("status = %q, want discovered", m.Status)
	}
	if len(cs.points) != 1 || cs.points[0].Value != 3 {
		t.Fatalf("points = %+v, want one point of 3", cs.points)
	}
	if cs.points[0].Labels["payment_method"] != "visa" {
		t.Errorf("labels = %v, want payment_method=visa", cs.points[0].Labels)
	}
}

func TestCustom_NoRegistryMeansNoCapture(t *testing.T) {
	d := NewOTLPDecoder(&memWriter{})
	body := metricPayload(t, "api", gaugeMetric("queue_depth", past(), 5))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	d.mu.Lock()
	n := len(d.customAggs)
	d.mu.Unlock()
	if n != 0 {
		t.Errorf("%d series buffered with no registry wired", n)
	}
}

func TestCustom_FixedSignalsDoNotDoubleAsCustom(t *testing.T) {
	d, cs := customDecoder()
	body := metricPayload(t, "api", gaugeMetric("system.cpu.utilization", past(), 0.42))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(cs.metrics) != 0 {
		t.Errorf("a catalogued instrument also registered as custom: %+v", cs.metrics)
	}
}

func TestCustom_CumulativeCountersBecomeDeltas(t *testing.T) {
	d, cs := customDecoder()
	ctx := context.Background()

	first := metricPayload(t, "api", sumMetric("orders", past(), 100, true))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", first); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(cs.points) != 0 {
		t.Fatalf("the first cumulative observation produced %+v, want nothing", cs.points)
	}

	second := metricPayload(t, "api", sumMetric("orders", past(), 112, true))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", second); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := cs.total("orders"); got != 12 {
		t.Errorf("delta = %v, want 12", got)
	}

	third := metricPayload(t, "api", sumMetric("orders", past(), 4, true))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", third); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := cs.total("orders"); got != 16 {
		t.Errorf("total after a counter reset = %v, want 16 (12 + 4)", got)
	}
}

func TestCustom_GaugeKeepsTheNewestObservation(t *testing.T) {
	d, cs := customDecoder()
	at := envmetrics.TruncateBucket(past())
	body := metricPayload(t, "api",
		gaugeMetric("queue_depth", at, 5),
		gaugeMetric("queue_depth", at.Add(time.Second), 9),
	)
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(cs.points) != 1 || cs.points[0].Value != 9 {
		t.Errorf("points = %+v, want a single gauge of 9", cs.points)
	}
	if cs.metrics["api\x00queue_depth"].Kind != envmetrics.KindGauge {
		t.Error("a non-monotonic reading must register as a gauge")
	}
}

func TestCustom_RejectedMetricsStopCollecting(t *testing.T) {
	d, cs := customDecoder()
	cs.status["noisy"] = envmetrics.MetricRejected
	body := metricPayload(t, "api", sumMetric("noisy", past(), 1, false))
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(cs.points) != 0 {
		t.Errorf("a rejected metric kept collecting: %+v", cs.points)
	}
}

func TestCustom_CardinalityFoldsIntoOther(t *testing.T) {
	d, cs := customDecoder()
	cs.limit = 3
	ctx := context.Background()

	for _, v := range []string{"a", "b", "c", "d", "e"} {
		body := metricPayload(t, "api", sumMetric("hits", past(), 1, false, strAttr("tenant", v)))
		if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if err := d.Flush(ctx); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}

	if got := cs.total("hits"); got != 5 {
		t.Errorf("total = %v, want 5 — folding must preserve the sum", got)
	}
	folded := 0
	for _, p := range cs.points {
		if p.Labels["tenant"] == envmetrics.OtherLabelValue {
			folded++
		}
	}
	if folded != 2 {
		t.Errorf("%d points folded into %q, want 2", folded, envmetrics.OtherLabelValue)
	}
}

func TestCustom_RunawayLabelsCannotGrowUnbounded(t *testing.T) {
	d, _ := customDecoder()
	for i := 0; i < maxCumulativeSeries+50; i++ {
		body := metricPayload(t, "api", sumMetric("hits", past(), 1, true,
			strAttr("request_id", strconv.Itoa(i))))
		if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
			t.Fatalf("Decode: %v", err)
		}
	}
	d.mu.Lock()
	cum, dropped := len(d.cumulative), d.dropped
	d.mu.Unlock()

	if cum > maxCumulativeSeries {
		t.Errorf("cumulative table holds %d series, past the ceiling of %d", cum, maxCumulativeSeries)
	}
	if dropped == 0 {
		t.Error("series past the ceiling must be counted — a silent drop reads as a chart that stopped moving")
	}
}

func TestCustom_HistogramsAreSkipped(t *testing.T) {
	d, cs := customDecoder()
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otlpmetrics.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "api")}},
			ScopeMetrics: []*otlpmetrics.ScopeMetrics{{Metrics: []*otlpmetrics.Metric{{
				Name: "request.duration",
				Data: &otlpmetrics.Metric_Histogram{Histogram: &otlpmetrics.Histogram{
					DataPoints: []*otlpmetrics.HistogramDataPoint{{
						TimeUnixNano: uint64(past().UnixNano()),
						Count:        3,
					}},
				}},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("a histogram must be skipped, not rejected: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(cs.metrics) != 0 {
		t.Errorf("a histogram registered a metric: %+v", cs.metrics)
	}
}

func TestCustom_LabelsBeyondTheCeilingAreDroppedConsistently(t *testing.T) {
	attrs := []*commonpb.KeyValue{}
	for _, k := range []string{"z", "y", "x", "w", "v", "u", "t", "s"} {
		attrs = append(attrs, strAttr(k, "1"))
	}
	got := dataPointLabels(attrs)
	if len(got) != envmetrics.MaxLabelsPerMetric {
		t.Fatalf("kept %d labels, want %d", len(got), envmetrics.MaxLabelsPerMetric)
	}
	for _, k := range []string{"s", "t", "u", "v", "w", "x"} {
		if _, ok := got[k]; !ok {
			t.Errorf("label %q was dropped; the surviving set must be the first by name", k)
		}
	}
}
