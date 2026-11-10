package envmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fixedInterval int

func (f fixedInterval) EnvMetricsIntervalSeconds(context.Context) (int, error) { return int(f), nil }

func fakeCloud(t *testing.T, handler func(req collectRequest) (int, any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collect" {
			t.Errorf("collector hit %s, want /collect", r.URL.Path)
		}
		var req collectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode collect request: %v", err)
		}
		code, body := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeTargets struct{}

func (fakeTargets) ResolveCollectTarget(_ context.Context, envID string) (*CollectTarget, error) {
	return &CollectTarget{
		Environment: CollectEnvironment{ID: envID, ProjectID: "p1", Name: "prod", Provider: "aws", Region: "us-east-1"},
		Credential:  CollectCredential{Name: "obs", Provider: "aws", Kind: "aws_keys", Payload: map[string]string{"access_key_id": "AKIA"}},
	}, nil
}

func testCollector(s *Store, url string) *Collector {
	c := NewCollector(s, fixedInterval(300), url)
	c.SetTargets(fakeTargets{})
	return c
}

func markCollected(t *testing.T, s *Store, envID, service string, sig Signal) {
	t.Helper()
	if err := s.WritePoints(context.Background(), []Point{{
		EnvironmentID: envID, Service: service, Signal: sig, Bucket: time.Now().Add(-72 * time.Hour), Value: 1,
	}}); err != nil {
		t.Fatalf("seed point: %v", err)
	}
}

func seedActive(t *testing.T, s *Store, envID, service string, sig Signal) {
	t.Helper()
	if _, err := s.ProposeMappings(context.Background(), envID,
		[]MappingInput{{Service: service, Signal: sig, Source: map[string]any{
			"namespace": "AWS/ApplicationELB", "metric_name": "TargetResponseTime",
		}}}, "", "", "test"); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := s.ApproveMappings(context.Background(), envID); err != nil {
		t.Fatalf("approve: %v", err)
	}
}

func TestCollectorSweepWritesPoints(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-a", "api", SignalLatencyP95)

	var got collectRequest
	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		got = req
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points": []map[string]any{{
				"mapping_id": req.Mappings[0].ID,
				"service":    "api",
				"signal":     "latency_p95",
				"at":         time.Now().UTC().Format(time.RFC3339),
				"value":      231.5,
			}},
		}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	if got.EnvironmentID != "env-a" || len(got.Mappings) != 1 {
		t.Fatalf("collector sent %+v, want one mapping for env-a", got)
	}
	if got.Mappings[0].Source["namespace"] != "AWS/ApplicationELB" {
		t.Errorf("source not forwarded: %+v", got.Mappings[0].Source)
	}

	pts, err := s.Series(ctx, "env-a", SignalLatencyP95, "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 231.5 {
		t.Fatalf("stored %+v, want one point valued 231.5", pts)
	}
}

func TestCollectorIgnoresProposals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.ProposeMappings(ctx, "env-b",
		[]MappingInput{{Service: "api", Signal: SignalCPU, Source: map[string]any{"namespace": "X", "metric_name": "Y"}}},
		"", "", "agent:devops"); err != nil {
		t.Fatalf("propose: %v", err)
	}

	called := false
	cloud := fakeCloud(t, func(collectRequest) (int, any) {
		called = true
		return http.StatusOK, map[string]any{"points": []any{}}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	if called {
		t.Error("the collector called the cloud for an unapproved mapping")
	}
}

func TestCollectorSurvivesFailingEnvironment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-bad", "api", SignalCPU)
	seedActive(t, s, "env-good", "api", SignalCPU)

	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		if req.EnvironmentID == "env-bad" {
			return http.StatusBadGateway, map[string]any{"error": "credentials rejected"}
		}
		return http.StatusOK, map[string]any{
			"points": []map[string]any{{
				"service": "api", "signal": "cpu",
				"at": time.Now().UTC().Format(time.RFC3339), "value": 42.0,
			}},
		}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	pts, err := s.Series(ctx, "env-good", SignalCPU, "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 {
		t.Errorf("the healthy environment got %d points, want 1 — one bad account must not block the rest", len(pts))
	}
}

func TestCollectorSkipsUnsupportedProvider(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-prom", "api", SignalCPU)

	cloud := fakeCloud(t, func(collectRequest) (int, any) {
		return http.StatusOK, map[string]any{"skipped": "provider prometheus not supported yet", "points": []any{}}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	pts, _ := s.Series(ctx, "env-prom", "", "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(pts) != 0 {
		t.Errorf("a skipped environment wrote %d points, want 0", len(pts))
	}
}

func TestCollectorStoresPointsDespitePartialErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-partial", "api", SignalCPU)

	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points": []map[string]any{{
				"service": "api", "signal": "cpu",
				"at": time.Now().UTC().Format(time.RFC3339), "value": 61.0,
			}},
			"errors": []map[string]any{{
				"mapping_id": "other", "signal": "memory",
				"error": "metric.type=\"...\" → HTTP 403 [PERMISSION_DENIED]",
			}},
		}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	pts, err := s.Series(ctx, "env-partial", SignalCPU, "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 61 {
		t.Fatalf("stored %+v, want the healthy cpu point despite a failed sibling mapping", pts)
	}
}

func TestCollectorZeroFillsCountersOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-quiet", "api", SignalThroughput)
	seedActive(t, s, "env-quiet", "api", SignalLatencyP95)
	seedActive(t, s, "env-quiet", "api", SignalErrorRate)
	for _, sig := range []Signal{SignalThroughput, SignalLatencyP95, SignalErrorRate} {
		markCollected(t, s, "env-quiet", "api", sig)
	}

	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		var errs []map[string]any
		for _, m := range req.Mappings {
			if m.Signal == SignalErrorRate {
				errs = append(errs, map[string]any{
					"mapping_id": m.ID, "signal": string(m.Signal),
					"error": "AccessDenied",
				})
			}
		}
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points":         []any{},
			"errors":         errs,
		}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	tp, err := s.Series(ctx, "env-quiet", SignalThroughput, "", from, to)
	if err != nil {
		t.Fatalf("Series throughput: %v", err)
	}
	if len(tp) != 1 || tp[0].Value != 0 {
		t.Errorf("throughput = %+v, want a single explicit 0 — a quiet counter must say zero, not go blank", tp)
	}

	lat, err := s.Series(ctx, "env-quiet", SignalLatencyP95, "", from, to)
	if err != nil {
		t.Fatalf("Series latency: %v", err)
	}
	if len(lat) != 0 {
		t.Errorf("latency_p95 = %+v, want no point — an unsampled p95 is unknown, and 0 ms would be a lie", lat)
	}

	er, err := s.Series(ctx, "env-quiet", SignalErrorRate, "", from, to)
	if err != nil {
		t.Fatalf("Series error_rate: %v", err)
	}
	if len(er) != 0 {
		t.Errorf("error_rate = %+v, want no point — the query failed, so the answer is unknown, not zero", er)
	}
}

func TestCollectorZeroFillDoesNotClobberRealValues(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-busy", "api", SignalThroughput)
	markCollected(t, s, "env-busy", "api", SignalThroughput)

	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points": []map[string]any{{
				"mapping_id": req.Mappings[0].ID,
				"service":    "api", "signal": "throughput",
				"at": time.Now().UTC().Format(time.RFC3339), "value": 42.0,
			}},
		}
	})

	testCollector(s, cloud.URL).Sweep(ctx)

	pts, err := s.Series(ctx, "env-busy", SignalThroughput, "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 42 {
		t.Fatalf("stored %+v, want the real 42 — the fill must only cover mappings that answered nothing", pts)
	}
}

func TestCollectorCollectNow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-now", "api", SignalLatencyP95)
	seedActive(t, s, "env-other", "api", SignalLatencyP95)

	var seen []string
	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		seen = append(seen, req.EnvironmentID)
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points": []map[string]any{{
				"mapping_id": req.Mappings[0].ID,
				"service":    "api", "signal": "latency_p95",
				"at": time.Now().UTC().Format(time.RFC3339), "value": 120.0,
			}},
		}
	})
	c := testCollector(s, cloud.URL)

	n, err := c.CollectNow(ctx, "env-now")
	if err != nil || n != 1 {
		t.Fatalf("CollectNow = (%d, %v), want (1, nil)", n, err)
	}
	if len(seen) != 1 || seen[0] != "env-now" {
		t.Errorf("collected %v, want only env-now", seen)
	}

	if _, err := c.CollectNow(ctx, "env-now"); !errors.Is(err, ErrCollectThrottled) {
		t.Errorf("second immediate collect: err = %v, want ErrCollectThrottled", err)
	}
	if _, err := c.CollectNow(ctx, "env-other"); err != nil {
		t.Errorf("the cooldown leaked across environments: %v", err)
	}
}

func TestCollectorCollectNowWithoutMappings(t *testing.T) {
	s := newTestStore(t)
	cloud := fakeCloud(t, func(collectRequest) (int, any) {
		t.Error("the cloud must not be called when there is nothing to poll")
		return http.StatusOK, map[string]any{"points": []any{}}
	})
	_, err := testCollector(s, cloud.URL).CollectNow(context.Background(), "env-empty")
	if !errors.Is(err, ErrNoActiveMappings) {
		t.Errorf("err = %v, want ErrNoActiveMappings", err)
	}
}

func TestCollectorEnabledAndInterval(t *testing.T) {
	s := newTestStore(t)
	if NewCollector(s, fixedInterval(300), "").Enabled() {
		t.Error("no cloud URL should disable the collector")
	}
	if !NewCollector(s, fixedInterval(300), "http://x").Enabled() {
		t.Error("a configured collector should be enabled")
	}
	if got := NewCollector(s, fixedInterval(0), "http://x").interval(t.Context()); got != 0 {
		t.Errorf("interval = %v, want 0 (explicitly disabled)", got)
	}
	if got := NewCollector(s, fixedInterval(600), "http://x").interval(t.Context()); got != 10*time.Minute {
		t.Errorf("interval = %v, want 10m", got)
	}
}

func TestCollectorStartDisabledDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewCollector(nil, fixedInterval(300), "").Start(ctx)
	NewCollector(newTestStore(t), fixedInterval(0), "http://x").Start(ctx)
}

func TestCollectorSendsTargetAndBackfillsNewSignals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedActive(t, s, "env-new", "api", SignalErrorRate)

	spike := TruncateBucket(time.Now().Add(-3 * time.Hour))
	var reqs []collectRequest
	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		reqs = append(reqs, req)
		return http.StatusOK, map[string]any{
			"environment_id": req.EnvironmentID,
			"points": []map[string]any{{
				"mapping_id": req.Mappings[0].ID, "service": "api", "signal": "error_rate",
				"at": spike.Format(time.RFC3339), "value": 7.0,
			}},
		}
	})
	c := testCollector(s, cloud.URL)
	c.Sweep(ctx)

	if len(reqs) != 1 {
		t.Fatalf("collect requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Environment.ID != "env-new" || r.Environment.Provider != "aws" || r.Credential.Kind != "aws_keys" {
		t.Errorf("target not sent: env=%+v cred kind=%q", r.Environment, r.Credential.Kind)
	}
	if !r.Mappings[0].Backfill || r.BackfillHours != 24 {
		t.Errorf("first collection: backfill=%v hours=%d, want true and 24", r.Mappings[0].Backfill, r.BackfillHours)
	}

	pts, err := s.Series(ctx, "env-new", SignalErrorRate, "", time.Now().Add(-25*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	want := int(BackfillWindow/BucketWidth) + 1
	if len(pts) != want {
		t.Fatalf("stored %d points, want %d: the spike plus a zero in every other bucket of the day", len(pts), want)
	}
	for _, p := range pts {
		if p.Bucket.Equal(spike) != (p.Value == 7) {
			t.Fatalf("bucket %s = %v, want 7 only at the spike and 0 elsewhere", p.Bucket, p.Value)
		}
	}

	c.Sweep(ctx)
	if len(reqs) != 2 || reqs[1].Mappings[0].Backfill {
		t.Errorf("second collection backfilled again: %+v", reqs[len(reqs)-1].Mappings)
	}
}

func TestCollectorWithoutTargetsSendsNothing(t *testing.T) {
	s := newTestStore(t)
	seedActive(t, s, "env-x", "api", SignalLatencyP95)
	posted := false
	cloud := fakeCloud(t, func(req collectRequest) (int, any) {
		posted = true
		return http.StatusOK, map[string]any{"environment_id": req.EnvironmentID, "points": []any{}}
	})
	if _, err := NewCollector(s, fixedInterval(300), cloud.URL).CollectNow(context.Background(), "env-x"); err == nil {
		t.Fatal("CollectNow without targets succeeded")
	}
	if posted {
		t.Error("the collector posted without a credential")
	}
}
