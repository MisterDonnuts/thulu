package sandbox

import (
	"errors"
	"testing"
	"time"
)

func TestPruneReleasedBefore(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	old := time.Now().UTC().AddDate(0, 0, -40)
	recent := time.Now().UTC().AddDate(0, 0, -1)

	evictedOld := newSandbox("evicted-old", "c1", StatusEvicted)
	evictedOld.ReleasedAt = &old
	evictedNew := newSandbox("evicted-new", "c2", StatusEvicted)
	evictedNew.ReleasedAt = &recent
	failedOld := newSandbox("failed-old", "c3", StatusFailed)
	failedOld.LastActivityAt = old
	live := newSandbox("live", "c4", StatusReady)
	live.LastActivityAt = old
	for _, sb := range []*Sandbox{evictedOld, evictedNew, failedOld, live} {
		if err := s.Upsert(ctx, sb); err != nil {
			t.Fatalf("Upsert %s: %v", sb.TaskID, err)
		}
	}

	n, err := s.PruneReleasedBefore(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneReleasedBefore: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d rows, want 2", n)
	}
	for id, want := range map[string]bool{
		"evicted-old": false, "failed-old": false, "evicted-new": true, "live": true,
	} {
		_, err := s.Get(ctx, id)
		if got := !errors.Is(err, ErrNotFound); got != want {
			t.Errorf("row %s present=%t, want %t (err=%v)", id, got, want, err)
		}
	}
}
