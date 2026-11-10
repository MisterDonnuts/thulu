package envmetrics

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/metrics"
	"github.com/MisterDonnuts/thulu/internal/syncx"
)

type Collector struct {
	store      *Store
	settings   intervalSource
	cloudURL   string
	httpClient *http.Client
	targets    TargetResolver

	// Purged on the collector's tick, not the janitor: the janitor only runs when Docker is present
	// and this must also work in Kubernetes.
	retention time.Duration
	lastPurge time.Time

	// Guarded: API handlers call CollectNow from request goroutines while the tick runs on its own.
	manualMu   sync.Mutex
	lastManual map[string]time.Time
}

const DefaultRetention = 90 * 24 * time.Hour

const purgeEvery = time.Hour

type intervalSource interface {
	EnvMetricsIntervalSeconds(ctx context.Context) (int, error)
}

func NewCollector(store *Store, settings intervalSource, cloudURL string) *Collector {
	return &Collector{
		store:      store,
		settings:   settings,
		cloudURL:   cloudURL,
		retention:  DefaultRetention,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

const BackfillWindow = 24 * time.Hour

func (c *Collector) SetTargets(r TargetResolver) { c.targets = r }

func (c *Collector) Enabled() bool {
	return c != nil && c.cloudURL != "" && c.store != nil
}

func (c *Collector) Start(ctx context.Context) {
	if !c.Enabled() {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.collector_disabled_no_url"))
		return
	}
	interval := c.interval(ctx)
	if interval == 0 {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.collector_disabled_by_settings",
			"setting", "envmetrics.interval_seconds"))
		return
	}
	log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.collector_started", "interval", interval, "url", c.cloudURL))
	go syncx.TickUntil(ctx, interval, c.Sweep)
}

func (c *Collector) interval(ctx context.Context) time.Duration {
	if c.settings == nil {
		return BucketWidth
	}
	n, err := c.settings.EnvMetricsIntervalSeconds(ctx)
	if err != nil {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.read_interval_failed", "err", err, "fallback", BucketWidth))
		return BucketWidth
	}
	if n == 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

var ErrNoActiveMappings error = apperr.Precondition("envmetrics.errors.no_active_mappings")

var ErrCollectThrottled error = apperr.RateLimited("envmetrics.errors.collect_throttled")

// Every collection is billed to the environment's cloud account, and nothing changes within one
// 5-minute bucket.
const manualCollectCooldown = 20 * time.Second

func (c *Collector) CollectNow(ctx context.Context, envID string) (int, error) {
	if c == nil || c.store == nil {
		return 0, apperr.Unavailable("envmetrics.errors.collector_not_configured")
	}
	if c.cloudURL == "" {
		return 0, apperr.Unavailable("envmetrics.errors.collector_no_url")
	}
	c.manualMu.Lock()
	if last, ok := c.lastManual[envID]; ok && time.Since(last) < manualCollectCooldown {
		wait := manualCollectCooldown - time.Since(last)
		c.manualMu.Unlock()
		return 0, apperr.Wrap(ErrCollectThrottled, "envmetrics.errors.collect_wait", "wait", wait.Round(time.Second))
	}
	if c.lastManual == nil {
		c.lastManual = map[string]time.Time{}
	}
	c.lastManual[envID] = time.Now()
	c.manualMu.Unlock()

	all, err := c.store.ActiveMappings(ctx)
	if err != nil {
		return 0, apperr.Wrap(err, "envmetrics.errors.list_active_mappings")
	}
	ms := make([]Mapping, 0, len(all))
	for i := range all {
		if all[i].EnvironmentID == envID {
			ms = append(ms, all[i])
		}
	}
	if len(ms) == 0 {
		return 0, ErrNoActiveMappings
	}
	n, err := c.collectEnvironment(ctx, envID, ms)
	if err != nil {
		metrics.RecordEnvMetricsPoll("fail")
		return 0, err
	}
	metrics.RecordEnvMetricsPoll("ok")
	return n, nil
}

func (c *Collector) Sweep(ctx context.Context) {
	start := time.Now()
	mappings, err := c.store.ActiveMappings(ctx)
	if err != nil {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.list_active_mappings_failed", "err", err))
		return
	}
	if len(mappings) == 0 {
		return
	}

	byEnv := map[string][]Mapping{}
	for i := range mappings {
		byEnv[mappings[i].EnvironmentID] = append(byEnv[mappings[i].EnvironmentID], mappings[i])
	}

	var envsOK, envsFail, written int
	for envID, ms := range byEnv {
		n, sweepErr := c.collectEnvironment(ctx, envID, ms)
		if sweepErr != nil {
			envsFail++
			metrics.RecordEnvMetricsPoll("fail")
			log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.environment_failed", "env", envID, "err", sweepErr))
			continue
		}
		envsOK++
		written += n
		metrics.RecordEnvMetricsPoll("ok")
	}
	log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.sweep_done",
		"elapsed", time.Since(start).Round(time.Millisecond), "ok", envsOK, "failed", envsFail, "points", written))

	c.purgeIfDue(ctx)
}

func (c *Collector) purgeIfDue(ctx context.Context) {
	if time.Since(c.lastPurge) < purgeEvery {
		return
	}
	c.lastPurge = time.Now()
	if n := c.PurgeOldPoints(ctx, c.retention); n > 0 {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.purged_points", "count", n, "retention", c.retention))
	}
}

// Wire contract with thulu-cloud's /collect endpoint; change both sides together.
type collectRequest struct {
	EnvironmentID string             `json:"environment_id"`
	Environment   CollectEnvironment `json:"environment"`
	Credential    CollectCredential  `json:"credential"`
	BackfillHours int                `json:"backfill_hours,omitempty"`
	Mappings      []collectMapping   `json:"mappings"`
}

type collectMapping struct {
	ID       string         `json:"id"`
	Service  string         `json:"service"`
	Signal   Signal         `json:"signal"`
	Source   map[string]any `json:"source"`
	Backfill bool           `json:"backfill,omitempty"`
}

type collectResponse struct {
	EnvironmentID string `json:"environment_id"`
	Skipped       string `json:"skipped,omitempty"`
	Error         string `json:"error,omitempty"`
	Points        []struct {
		MappingID string  `json:"mapping_id"`
		Service   string  `json:"service"`
		Signal    Signal  `json:"signal"`
		At        string  `json:"at"`
		Value     float64 `json:"value"`
	} `json:"points"`
	// Per-mapping failures only. A total failure arrives as a non-200, so an empty Points list
	// means no data, not a rejected credential.
	Errors []struct {
		MappingID string `json:"mapping_id"`
		Signal    Signal `json:"signal"`
		Error     string `json:"error"`
	} `json:"errors,omitempty"`
}

func (c *Collector) collectEnvironment(ctx context.Context, envID string, ms []Mapping) (int, error) {
	seen, err := c.store.CollectedSignals(ctx, envID)
	if err != nil {
		return 0, apperr.Wrap(err, "envmetrics.errors.list_collected_signals")
	}
	req := collectRequest{EnvironmentID: envID}
	backfilled := map[string]bool{}
	for i := range ms {
		if len(ms[i].Source) == 0 {
			continue
		}
		backfill := !seen[SignalKey{Service: ms[i].Service, Signal: ms[i].Signal}]
		backfilled[ms[i].ID] = backfill
		req.Mappings = append(req.Mappings, collectMapping{
			ID: ms[i].ID, Service: ms[i].Service, Signal: ms[i].Signal, Source: ms[i].Source,
			Backfill: backfill,
		})
	}
	if len(req.Mappings) == 0 {
		return 0, nil
	}
	if c.targets == nil {
		return 0, apperr.Unavailable("envmetrics.errors.collector_no_targets")
	}
	target, err := c.targets.ResolveCollectTarget(ctx, envID)
	if err != nil {
		return 0, err
	}
	req.Environment, req.Credential = target.Environment, target.Credential
	req.BackfillHours = int(BackfillWindow / time.Hour)

	body, err := json.Marshal(req)
	if err != nil {
		return 0, apperr.Wrap(err, "envmetrics.errors.marshal_collect_request")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cloudURL+"/collect", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, apperr.WrapKind(apperr.KindUnavailable, err, "envmetrics.errors.post_collect")
	}
	defer func() { _ = resp.Body.Close() }()

	var out collectResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&out); decErr != nil {
		return 0, apperr.Wrap(decErr, "envmetrics.errors.decode_collect_response", "status", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, apperr.New("envmetrics.errors.collect_http_status", "status", resp.StatusCode, "message", out.Error)
	}
	if out.Skipped != "" {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.environment_skipped", "env", envID, "reason", out.Skipped))
		return 0, nil
	}
	for _, e := range out.Errors {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.mapping_failed",
			"env", envID, "mapping", e.MappingID, "signal", e.Signal, "err", e.Error))
	}

	now := time.Now()
	pts := make([]Point, 0, len(out.Points))
	got := make(map[string]map[time.Time]bool, len(out.Points))
	for _, p := range out.Points {
		at, parseErr := time.Parse(time.RFC3339, p.At)
		if parseErr != nil {
			at = now.UTC()
		}
		pts = append(pts, Point{
			EnvironmentID: envID, Service: p.Service, Signal: p.Signal,
			Bucket: at, Value: p.Value,
		})
		if got[p.MappingID] == nil {
			got[p.MappingID] = map[time.Time]bool{}
		}
		got[p.MappingID][TruncateBucket(at)] = true
	}
	pts = append(pts, zeroFill(envID, ms, got, backfilled, out, now)...)

	if err := c.store.WritePoints(ctx, pts); err != nil {
		return 0, apperr.Wrap(err, "envmetrics.errors.write_points")
	}
	return len(pts), nil
}

// CloudWatch returns an empty series for a counter with no events. Zeros are stored at collection
// time, not filled on read, so a collector outage stays a gap.
// Gauges and mappings the provider reported an error for stay absent: their value is unknown.
func zeroFill(envID string, ms []Mapping, got map[string]map[time.Time]bool, backfilled map[string]bool,
	out collectResponse, now time.Time,
) []Point {
	failed := make(map[string]bool, len(out.Errors))
	for _, e := range out.Errors {
		failed[e.MappingID] = true
	}
	current := TruncateBucket(now)
	zeros := make([]Point, 0, len(ms))
	for i := range ms {
		m := &ms[i]
		if !m.Signal.IsCounter() || failed[m.ID] || m.NotApplicable {
			continue
		}
		first := current
		switch {
		case backfilled[m.ID]:
			first = TruncateBucket(now.Add(-BackfillWindow))
		case len(got[m.ID]) > 0:
			continue
		}
		for b := first; !b.After(current); b = b.Add(BucketWidth) {
			if got[m.ID][b] {
				continue
			}
			zeros = append(zeros, Point{
				EnvironmentID: envID, Service: m.Service, Signal: m.Signal,
				Bucket: b, Value: 0,
			})
		}
	}
	return zeros
}

func (c *Collector) PurgeOldPoints(ctx context.Context, retention time.Duration) int64 {
	if c == nil || c.store == nil {
		return 0
	}
	cutoff := time.Now().UTC().Add(-retention)
	n, err := c.store.PurgeOlderThan(ctx, cutoff)
	if err != nil {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.purge_points_failed", "err", err))
		return 0
	}
	custom, err := c.store.PurgeCustomOlderThan(ctx, cutoff)
	if err != nil {
		log.Printf("[envmetrics] %s", i18n.Log("envmetrics.log.purge_custom_points_failed", "err", err))
		return n
	}
	return n + custom
}
