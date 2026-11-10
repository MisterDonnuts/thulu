package ingest

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	otlptrace "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
)

type memWriter struct{ pts []envmetrics.Point }

func (m *memWriter) WritePoints(_ context.Context, pts []envmetrics.Point) error {
	m.pts = append(m.pts, pts...)
	return nil
}

func (m *memWriter) value(t *testing.T, service string, sig envmetrics.Signal) float64 {
	t.Helper()
	for _, p := range m.pts {
		if p.Service == service && p.Signal == sig {
			return p.Value
		}
	}
	t.Fatalf("no point for service %q signal %q (have %d points)", service, sig, len(m.pts))
	return 0
}

func (m *memWriter) has(service string, sig envmetrics.Signal) bool {
	for _, p := range m.pts {
		if p.Service == service && p.Signal == sig {
			return true
		}
	}
	return false
}

func past() time.Time { return time.Now().UTC().Add(-30 * time.Minute) }

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{
		Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{
		Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

type spanSpec struct {
	kind    otlptrace.Span_SpanKind
	ms      float64
	status  otlptrace.Status_StatusCode
	httpNum int64
	at      time.Time
}

func traceBody(t *testing.T, service string, specs []spanSpec) []byte {
	t.Helper()
	spans := make([]*otlptrace.Span, 0, len(specs))
	for _, s := range specs {
		at := s.at
		if at.IsZero() {
			at = past()
		}
		sp := &otlptrace.Span{
			Name:              "GET /x",
			Kind:              s.kind,
			StartTimeUnixNano: uint64(at.UnixNano()),
			EndTimeUnixNano:   uint64(at.Add(time.Duration(s.ms * float64(time.Millisecond))).UnixNano()),
		}
		if s.status != otlptrace.Status_STATUS_CODE_UNSET {
			sp.Status = &otlptrace.Status{Code: s.status}
		}
		if s.httpNum != 0 {
			sp.Attributes = []*commonpb.KeyValue{intAttr("http.response.status_code", s.httpNum)}
		}
		spans = append(spans, sp)
	}
	req := &tracepb.ExportTraceServiceRequest{
		ResourceSpans: []*otlptrace.ResourceSpans{{
			Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", service)}},
			ScopeSpans: []*otlptrace.ScopeSpans{{Spans: spans}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal traces: %v", err)
	}
	return body
}

func decodeTraceSpans(t *testing.T, service string, specs []spanSpec) *memWriter {
	t.Helper()
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	if err := d.Decode("env-1", "traces", "application/x-protobuf", traceBody(t, service, specs)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return w
}

func TestOTLP_LatencyAndThroughputFromSpans(t *testing.T) {
	specs := make([]spanSpec, 0, 100)
	for i := 1; i <= 100; i++ {
		specs = append(specs, spanSpec{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: float64(i)})
	}
	w := decodeTraceSpans(t, "api", specs)

	if got := w.value(t, "api", envmetrics.SignalLatencyP50); got != 50 {
		t.Errorf("p50 = %v ms, want 50", got)
	}
	if got := w.value(t, "api", envmetrics.SignalLatencyP95); got != 95 {
		t.Errorf("p95 = %v ms, want 95", got)
	}
	want := 100 / envmetrics.BucketWidth.Seconds()
	if got := w.value(t, "api", envmetrics.SignalThroughput); got != want {
		t.Errorf("throughput = %v/s, want %v", got, want)
	}
}

func TestOTLP_ClientSpansDoNotInflateThroughput(t *testing.T) {
	w := decodeTraceSpans(t, "api", []spanSpec{
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 10},
		{kind: otlptrace.Span_SPAN_KIND_CLIENT, ms: 10},
		{kind: otlptrace.Span_SPAN_KIND_INTERNAL, ms: 10},
	})
	want := 1 / envmetrics.BucketWidth.Seconds()
	if got := w.value(t, "api", envmetrics.SignalThroughput); got != want {
		t.Errorf("throughput = %v/s, want %v (only the server span counts)", got, want)
	}
}

func TestOTLP_ErrorRateCountsHandledFiveHundreds(t *testing.T) {
	w := decodeTraceSpans(t, "api", []spanSpec{
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, httpNum: 200},
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, httpNum: 500},
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, httpNum: 503},
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, status: otlptrace.Status_STATUS_CODE_ERROR},
	})
	if got := w.value(t, "api", envmetrics.SignalErrorRate); got != 75 {
		t.Errorf("error rate = %v%%, want 75", got)
	}
}

func TestOTLP_ExplicitOKBeatsTheStatusCodeFallback(t *testing.T) {
	w := decodeTraceSpans(t, "api", []spanSpec{
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, httpNum: 500, status: otlptrace.Status_STATUS_CODE_OK},
	})
	if got := w.value(t, "api", envmetrics.SignalErrorRate); got != 0 {
		t.Errorf("error rate = %v%%, want 0", got)
	}
}

func TestOTLP_ZeroErrorsIsWrittenNotOmitted(t *testing.T) {
	w := decodeTraceSpans(t, "api", []spanSpec{
		{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, httpNum: 200},
	})
	if !w.has("api", envmetrics.SignalErrorRate) {
		t.Fatal("no error_rate point for a bucket that had traffic")
	}
	if got := w.value(t, "api", envmetrics.SignalErrorRate); got != 0 {
		t.Errorf("error rate = %v, want 0", got)
	}
}

func TestOTLP_ServicesAreKeptApart(t *testing.T) {
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	for _, svc := range []string{"api", "worker"} {
		if err := d.Decode("env-1", "traces", "application/x-protobuf",
			traceBody(t, svc, []spanSpec{{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 10}})); err != nil {
			t.Fatalf("Decode %s: %v", svc, err)
		}
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, svc := range []string{"api", "worker"} {
		if !w.has(svc, envmetrics.SignalThroughput) {
			t.Errorf("no throughput for %q", svc)
		}
	}
}

func TestOTLP_MissingServiceNameIsLabelledNotMerged(t *testing.T) {
	req := &tracepb.ExportTraceServiceRequest{
		ResourceSpans: []*otlptrace.ResourceSpans{{
			ScopeSpans: []*otlptrace.ScopeSpans{{Spans: []*otlptrace.Span{{
				Kind:              otlptrace.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: uint64(past().UnixNano()),
				EndTimeUnixNano:   uint64(past().Add(time.Millisecond).UnixNano()),
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	if err := d.Decode("env-1", "traces", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if !w.has("unknown", envmetrics.SignalThroughput) {
		t.Error("an unnamed service did not land under 'unknown'")
	}
}

func TestOTLP_OpenBucketIsHeldBack(t *testing.T) {
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	if err := d.Decode("env-1", "traces", "application/x-protobuf",
		traceBody(t, "api", []spanSpec{{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 5, at: time.Now().UTC()}})); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(w.pts) != 0 {
		t.Errorf("flushed %d points from the open bucket, want 0", len(w.pts))
	}
	if err := d.flushAll(context.Background()); err != nil {
		t.Fatalf("flushAll: %v", err)
	}
	if len(w.pts) == 0 {
		t.Error("shutdown flush dropped the in-progress bucket")
	}
}

func TestOTLP_MetricsGaugesAreScaledToPercent(t *testing.T) {
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otlpmetrics.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "api")}},
			ScopeMetrics: []*otlpmetrics.ScopeMetrics{{Metrics: []*otlpmetrics.Metric{{
				Name: "system.cpu.utilization",
				Data: &otlpmetrics.Metric_Gauge{Gauge: &otlpmetrics.Gauge{
					DataPoints: []*otlpmetrics.NumberDataPoint{{
						TimeUnixNano: uint64(past().UnixNano()),
						Value:        &otlpmetrics.NumberDataPoint_AsDouble{AsDouble: 0.42},
					}},
				}},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	if err := d.Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := w.value(t, "api", envmetrics.SignalCPU); got != 42 {
		t.Errorf("cpu = %v, want 42", got)
	}
}

func TestOTLP_UnknownInstrumentsAreIgnoredNotFatal(t *testing.T) {
	req := &metricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otlpmetrics.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "api")}},
			ScopeMetrics: []*otlpmetrics.ScopeMetrics{{Metrics: []*otlpmetrics.Metric{{
				Name: "some.vendor.specific.thing",
				Data: &otlpmetrics.Metric_Gauge{Gauge: &otlpmetrics.Gauge{
					DataPoints: []*otlpmetrics.NumberDataPoint{{
						Value: &otlpmetrics.NumberDataPoint_AsDouble{AsDouble: 1},
					}},
				}},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := &memWriter{}
	if err := NewOTLPDecoder(w).Decode("env-1", "metrics", "application/x-protobuf", body); err != nil {
		t.Errorf("unknown instrument returned an error: %v", err)
	}
}

func TestOTLP_LogsAreAcceptedAndDropped(t *testing.T) {
	if err := NewOTLPDecoder(&memWriter{}).Decode("env-1", "logs", "application/x-protobuf", []byte{0x0a, 0x00}); err != nil {
		t.Errorf("logs rejected: %v", err)
	}
}

func TestOTLP_GarbagePayloadIsAnError(t *testing.T) {
	err := NewOTLPDecoder(&memWriter{}).Decode("env-1", "traces", "application/x-protobuf",
		[]byte("this is definitely not protobuf"))
	if err == nil {
		t.Error("a malformed payload decoded without error")
	}
}

func TestOTLP_JSONEncodingIsAccepted(t *testing.T) {
	body := []byte(`{"resourceSpans":[{"resource":{"attributes":[
		{"key":"service.name","value":{"stringValue":"api"}}]},
		"scopeSpans":[{"spans":[{"kind":2,"startTimeUnixNano":"1000000000","endTimeUnixNano":"1010000000"}]}]}]}`)
	w := &memWriter{}
	d := NewOTLPDecoder(w)
	if err := d.Decode("env-1", "traces", "application/json", body); err != nil {
		t.Fatalf("Decode JSON: %v", err)
	}
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if !w.has("api", envmetrics.SignalThroughput) {
		t.Error("JSON payload produced no points")
	}
}

func TestOTLP_SampleCapDoesNotSkewThroughput(t *testing.T) {
	specs := make([]spanSpec, 0, maxSamplesPerBucket+500)
	for i := 0; i < maxSamplesPerBucket+500; i++ {
		specs = append(specs, spanSpec{kind: otlptrace.Span_SPAN_KIND_SERVER, ms: 10})
	}
	w := decodeTraceSpans(t, "api", specs)
	want := float64(maxSamplesPerBucket+500) / envmetrics.BucketWidth.Seconds()
	if got := w.value(t, "api", envmetrics.SignalThroughput); got != want {
		t.Errorf("throughput = %v/s, want %v", got, want)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for _, c := range []struct {
		p    float64
		want float64
	}{{0.5, 5}, {0.95, 10}, {0, 1}, {1, 10}} {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile of nothing = %v, want 0", got)
	}
}
