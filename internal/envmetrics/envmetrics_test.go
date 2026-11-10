package envmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mapping(service string, sig Signal) MappingInput {
	return MappingInput{
		Service: service,
		Signal:  sig,
		Source:  map[string]any{"namespace": "AWS/ApplicationELB", "metric_name": "TargetResponseTime"},
	}
}

func TestSignalValid(t *testing.T) {
	if !SignalLatencyP95.Valid() {
		t.Error("latency_p95 should be valid")
	}
	if Signal("made_up").Valid() {
		t.Error("an unknown signal must not validate — the catalog is closed on purpose")
	}
	if len(ValidSignals) != 9 {
		t.Errorf("ValidSignals has %d entries, want 9", len(ValidSignals))
	}
	if !SignalSaturation.Valid() {
		t.Error("saturation should be valid")
	}
}

func TestMappingInputValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      MappingInput
		wantErr bool
	}{
		{"ok", mapping("api", SignalLatencyP95), false},
		{"no service", MappingInput{Signal: SignalCPU, Source: map[string]any{"a": 1}}, true},
		{"unknown signal", MappingInput{Service: "api", Signal: "nope", Source: map[string]any{"a": 1}}, true},
		{"empty source", MappingInput{Service: "api", Signal: SignalCPU}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestTruncateBucket(t *testing.T) {
	in := time.Date(2026, 7, 31, 14, 37, 42, 500, time.FixedZone("art", -3*3600))
	got := TruncateBucket(in)
	want := time.Date(2026, 7, 31, 17, 35, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("TruncateBucket = %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("bucket location = %v, want UTC — a TZ change between restarts must not shift buckets", got.Location())
	}
}

func TestProposedMappingsAreInert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ProposeMappings(ctx, "env-1",
		[]MappingInput{mapping("api", SignalLatencyP95), mapping("api", SignalErrorRate)},
		"task-1", "flag-1", "agent:devops"); err != nil {
		t.Fatalf("ProposeMappings: %v", err)
	}

	active, err := s.ActiveMappings(ctx)
	if err != nil {
		t.Fatalf("ActiveMappings: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("proposals must not be collected, got %d active", len(active))
	}

	listed, err := s.ListMappings(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListMappings: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListMappings returned %d, want 2", len(listed))
	}
	for _, m := range listed {
		if m.Status != StatusProposed {
			t.Errorf("status = %q, want proposed", m.Status)
		}
		if m.TaskID != "task-1" || m.FlagID != "flag-1" {
			t.Errorf("provenance lost: task=%q flag=%q", m.TaskID, m.FlagID)
		}
		if m.Source["namespace"] != "AWS/ApplicationELB" {
			t.Errorf("source round-trip failed: %+v", m.Source)
		}
	}

	n, err := s.ApproveMappings(ctx, "env-1")
	if err != nil {
		t.Fatalf("ApproveMappings: %v", err)
	}
	if n != 2 {
		t.Errorf("activated %d, want 2", n)
	}
	active, err = s.ActiveMappings(ctx)
	if err != nil {
		t.Fatalf("ActiveMappings after approve: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("got %d active after approve, want 2", len(active))
	}
}

func TestApproveRetiresReplacedMapping(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ProposeMappings(ctx, "env-2", []MappingInput{mapping("api", SignalCPU)}, "", "", "user"); err != nil {
		t.Fatalf("first propose: %v", err)
	}
	if _, err := s.ApproveMappings(ctx, "env-2"); err != nil {
		t.Fatalf("first approve: %v", err)
	}

	second := mapping("api", SignalCPU)
	second.Source = map[string]any{"namespace": "AWS/ECS", "metric_name": "CPUUtilization"}
	if _, err := s.ProposeMappings(ctx, "env-2", []MappingInput{second}, "", "", "agent:devops"); err != nil {
		t.Fatalf("second propose: %v", err)
	}
	if _, err := s.ApproveMappings(ctx, "env-2"); err != nil {
		t.Fatalf("second approve: %v", err)
	}

	active, err := s.ActiveMappings(ctx)
	if err != nil {
		t.Fatalf("ActiveMappings: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("got %d active, want exactly 1 — the old mapping should be retired", len(active))
	}
	if active[0].Source["namespace"] != "AWS/ECS" {
		t.Errorf("the surviving mapping is the old one: %+v", active[0].Source)
	}
}

func TestPendingFlagIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ProposeMappings(ctx, "env-3",
		[]MappingInput{mapping("api", SignalCPU), mapping("web", SignalCPU)},
		"task-9", "flag-9", "agent:devops"); err != nil {
		t.Fatalf("propose: %v", err)
	}
	ids, err := s.PendingFlagIDs(ctx, "env-3")
	if err != nil {
		t.Fatalf("PendingFlagIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "flag-9" {
		t.Errorf("PendingFlagIDs = %v, want [flag-9] deduplicated", ids)
	}

	if _, err := s.ApproveMappings(ctx, "env-3"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if ids, err = s.PendingFlagIDs(ctx, "env-3"); err != nil || len(ids) != 0 {
		t.Errorf("after approve PendingFlagIDs = %v (err %v), want empty", ids, err)
	}
}

func TestWritePointsIsLastValueWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 31, 12, 3, 0, 0, time.UTC)

	write := func(v float64) {
		t.Helper()
		if err := s.WritePoints(ctx, []Point{{
			EnvironmentID: "env-4", Service: "api", Signal: SignalLatencyP95,
			Bucket: at, Value: v,
		}}); err != nil {
			t.Fatalf("WritePoints(%v): %v", v, err)
		}
	}
	write(120)
	write(180)

	pts, err := s.Series(ctx, "env-4", SignalLatencyP95, "", at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1 — the second write must overwrite the bucket", len(pts))
	}
	if pts[0].Value != 180 {
		t.Errorf("value = %v, want 180 (last value wins, not 300)", pts[0].Value)
	}
	if !pts[0].Bucket.Equal(TruncateBucket(at)) {
		t.Errorf("bucket = %v, want it snapped to %v", pts[0].Bucket, TruncateBucket(at))
	}
}

func TestSeriesFiltersAndPurge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -120)

	if err := s.WritePoints(ctx, []Point{
		{EnvironmentID: "env-5", Service: "api", Signal: SignalCPU, Bucket: now, Value: 10},
		{EnvironmentID: "env-5", Service: "web", Signal: SignalCPU, Bucket: now, Value: 20},
		{EnvironmentID: "env-5", Service: "api", Signal: SignalMemory, Bucket: now, Value: 30},
		{EnvironmentID: "env-5", Service: "api", Signal: SignalCPU, Bucket: old, Value: 40},
	}); err != nil {
		t.Fatalf("WritePoints: %v", err)
	}

	wide := func(sig Signal, svc string) int {
		t.Helper()
		pts, err := s.Series(ctx, "env-5", sig, svc, old.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		return len(pts)
	}
	if got := wide("", ""); got != 4 {
		t.Errorf("unfiltered = %d, want 4", got)
	}
	if got := wide(SignalCPU, ""); got != 3 {
		t.Errorf("by signal = %d, want 3", got)
	}
	if got := wide(SignalCPU, "api"); got != 2 {
		t.Errorf("by signal+service = %d, want 2", got)
	}

	n, err := s.PurgeOlderThan(ctx, now.AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	if got := wide("", ""); got != 3 {
		t.Errorf("after purge = %d, want 3", got)
	}
}

func TestDeleteForEnvironment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ProposeMappings(ctx, "env-6", []MappingInput{mapping("api", SignalCPU)}, "", "", "user"); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if err := s.WritePoints(ctx, []Point{{
		EnvironmentID: "env-6", Service: "api", Signal: SignalCPU,
		Bucket: time.Now(), Value: 1,
	}}); err != nil {
		t.Fatalf("WritePoints: %v", err)
	}
	if err := s.DeleteForEnvironment(ctx, "env-6"); err != nil {
		t.Fatalf("DeleteForEnvironment: %v", err)
	}
	if m, _ := s.ListMappings(ctx, "env-6"); len(m) != 0 {
		t.Errorf("mappings survived the cascade: %d", len(m))
	}
	pts, _ := s.Series(ctx, "env-6", "", "", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(pts) != 0 {
		t.Errorf("points survived the cascade: %d", len(pts))
	}
}
