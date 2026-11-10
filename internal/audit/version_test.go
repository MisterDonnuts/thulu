package audit_test

import (
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/audit"
)

func TestRecordVersion(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	first, err := store.RecordVersion(ctx, "0.2.0", "ghcr.io/misterdonnuts/octopus@sha256:aaa", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.From != "" || first.To != "0.2.0" {
		t.Fatalf("first boot = %+v, want a change from nothing to 0.2.0", first)
	}

	again, err := store.RecordVersion(ctx, "0.2.0", "", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("same version recorded again: %+v", again)
	}

	up, err := store.RecordVersion(ctx, "0.3.0", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if up == nil || up.From != "0.2.0" || up.To != "0.3.0" {
		t.Fatalf("upgrade = %+v, want 0.2.0 → 0.3.0", up)
	}
	back, err := store.RecordVersion(ctx, "0.2.0", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if back == nil || back.From != "0.3.0" || back.To != "0.2.0" {
		t.Fatalf("rollback = %+v, want 0.3.0 → 0.2.0", back)
	}

	hist, err := store.UpdateHistory(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history has %d entries, want 3: %+v", len(hist), hist)
	}
	if hist[0].Metadata["to"] != "0.2.0" || hist[0].Metadata["from"] != "0.3.0" {
		t.Errorf("newest entry = %+v, want the rollback", hist[0])
	}
	if hist[2].Metadata["image"] != "ghcr.io/misterdonnuts/octopus@sha256:aaa" {
		t.Errorf("first entry lost its image: %+v", hist[2].Metadata)
	}
	if hist[0].ActorType != "system" {
		t.Errorf("actor type = %q, want system", hist[0].ActorType)
	}

	rep, err := store.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Errorf("chain broken after version events: %+v", rep)
	}
}

func TestRecordVersionConcurrentReplicas(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := store.RecordVersion(ctx, "0.2.0", "", ""); err != nil {
		t.Fatal(err)
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		changes int
	)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := store.RecordVersion(ctx, "0.3.0", "", "")
			if err != nil {
				t.Error(err)
				return
			}
			if c != nil {
				mu.Lock()
				changes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if changes != 1 {
		t.Errorf("%d replicas recorded the change, want exactly 1", changes)
	}
}

func TestRecordVersionRejectsEmpty(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordVersion(t.Context(), " ", "", ""); err == nil {
		t.Error("an empty version was recorded")
	}
}
