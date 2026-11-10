package sandbox

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

type queueRig struct {
	store    *Store
	settings *settings.Store
	queue    *Queue

	mu     sync.Mutex
	grants []string
}

func newQueueRig(t *testing.T, max int, blockMs int, idleSecs int) *queueRig {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatalf("settings.NewStore: %v", err)
	}
	sett.SetStageBoard(stagetest.Flow)
	if err := sett.SeedSandboxPriorities(t.Context()); err != nil {
		t.Fatalf("SeedSandboxPriorities: %v", err)
	}
	if err := sett.SetSandboxMax(t.Context(), max); err != nil {
		t.Fatalf("SetSandboxMax: %v", err)
	}
	if err := sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, blockMs); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	if err := sett.PutJSON(t.Context(), settings.SandboxIdleEvictSecondsKey, idleSecs); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}

	rig := &queueRig{store: st, settings: sett}
	rig.queue = NewQueue(st, sett, func(taskID, stage string) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.grants = append(rig.grants, taskID)
	})
	rig.queue.StartGC(t.Context())
	return rig
}

func (r *queueRig) markReady(t *testing.T, taskID string) {
	t.Helper()
	if err := r.store.SetStatus(t.Context(), taskID, StatusReady); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
}

func (r *queueRig) staleActivity(t *testing.T, taskID string, age time.Duration) {
	t.Helper()
	sb, err := r.store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	sb.LastActivityAt = time.Now().Add(-age).UTC()
	if err := r.store.Upsert(t.Context(), sb); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func (r *queueRig) recordedGrants() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.grants))
	copy(out, r.grants)
	return out
}

func TestAcquireGrantsImmediatelyWhenCapacityAvailable(t *testing.T) {
	r := newQueueRig(t, 3, 50, 1800)
	res, err := r.queue.Acquire(context.Background(), "t1", "in_progress")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if res.Outcome != AcquireGranted {
		t.Errorf("Outcome: got %d", res.Outcome)
	}
	sb, err := r.store.Get(t.Context(), "t1")
	if err != nil {
		t.Fatalf("expected starting row: %v", err)
	}
	if sb.Status != StatusStarting {
		t.Errorf("status: %q", sb.Status)
	}
}

func TestAcquireExistingShortCircuits(t *testing.T) {
	r := newQueueRig(t, 1, 50, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	res, err := r.queue.Acquire(context.Background(), "t1", "in_progress")
	if err != nil {
		t.Fatalf("Acquire #2: %v", err)
	}
	if res.Outcome != AcquireExisting {
		t.Errorf("Outcome: got %d", res.Outcome)
	}
}

func TestAcquireQueuesWhenCapFull(t *testing.T) {
	r := newQueueRig(t, 2, 200, 1800)
	for _, id := range []string{"t1", "t2"} {
		if _, err := r.queue.Acquire(context.Background(), id, "in_progress"); err != nil {
			t.Fatalf("acquire %s: %v", id, err)
		}
		r.markReady(t, id)
	}

	start := time.Now()
	res, err := r.queue.Acquire(context.Background(), "t3", "in_progress")
	if err != nil {
		t.Fatalf("Acquire t3: %v", err)
	}
	if res.Outcome != AcquireQueued {
		t.Errorf("Outcome: expected queued, got %d", res.Outcome)
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Errorf("did not wait blockMs: %v", elapsed)
	}
	if res.QueuePosition != 0 {
		t.Errorf("QueuePosition: got %d", res.QueuePosition)
	}

	q, _ := r.store.ListQueue(t.Context())
	if len(q) != 1 || q[0].TaskID != "t3" {
		t.Errorf("queue state: %+v", q)
	}
}

func TestReleaseGrantsNextWaiter(t *testing.T) {
	r := newQueueRig(t, 1, 5000, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	type res struct {
		o AcquireOutcome
		e error
	}
	resCh := make(chan res, 1)
	go func() {
		ar, err := r.queue.Acquire(context.Background(), "t2", "handoff")
		resCh <- res{ar.Outcome, err}
	}()

	time.Sleep(80 * time.Millisecond)
	if err := r.queue.Release(t.Context(), "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case got := <-resCh:
		if got.e != nil {
			t.Fatalf("Acquire t2: %v", got.e)
		}
		if got.o != AcquireGranted {
			t.Errorf("Outcome: got %d", got.o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire t2 did not unblock after Release(t1)")
	}
}

func TestReleaseFiresCallbackWhenNoWaiter(t *testing.T) {
	r := newQueueRig(t, 1, 80, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	res, err := r.queue.Acquire(context.Background(), "t2", "handoff")
	if err != nil {
		t.Fatalf("Acquire t2: %v", err)
	}
	if res.Outcome != AcquireQueued {
		t.Fatalf("Outcome: got %d", res.Outcome)
	}

	if err := r.queue.Release(t.Context(), "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if g := r.recordedGrants(); len(g) > 0 && g[0] == "t2" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("OnSlotGranted never fired: grants=%v", r.recordedGrants())
}

func TestPriorityIsRespected(t *testing.T) {
	r := newQueueRig(t, 1, 80, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	_, _ = r.queue.Acquire(context.Background(), "t-low", "refinement")
	time.Sleep(20 * time.Millisecond)
	_, _ = r.queue.Acquire(context.Background(), "t-high", "handoff")

	if err := r.queue.Release(t.Context(), "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if g := r.recordedGrants(); len(g) > 0 {
			if g[0] != "t-high" {
				t.Errorf("expected t-high to win, got %v", g)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no grant fired: %v", r.recordedGrants())
}

func TestEvictsIdleBeforeQueueing(t *testing.T) {
	r := newQueueRig(t, 1, 1000, 60)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")
	r.staleActivity(t, "t1", 5*time.Minute)

	res, err := r.queue.Acquire(context.Background(), "t2", "handoff")
	if err != nil {
		t.Fatalf("Acquire t2: %v", err)
	}
	if res.Outcome != AcquireGranted {
		t.Errorf("Outcome: expected granted (via eviction), got %d", res.Outcome)
	}

	t1, _ := r.store.Get(t.Context(), "t1")
	if t1.Status != StatusEvicted {
		t.Errorf("t1 should have been evicted, status=%q", t1.Status)
	}
}

func TestDoesNotEvictWhenIdleThresholdNotMet(t *testing.T) {
	r := newQueueRig(t, 1, 100, 60)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	res, _ := r.queue.Acquire(context.Background(), "t2", "handoff")
	if res.Outcome != AcquireQueued {
		t.Errorf("expected queued, got %d", res.Outcome)
	}
	t1, _ := r.store.Get(t.Context(), "t1")
	if t1.Status == StatusEvicted {
		t.Errorf("t1 should NOT have been evicted — activity is fresh")
	}
}

func TestCancelRemovesFromQueue(t *testing.T) {
	r := newQueueRig(t, 1, 80, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	_, _ = r.queue.Acquire(context.Background(), "t2", "handoff")
	if pos, _ := r.store.Position(t.Context(), "t2"); pos != 0 {
		t.Errorf("t2 should be at pos 0, got %d", pos)
	}

	if err := r.queue.Cancel(t.Context(), "t2"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if pos, _ := r.store.Position(t.Context(), "t2"); pos != -1 {
		t.Errorf("t2 should be gone, got pos %d", pos)
	}
}

func TestPendingGrantSurvivesUntilClaimedOrExpired(t *testing.T) {
	r := newQueueRig(t, 1, 80, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	_, _ = r.queue.Acquire(context.Background(), "t2", "handoff")
	if err := r.queue.Release(t.Context(), "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if r.queue.PendingGrant("t2") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.queue.PendingGrant("t2") {
		t.Fatal("pending grant for t2 was never recorded")
	}

	res, err := r.queue.Acquire(context.Background(), "t3", "in_progress")
	if err != nil {
		t.Fatalf("Acquire t3: %v", err)
	}
	if res.Outcome != AcquireQueued {
		t.Errorf("t3 should have queued (slot reserved for t2), got %d", res.Outcome)
	}

	res, err = r.queue.Acquire(context.Background(), "t2", "handoff")
	if err != nil {
		t.Fatalf("Acquire t2 (claim): %v", err)
	}
	if res.Outcome != AcquireGranted {
		t.Errorf("t2 should have claimed grant, got %d", res.Outcome)
	}
}

func TestParallelAcquireRespectsCap(t *testing.T) {
	const cap = 3
	r := newQueueRig(t, cap, 200, 1800)

	const total = 10
	var grantedN, queuedN, otherN int32
	var errs []error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "task-" + string(rune('a'+i))
			res, err := r.queue.Acquire(context.Background(), id, "in_progress")
			if err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
				return
			}
			switch res.Outcome {
			case AcquireGranted:
				atomic.AddInt32(&grantedN, 1)
			case AcquireQueued:
				atomic.AddInt32(&queuedN, 1)
			default:
				atomic.AddInt32(&otherN, 1)
			}
		}(i)
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Errorf("Acquire errors: %v", errs)
	}
	if int(grantedN) > cap {
		t.Errorf("granted exceeded cap: %d > %d", grantedN, cap)
	}
	if int(grantedN)+int(queuedN)+int(otherN) != total {
		t.Errorf("total mismatch: granted=%d queued=%d other=%d errs=%d (want %d)",
			grantedN, queuedN, otherN, len(errs), total)
	}
}

func TestContextCancellationAbortsWaiter(t *testing.T) {
	r := newQueueRig(t, 1, 5000, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	ctx, cancel := context.WithCancel(context.Background())
	type res struct{ err error }
	resCh := make(chan res, 1)
	go func() {
		_, err := r.queue.Acquire(ctx, "t2", "handoff")
		resCh <- res{err: err}
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case got := <-resCh:
		if got.err == nil {
			t.Error("expected ctx error, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Acquire did not return after ctx cancel")
	}
}

func TestExpiredGrantRespectsWorkspaceCap(t *testing.T) {
	r := newQueueRig(t, 2, 100, 1800)
	r.queue.SetConcurrencyPolicy(func(_ context.Context, taskID string) (string, int) {
		switch taskID {
		case "w1a", "w1b":
			return "W1", 1
		case "w2a":
			return "W2", 0
		}
		return "", 0
	})

	res, err := r.queue.Acquire(context.Background(), "w1a", "in_progress")
	if err != nil || res.Outcome != AcquireGranted {
		t.Fatalf("w1a should be granted: out=%v err=%v", res.Outcome, err)
	}
	r.markReady(t, "w1a")

	r.queue.mu.Lock()
	r.queue.pendingGrants["ghost"] = time.Now().Add(-2 * pendingGrantTTL)
	r.queue.mu.Unlock()

	res, err = r.queue.Acquire(context.Background(), "w1b", "handoff")
	if err != nil || res.Outcome != AcquireQueued {
		t.Fatalf("w1b should be queued (W1 at cap): out=%v err=%v", res.Outcome, err)
	}
	res, err = r.queue.Acquire(context.Background(), "w2a", "refinement")
	if err != nil || res.Outcome != AcquireQueued {
		t.Fatalf("w2a should be queued (global cap full): out=%v err=%v", res.Outcome, err)
	}

	r.queue.expireOnce(t.Context())

	if r.queue.PendingGrant("w1b") {
		t.Error("w1b got the recycled slot but W1 is at its concurrency cap")
	}
	if !r.queue.PendingGrant("w2a") {
		t.Error("w2a (workspace with room) should have received the recycled slot")
	}
}

func TestPerWorkspaceConcurrencyCap(t *testing.T) {
	r := newQueueRig(t, 10, 150, 1800)
	r.queue.SetConcurrencyPolicy(func(_ context.Context, taskID string) (string, int) {
		switch taskID {
		case "w1a", "w1b":
			return "W1", 1
		case "w2a":
			return "W2", 0
		}
		return "", 0
	})

	res, err := r.queue.Acquire(context.Background(), "w1a", "in_progress")
	if err != nil || res.Outcome != AcquireGranted {
		t.Fatalf("w1a should be granted: out=%v err=%v", res.Outcome, err)
	}
	r.markReady(t, "w1a")

	res, err = r.queue.Acquire(context.Background(), "w1b", "in_progress")
	if err != nil {
		t.Fatalf("w1b: %v", err)
	}
	if res.Outcome != AcquireQueued {
		t.Errorf("w1b should be queued (W1 at cap), got %d", res.Outcome)
	}

	res, err = r.queue.Acquire(context.Background(), "w2a", "in_progress")
	if err != nil || res.Outcome != AcquireGranted {
		t.Fatalf("w2a (other tenant) should be granted despite W1 cap: out=%v err=%v", res.Outcome, err)
	}
	r.markReady(t, "w2a")

	if err := r.queue.Release(t.Context(), "w1a"); err != nil {
		t.Fatalf("Release w1a: %v", err)
	}
	res, err = r.queue.Acquire(context.Background(), "w1b", "in_progress")
	if err != nil || res.Outcome != AcquireGranted {
		t.Errorf("w1b should be granted after w1a released: out=%v err=%v", res.Outcome, err)
	}
}

func TestRemovePassesAGrantOn(t *testing.T) {
	r := newQueueRig(t, 1, 80, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")
	_, _ = r.queue.Acquire(context.Background(), "t2", "handoff")
	_, _ = r.queue.Acquire(context.Background(), "t3", "handoff")
	if err := r.queue.Release(t.Context(), "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !r.queue.PendingGrant("t2") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.queue.PendingGrant("t2") {
		t.Fatal("t2 never got the grant")
	}

	if err := r.queue.Remove(t.Context(), "t2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if pos, _ := r.store.Position(t.Context(), "t2"); pos != -1 {
		t.Errorf("t2 still in line at %d", pos)
	}
	for !r.queue.PendingGrant("t3") && time.Now().Before(deadline.Add(time.Second)) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.queue.PendingGrant("t3") {
		t.Errorf("the slot did not pass to t3: grants=%v", r.recordedGrants())
	}
}

func TestReprioritizeReordersTheWaitingTasks(t *testing.T) {
	r := newQueueRig(t, 1, 50, 1800)
	_, _ = r.queue.Acquire(context.Background(), "t1", "in_progress")
	r.markReady(t, "t1")

	_, _ = r.queue.Acquire(context.Background(), "t2", "in_progress")
	_, _ = r.queue.Acquire(context.Background(), "t3", "handoff")
	if pos, _ := r.store.Position(t.Context(), "t3"); pos != 0 {
		t.Fatalf("t3 should lead before the change, got pos %d", pos)
	}

	prios, err := r.settings.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	prios["in_progress"] = prios["handoff"] + 5
	if err := r.settings.SetSandboxPriorities(t.Context(), prios); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	n, err := r.queue.Reprioritize(t.Context())
	if err != nil {
		t.Fatalf("Reprioritize: %v", err)
	}
	if n != 1 {
		t.Errorf("Reprioritize changed %d entries, want 1 (only t2's stage moved)", n)
	}
	if pos, _ := r.store.Position(t.Context(), "t2"); pos != 0 {
		t.Errorf("t2 should lead after its stage outranks handoff, got pos %d", pos)
	}

	delete(prios, "handoff")
	if err := r.settings.SetSandboxPriorities(t.Context(), prios); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	if _, err := r.queue.Reprioritize(t.Context()); err != nil {
		t.Fatalf("Reprioritize: %v", err)
	}
	q, err := r.store.ListQueue(t.Context())
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	for _, e := range q {
		if e.TaskID == "t3" && e.Priority != 0 {
			t.Errorf("t3 priority = %d, want 0 once handoff is gone from the map", e.Priority)
		}
	}
}
