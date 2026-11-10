package ingest

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	otlptrace "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const maxSamplesPerBucket = 2000

type aggKey struct {
	envID   string
	service string
	bucket  time.Time
}

type agg struct {
	durations []float64
	// Counts every span, including those past the sample cap, so throughput stays exact.
	seen   int64
	errors int64
	gauges map[envmetrics.Signal]float64
}

type PointWriter interface {
	WritePoints(ctx context.Context, pts []envmetrics.Point) error
}

type OTLPDecoder struct {
	writer PointWriter

	mu   sync.Mutex
	aggs map[aggKey]*agg

	custom     CustomStore
	customAggs map[customKey]*customAgg
	cumulative map[cumKey]float64
	dropped    int
}

func NewOTLPDecoder(w PointWriter) *OTLPDecoder {
	return &OTLPDecoder{
		writer:     w,
		aggs:       map[aggKey]*agg{},
		customAggs: map[customKey]*customAgg{},
		cumulative: map[cumKey]float64{},
	}
}

func (d *OTLPDecoder) Decode(envID, kind, contentType string, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	switch kind {
	case "traces":
		return d.decodeTraces(envID, contentType, body)
	case "metrics":
		return d.decodeMetrics(envID, contentType, body)
	case "logs":
		// Accept and drop: rejecting logs would make the sender's collector retry forever.
		return nil
	}
	return apperr.Invalid("ingest.errors.unknown_signal", "kind", kind)
}

// JSON must go through protojson: encoding/json silently yields an empty message.
// DiscardUnknown so fields added by newer SDKs do not fail the request.
func unmarshal(contentType string, body []byte, msg proto.Message) error {
	if strings.Contains(contentType, "json") {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, msg); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, "ingest.errors.otlp_json")
		}
		return nil
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "ingest.errors.otlp_protobuf")
	}
	return nil
}

func (d *OTLPDecoder) decodeTraces(envID, contentType string, body []byte) error {
	var req tracepb.ExportTraceServiceRequest
	if err := unmarshal(contentType, body, &req); err != nil {
		return apperr.Wrap(err, "ingest.errors.parse_traces")
	}
	for _, rs := range req.GetResourceSpans() {
		service := serviceName(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				d.observeSpan(envID, service, span)
			}
		}
	}
	return nil
}

// Only SERVER and CONSUMER spans: client spans are the same work seen from the caller and would
// double throughput.
func (d *OTLPDecoder) observeSpan(envID, service string, span *otlptrace.Span) {
	kind := span.GetKind()
	if kind != otlptrace.Span_SPAN_KIND_SERVER && kind != otlptrace.Span_SPAN_KIND_CONSUMER {
		return
	}
	start, end := span.GetStartTimeUnixNano(), span.GetEndTimeUnixNano()
	if end < start {
		return
	}
	ms := float64(end-start) / 1e6
	ts := time.Unix(0, int64(start)).UTC() //nolint:gosec // nanosecond clock from the wire; overflow only past year 2262

	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.bucketLocked(envID, service, ts)
	a.seen++
	if len(a.durations) < maxSamplesPerBucket {
		a.durations = append(a.durations, ms)
	}
	if isError(span) {
		a.errors++
	}
}

// Fall back to the HTTP status when the span status is UNSET: a handler that catches its exception
// and returns 500 leaves it UNSET.
func isError(span *otlptrace.Span) bool {
	switch span.GetStatus().GetCode() {
	case otlptrace.Status_STATUS_CODE_ERROR:
		return true
	case otlptrace.Status_STATUS_CODE_OK:
		return false
	case otlptrace.Status_STATUS_CODE_UNSET:
	}
	for _, kv := range span.GetAttributes() {
		switch kv.GetKey() {
		case "http.response.status_code", "http.status_code":
			if code := kv.GetValue().GetIntValue(); code >= 500 {
				return true
			}
		}
	}
	return false
}

func serviceName(attrs []*commonpb.KeyValue) string {
	for _, kv := range attrs {
		if kv.GetKey() == "service.name" {
			if v := strings.TrimSpace(kv.GetValue().GetStringValue()); v != "" {
				return v
			}
		}
	}
	return "unknown"
}

func (d *OTLPDecoder) bucketLocked(envID, service string, ts time.Time) *agg {
	k := aggKey{envID: envID, service: service, bucket: envmetrics.TruncateBucket(ts)}
	a := d.aggs[k]
	if a == nil {
		a = &agg{gauges: map[envmetrics.Signal]float64{}}
		d.aggs[k] = a
	}
	return a
}

// Lists both current and older semconv names, since SDKs in the wild emit either. Utilization
// arrives as a 0..1 ratio; scale is to percent.
var gaugeInstruments = map[string]struct {
	signal envmetrics.Signal
	scale  float64
}{
	"system.cpu.utilization":                 {envmetrics.SignalCPU, 100},
	"process.cpu.utilization":                {envmetrics.SignalCPU, 100},
	"container.cpu.utilization":              {envmetrics.SignalCPU, 100},
	"system.memory.utilization":              {envmetrics.SignalMemory, 100},
	"process.memory.utilization":             {envmetrics.SignalMemory, 100},
	"process.runtime.jvm.memory.utilization": {envmetrics.SignalMemory, 100},
}

func (d *OTLPDecoder) decodeMetrics(envID, contentType string, body []byte) error {
	var req metricspb.ExportMetricsServiceRequest
	if err := unmarshal(contentType, body, &req); err != nil {
		return apperr.Wrap(err, "ingest.errors.parse_metrics")
	}
	for _, rm := range req.GetResourceMetrics() {
		service := serviceName(rm.GetResource().GetAttributes())
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				// Known instruments must not also register as custom metrics, or CPU shows twice.
				if spec, ok := gaugeInstruments[m.GetName()]; ok {
					d.observeGauge(envID, service, m, spec.signal, spec.scale)
					continue
				}
				d.observeCustom(envID, service, m)
			}
		}
	}
	return nil
}

func (d *OTLPDecoder) observeGauge(envID, service string, m *otlpmetrics.Metric, sig envmetrics.Signal, scale float64) {
	var points []*otlpmetrics.NumberDataPoint
	switch data := m.GetData().(type) {
	case *otlpmetrics.Metric_Gauge:
		points = data.Gauge.GetDataPoints()
	case *otlpmetrics.Metric_Sum:
		points = data.Sum.GetDataPoints()
	default:
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range points {
		var v float64
		switch n := p.GetValue().(type) {
		case *otlpmetrics.NumberDataPoint_AsDouble:
			v = n.AsDouble
		case *otlpmetrics.NumberDataPoint_AsInt:
			v = float64(n.AsInt)
		default:
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		ts := time.Unix(0, int64(p.GetTimeUnixNano())).UTC() //nolint:gosec // see observeSpan
		d.bucketLocked(envID, service, ts).gauges[sig] = v * scale
	}
}

// Hold back the open bucket: flushing it early writes a partial count that a later flush
// overwrites.
func (d *OTLPDecoder) Flush(ctx context.Context) error {
	cutoff := envmetrics.TruncateBucket(time.Now().UTC())

	d.mu.Lock()
	ready := map[aggKey]*agg{}
	for k, a := range d.aggs {
		if k.bucket.Before(cutoff) {
			ready[k] = a
			delete(d.aggs, k)
		}
	}
	d.mu.Unlock()

	if len(ready) == 0 {
		return d.flushCustom(ctx, false)
	}
	pts := make([]envmetrics.Point, 0, len(ready)*5)
	for k, a := range ready {
		pts = append(pts, a.points(k)...)
	}
	if err := d.writer.WritePoints(ctx, pts); err != nil {
		return err
	}
	return d.flushCustom(ctx, false)
}

func (a *agg) points(k aggKey) []envmetrics.Point {
	at := func(sig envmetrics.Signal, v float64) envmetrics.Point {
		return envmetrics.Point{
			EnvironmentID: k.envID, Service: k.service,
			Signal: sig, Bucket: k.bucket, Value: v,
		}
	}
	out := []envmetrics.Point{}
	if a.seen > 0 {
		out = append(out,
			at(envmetrics.SignalThroughput, float64(a.seen)/envmetrics.BucketWidth.Seconds()),
			at(envmetrics.SignalErrorRate, float64(a.errors)/float64(a.seen)*100),
		)
	}
	if len(a.durations) > 0 {
		sort.Float64s(a.durations)
		out = append(out,
			at(envmetrics.SignalLatencyP50, percentile(a.durations, 0.50)),
			at(envmetrics.SignalLatencyP95, percentile(a.durations, 0.95)),
		)
	}
	for sig, v := range a.gauges {
		out = append(out, at(sig, v))
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (d *OTLPDecoder) RunFlusher(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := d.flushAll(flushCtx); err != nil && !errors.Is(err, context.Canceled) {
				logx.Warn(err, "ingest.log.final_rollup_flush")
			}
			cancel()
			return
		case <-t.C:
			if err := d.Flush(ctx); err != nil {
				logx.Warn(err, "ingest.log.rollup_flush")
			}
		}
	}
}

// Ignores the cutoff; only correct at shutdown.
func (d *OTLPDecoder) flushAll(ctx context.Context) error {
	d.mu.Lock()
	ready := d.aggs
	d.aggs = map[aggKey]*agg{}
	d.mu.Unlock()

	pts := make([]envmetrics.Point, 0, len(ready)*5)
	for k, a := range ready {
		pts = append(pts, a.points(k)...)
	}
	if len(pts) > 0 {
		if err := d.writer.WritePoints(ctx, pts); err != nil {
			return err
		}
	}
	return d.flushCustom(ctx, true)
}
