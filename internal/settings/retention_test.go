package settings

import "testing"

func TestRetentionDays(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for key, want := range map[string]int{
		RetentionRunLogsDaysKey:           30,
		RetentionSessionsDaysKey:          90,
		RetentionWebhookDeliveriesDaysKey: 30,
		RetentionRunKeysDaysKey:           7,
		RetentionSandboxRowsDaysKey:       30,
		RetentionCacheMetricsDaysKey:      90,
		RetentionStuckWorktreeDaysKey:     14,
	} {
		if !KnownKey(key) {
			t.Errorf("%s is not a known key", key)
		}
		got, err := s.RetentionDays(ctx, key)
		if err != nil || got != want {
			t.Errorf("RetentionDays(%s) = %d, %v; want %d", key, got, err, want)
		}
	}

	if err := s.PutJSON(ctx, RetentionSessionsDaysKey, -5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RetentionDays(ctx, RetentionSessionsDaysKey); got != 0 {
		t.Errorf("negative window read as %d, want 0", got)
	}

	if got, _ := s.JanitorDiskFreeMinPercent(ctx); got != 10 {
		t.Errorf("JanitorDiskFreeMinPercent = %d, want 10", got)
	}
	if err := s.PutJSON(ctx, JanitorDiskFreeMinPercentKey, 250); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.JanitorDiskFreeMinPercent(ctx); got != 100 {
		t.Errorf("out-of-range percent read as %d, want 100", got)
	}
}
