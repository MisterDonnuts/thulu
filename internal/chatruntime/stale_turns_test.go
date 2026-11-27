package chatruntime

import (
	"context"
	"slices"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func (d *fakeDocker) execsSnapshot() []container.ExecOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.execs)
}

func sweepsEveryTurn(o container.ExecOptions) bool {
	return len(o.Cmd) > 0 && o.Cmd[0] == "bash" &&
		slices.Contains(o.Env, "OCTOPUS_REAP_MODE=any") && slices.Contains(o.Env, "OCTOPUS_REAP_VAR="+envChatTurn)
}

// A daemon restarted while a chat turn ran: the turn kept running in the container under its
// timeout, and what it started in the background had no reap left to kill it. Reconcile, at boot,
// kills every process any turn marked in the containers it keeps, and keeps the chat.
func TestReconcileSweepsTurnsADaemonBeforeLeftRunning(t *testing.T) {
	r := newRig(t, 5)
	ctx := context.Background()
	id, err := r.mgr.Acquire(ctx, testChat("chat-stale"), testAgent(), AcquireOpts{})
	if err != nil {
		t.Fatal(err)
	}
	before := len(r.docker.execsSnapshot())

	r.mgr.Reconcile(ctx)

	execs := r.docker.execsSnapshot()[before:]
	if len(execs) != 1 || !sweepsEveryTurn(execs[0]) {
		t.Fatalf("want one sweep of every turn's processes in %s, got %+v", id, execs)
	}
	if slices.Contains(r.docker.removedIDs(), id) {
		t.Fatalf("the swept chat's container %s was removed", id)
	}
	rt, err := r.store.Get("chat-stale")
	if err != nil || rt.Status == StatusEvicted || rt.ContainerID != id {
		t.Fatalf("the swept chat lost its container: %+v %v", rt, err)
	}
}

// A turn of this daemon in flight is not a leftover: its own reap follows it.
func TestReconcileLeavesATurnInFlightAlone(t *testing.T) {
	r := newRig(t, 5)
	ctx := context.Background()
	if _, err := r.mgr.Acquire(ctx, testChat("chat-busy"), testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn("chat-busy"); err != nil {
		t.Fatal(err)
	}
	defer r.mgr.EndTurn("chat-busy")
	before := len(r.docker.execsSnapshot())

	r.mgr.Reconcile(ctx)

	for _, o := range r.docker.execsSnapshot()[before:] {
		if sweepsEveryTurn(o) {
			t.Fatalf("Reconcile swept a chat whose turn is in flight: %+v", o)
		}
	}
}
