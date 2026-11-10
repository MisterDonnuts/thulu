package ingest

import (
	"context"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const maxMetricNameLen = 120

const maxLabelValueLen = 120

// The per-metric cardinality cap only runs at flush, and the cumulative table is never emptied;
// these ceilings bound memory.
// Past them new series are dropped and counted, while series already tracked keep working.
const (
	maxSeriesInFlight   = 20_000
	maxCumulativeSeries = 10_000
)

type CustomStore interface {
	RegisterObservations(ctx context.Context, envID string, obs []envmetrics.Observation) (map[string]envmetrics.Metric, int, error)
	WriteCustomPoints(ctx context.Context, pts []envmetrics.CustomPoint) error
	LabelKeysFor(ctx context.Context, metricID string) (map[string]bool, error)
}

type customKey struct {
	envID    string
	service  string
	name     string
	labelKey string
	bucket   time.Time
}

type cumKey struct {
	envID    string
	service  string
	name     string
	labelKey string
}

type customAgg struct {
	kind   envmetrics.MetricKind
	unit   string
	labels map[string]string
	sum    float64
	last   float64
	lastAt int64
	isSet  bool
}

func (d *OTLPDecoder) SetCustomStore(cs CustomStore) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.custom = cs
	if d.customAggs == nil {
		d.customAggs = map[customKey]*customAgg{}
	}
	if d.cumulative == nil {
		d.cumulative = map[cumKey]float64{}
	}
}

func (d *OTLPDecoder) observeCustom(envID, service string, m *otlpmetrics.Metric) {
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return
	}
	if len(name) > maxMetricNameLen {
		name = name[:maxMetricNameLen]
	}

	shape, ok := readShape(m)
	if !ok {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// Without a registry nothing ever flushes these, so buffering would leak.
	if d.custom == nil {
		return
	}
	if d.customAggs == nil {
		d.customAggs = map[customKey]*customAgg{}
	}
	if d.cumulative == nil {
		d.cumulative = map[cumKey]float64{}
	}
	for _, p := range shape.points {
		d.foldPointLocked(envID, service, name, shape, p)
	}
}

type metricShape struct {
	points     []*otlpmetrics.NumberDataPoint
	kind       envmetrics.MetricKind
	cumulative bool
	unit       string
}

func readShape(m *otlpmetrics.Metric) (metricShape, bool) {
	out := metricShape{unit: strings.TrimSpace(m.GetUnit())}
	switch data := m.GetData().(type) {
	case *otlpmetrics.Metric_Gauge:
		out.points, out.kind = data.Gauge.GetDataPoints(), envmetrics.KindGauge
	case *otlpmetrics.Metric_Sum:
		out.points, out.kind = data.Sum.GetDataPoints(), envmetrics.KindGauge
		if data.Sum.GetIsMonotonic() {
			out.kind = envmetrics.KindCounter
		}
		out.cumulative = data.Sum.GetAggregationTemporality() ==
			otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	default:
		return out, false
	}
	return out, true
}

func (d *OTLPDecoder) foldPointLocked(envID, service, name string, shape metricShape, p *otlpmetrics.NumberDataPoint) {
	v, ok := numberValue(p)
	if !ok {
		return
	}
	labels := dataPointLabels(p.GetAttributes())
	lk := labelKey(labels)

	if shape.kind == envmetrics.KindCounter && shape.cumulative {
		delta, emit := d.cumulativeDeltaLocked(cumKey{envID, service, name, lk}, v)
		if !emit {
			return
		}
		v = delta
	}

	k := customKey{envID: envID, service: service, name: name, labelKey: lk,
		bucket: envmetrics.TruncateBucket(pointTime(p))}
	a := d.customAggs[k]
	if a == nil {
		if len(d.customAggs) >= maxSeriesInFlight {
			d.dropped++
			return
		}
		a = &customAgg{kind: shape.kind, unit: shape.unit, labels: labels}
		d.customAggs[k] = a
	}
	a.isSet = true
	if a.unit == "" {
		a.unit = shape.unit
	}
	if shape.kind == envmetrics.KindCounter {
		a.sum += v
		return
	}
	if at := int64(p.GetTimeUnixNano()); at >= a.lastAt { //nolint:gosec // see observeSpan
		a.last, a.lastAt = v, at
	}
}

// An unset timestamp would land in 1970, outside every retention window and panel range, so read it
// as now.
func pointTime(p *otlpmetrics.NumberDataPoint) time.Time {
	nano := p.GetTimeUnixNano()
	if nano == 0 {
		return time.Now().UTC()
	}
	return time.Unix(0, int64(nano)).UTC() //nolint:gosec // see observeSpan
}

// A series' first value is the total since process start, so it emits nothing (else every deploy
// spikes).
// A value below the previous one means the counter reset, so the new value is the delta.
func (d *OTLPDecoder) cumulativeDeltaLocked(k cumKey, v float64) (float64, bool) {
	prev, seen := d.cumulative[k]
	if !seen && len(d.cumulative) >= maxCumulativeSeries {
		d.dropped++
		return 0, false
	}
	d.cumulative[k] = v
	if !seen {
		return 0, false
	}
	if v < prev {
		return v, true
	}
	return v - prev, true
}

func numberValue(p *otlpmetrics.NumberDataPoint) (float64, bool) {
	var v float64
	switch n := p.GetValue().(type) {
	case *otlpmetrics.NumberDataPoint_AsDouble:
		v = n.AsDouble
	case *otlpmetrics.NumberDataPoint_AsInt:
		v = float64(n.AsInt)
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func dataPointLabels(attrs []*commonpb.KeyValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := map[string]string{}
	names := make([]string, 0, len(attrs))
	byName := map[string]string{}
	for _, kv := range attrs {
		k := strings.TrimSpace(kv.GetKey())
		if k == "" {
			continue
		}
		v, ok := scalarValue(kv.GetValue())
		if !ok {
			continue
		}
		if len(v) > maxLabelValueLen {
			v = v[:maxLabelValueLen]
		}
		if _, dup := byName[k]; !dup {
			names = append(names, k)
		}
		byName[k] = v
	}
	// Sort before capping so every exporter keeps the same labels; otherwise one series splits in
	// two.
	sort.Strings(names)
	for i, k := range names {
		if i >= envmetrics.MaxLabelsPerMetric {
			break
		}
		out[k] = byName[k]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func scalarValue(v *commonpb.AnyValue) (string, bool) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue, true
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue), true
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10), true
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64), true
	default:
		return "", false
	}
}

func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\x1e')
		}
		b.WriteString(k)
		b.WriteByte('\x1f')
		b.WriteString(labels[k])
	}
	return b.String()
}

// Register before writing: registration applies the per-environment ceiling and assigns ids; a
// point written before its metric exists is unreachable.
func (d *OTLPDecoder) flushCustom(ctx context.Context, all bool) error {
	d.mu.Lock()
	cs := d.custom
	if cs == nil || len(d.customAggs) == 0 {
		d.mu.Unlock()
		return nil
	}
	cutoff := envmetrics.TruncateBucket(time.Now().UTC())
	ready := map[customKey]*customAgg{}
	for k, a := range d.customAggs {
		if all || k.bucket.Before(cutoff) {
			ready[k] = a
			delete(d.customAggs, k)
		}
	}
	dropped := d.dropped
	d.dropped = 0
	d.mu.Unlock()

	if dropped > 0 {
		log.Printf("%s", i18n.Log("ingest.log.dropped_custom_series", "count", dropped))
	}

	if len(ready) == 0 {
		return nil
	}

	byEnv := map[string][]customKey{}
	for k := range ready {
		byEnv[k.envID] = append(byEnv[k.envID], k)
	}
	for envID, keys := range byEnv {
		if err := d.flushCustomEnv(ctx, cs, envID, keys, ready); err != nil {
			return err
		}
	}
	return nil
}

func (d *OTLPDecoder) flushCustomEnv(ctx context.Context, cs CustomStore, envID string, keys []customKey, ready map[customKey]*customAgg) error {
	obs := map[string]envmetrics.Observation{}
	for _, k := range keys {
		a := ready[k]
		id := k.service + "\x00" + k.name
		o, seen := obs[id]
		if !seen {
			o = envmetrics.Observation{Service: k.service, Name: k.name, Kind: a.kind, Unit: a.unit}
		}
		for name := range a.labels {
			if !containsString(o.Labels, name) {
				o.Labels = append(o.Labels, name)
			}
		}
		obs[id] = o
	}
	list := make([]envmetrics.Observation, 0, len(obs))
	for _, o := range obs {
		list = append(list, o)
	}
	registry, _, err := cs.RegisterObservations(ctx, envID, list)
	if err != nil {
		return err
	}

	seenKeys := map[string]map[string]bool{}
	pts := make([]envmetrics.CustomPoint, 0, len(keys))
	for _, k := range keys {
		a := ready[k]
		m, known := registry[k.service+"\x00"+k.name]
		if !known || m.Status == envmetrics.MetricRejected || !a.isSet {
			continue
		}
		existing, budgeted := seenKeys[m.ID]
		if !budgeted {
			stored, keyErr := cs.LabelKeysFor(ctx, m.ID)
			if keyErr != nil {
				return keyErr
			}
			existing = stored
			seenKeys[m.ID] = existing
		}
		labels := capCardinality(trimToRegistered(a.labels, m.Labels), existing, m.LabelLimit)
		existing[canonicalKeyOf(labels)] = true

		pts = append(pts, envmetrics.CustomPoint{
			MetricID: m.ID, Kind: m.Kind, Labels: labels,
			Bucket: k.bucket, Value: a.value(),
		})
	}
	return cs.WriteCustomPoints(ctx, pts)
}

func (a *customAgg) value() float64 {
	if a.kind == envmetrics.KindCounter {
		return a.sum
	}
	return a.last
}

func trimToRegistered(labels map[string]string, registered []string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for _, name := range registered {
		if v, ok := labels[name]; ok {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func capCardinality(labels map[string]string, existing map[string]bool, limit int) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = envmetrics.DefaultLabelLimit
	}
	key := canonicalKeyOf(labels)
	if existing[key] || len(existing) < limit {
		return labels
	}
	folded := make(map[string]string, len(labels))
	for k := range labels {
		folded[k] = envmetrics.OtherLabelValue
	}
	return folded
}

func canonicalKeyOf(labels map[string]string) string {
	return envmetrics.CanonicalLabels(labels)
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
