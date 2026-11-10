package chatruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestPruneReleasedBefore(t *testing.T) {
	st, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -40)
	recent := time.Now().UTC().AddDate(0, 0, -1)
	rows := []*Runtime{
		{ChatID: "evicted-old", Status: StatusEvicted, ReleasedAt: &old},
		{ChatID: "evicted-new", Status: StatusEvicted, ReleasedAt: &recent},
		{ChatID: "failed-old", Status: StatusFailed, LastActivityAt: old},
		{ChatID: "live", Status: StatusReady, LastActivityAt: old},
	}
	for _, rt := range rows {
		rt.AgentType, rt.ContainerID, rt.CreatedAt = "chat", "c-"+rt.ChatID, old
		if rt.LastActivityAt.IsZero() {
			rt.LastActivityAt = old
		}
		if err := st.Upsert(rt); err != nil {
			t.Fatalf("Upsert %s: %v", rt.ChatID, err)
		}
	}

	n, err := st.PruneReleasedBefore(t.Context(), time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneReleasedBefore: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d rows, want 2", n)
	}
	for id, want := range map[string]bool{
		"evicted-old": false, "failed-old": false, "evicted-new": true, "live": true,
	} {
		_, err := st.Get(id)
		if got := !errors.Is(err, ErrNotFound); got != want {
			t.Errorf("row %s present=%t, want %t (err=%v)", id, got, want, err)
		}
	}
}
