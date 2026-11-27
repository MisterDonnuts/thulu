package chatruntime

import (
	"context"
	"slices"
	"testing"
	"time"
)

func (d *fakeDocker) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.removed)
}

// The GC sweeps for chat containers no live row claims on its own schedule, not only at boot: a
// container whose row was released while its remove failed is gone within one sweep, and the
// container of a live chat stays.
func TestStartGCSweepsOrphanChatContainersPeriodically(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-orphans")
	id, err := r.mgr.Acquire(context.Background(), c, testAgent(), AcquireOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r.docker.setRunning("ctr-orphan", true)
	r.mgr.orphanEvery = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.mgr.StartGC(ctx, time.Hour)

	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(r.docker.removedIDs(), "ctr-orphan") {
		if time.Now().After(deadline) {
			t.Fatalf("the orphan container was never removed; removed %v", r.docker.removedIDs())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if slices.Contains(r.docker.removedIDs(), id) {
		t.Fatalf("the live chat's container %s was removed", id)
	}
}

// A create in flight has its container before its row: the sweep stands down until it is done.
func TestReapOrphansStandsDownWhileACreateIsInFlight(t *testing.T) {
	r := newRig(t, 5)
	r.docker.setRunning("ctr-creating", true)
	unreserve, err := r.mgr.reserveCapacity(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if n := r.mgr.ReapOrphans(context.Background()); n != 0 {
		t.Fatalf("removed %d containers during a create", n)
	}
	unreserve()
	if n := r.mgr.ReapOrphans(context.Background()); n != 1 {
		t.Fatalf("removed %d containers once the create was done, want 1", n)
	}
}
