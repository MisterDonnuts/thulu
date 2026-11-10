package sandbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

type fakeRuntime struct {
	mu        sync.Mutex
	n         int
	alive     map[string]bool
	timeouts  []int
	removed   []string
	released  []string
	hold      chan struct{}
	execStart chan struct{}
	exposed   [][]int
	refuse    []error
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{alive: map[string]bool{}} }

func (f *fakeRuntime) Image() string { return "fake" }

func (f *fakeRuntime) Create(_ context.Context, opts CreateOpts) (*CreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.refuse) > 0 {
		err := f.refuse[0]
		f.refuse = f.refuse[1:]
		return nil, err
	}
	f.exposed = append(f.exposed, opts.ExposePorts)
	f.n++
	id := fmt.Sprintf("sb-%d", f.n)
	f.alive[id] = true
	return &CreateResult{ContainerID: id, HostPorts: map[int]int{}}, nil
}

func (f *fakeRuntime) Exec(_ context.Context, _ string, opts ExecOpts) (*ExecResult, error) {
	f.mu.Lock()
	f.timeouts = append(f.timeouts, opts.TimeoutMs)
	hold, started := f.hold, f.execStart
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	return &ExecResult{}, nil
}

func (f *fakeRuntime) Alive(_ context.Context, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[id]
}

func (f *fakeRuntime) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRuntime) ReleaseStorage(_ context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, taskID)
	return nil
}

func newFakeManager(t *testing.T) (*Manager, *Store, *settings.Store, *fakeRuntime) {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_ = sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, 200)
	rt := newFakeRuntime()
	return NewManager(st, sett, rt, ManagerConfig{}), st, sett, rt
}

func TestClampExecTimeout(t *testing.T) {
	const capMs = 30 * 60 * 1000
	cases := []struct{ req, want int }{
		{0, capMs},
		{-1, capMs},
		{5000, 5000},
		{capMs * 4, capMs},
	}
	for _, c := range cases {
		if got := clampExecTimeout(c.req, capMs); got != c.want {
			t.Errorf("clampExecTimeout(%d) = %d, want %d", c.req, got, c.want)
		}
	}
	if got := clampExecTimeout(0, 0); got != settings.DefaultSandboxMaxExecTimeoutMs {
		t.Errorf("zero cap = %d, want the default", got)
	}
}

func TestManagerExecAppliesCap(t *testing.T) {
	m, _, sett, rt := newFakeManager(t)
	ctx := context.Background()
	if err := sett.PutJSON(ctx, settings.SandboxMaxExecTimeoutMsKey, 60_000); err != nil {
		t.Fatal(err)
	}
	for _, req := range []int{0, 10_000, 3_600_000} {
		res, _, err := m.Exec(ctx, "task-cap", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true", TimeoutMs: req})
		if err != nil {
			t.Fatal(err)
		}
		want := min(req, 60_000)
		if req == 0 {
			want = 60_000
		}
		if res.TimeoutMs != want {
			t.Errorf("request %d ran under %d, want %d", req, res.TimeoutMs, want)
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, ms := range rt.timeouts {
		if ms <= 0 || ms > 60_000 {
			t.Errorf("runtime got timeout %d", ms)
		}
	}
}

func backdate(t *testing.T, st *Store, taskID string, by time.Duration) {
	t.Helper()
	sb, err := st.Get(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	sb.LastActivityAt = time.Now().Add(-by).UTC()
	if err := st.Upsert(t.Context(), sb); err != nil {
		t.Fatal(err)
	}
}

func TestReapIdleWithoutSlotPressure(t *testing.T) {
	m, st, sett, rt := newFakeManager(t)
	ctx := context.Background()
	_ = sett.SetSandboxMax(ctx, 10)
	_ = sett.PutJSON(ctx, settings.SandboxIdleEvictSecondsKey, 60)
	for _, id := range []string{"task-idle", "task-fresh"} {
		if _, err := m.Acquire(ctx, id, "in_progress", AcquireDockerOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	backdate(t, st, "task-idle", 5*time.Minute)

	m.GCOnce(ctx)

	if sb, _ := st.Get(ctx, "task-idle"); sb.Status != StatusEvicted {
		t.Errorf("idle sandbox status = %s, want evicted", sb.Status)
	}
	if sb, _ := st.Get(ctx, "task-fresh"); sb.Status != StatusReady {
		t.Errorf("fresh sandbox status = %s, want ready", sb.Status)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.removed) != 1 || rt.removed[0] != "sb-1" {
		t.Errorf("removed %v, want [sb-1]", rt.removed)
	}
	if len(rt.released) != 0 {
		t.Errorf("idle eviction released task storage: %v", rt.released)
	}
}

func TestReapIdleSkipsRunningCommand(t *testing.T) {
	m, st, sett, rt := newFakeManager(t)
	ctx := context.Background()
	_ = sett.PutJSON(ctx, settings.SandboxIdleEvictSecondsKey, 60)
	if _, err := m.Acquire(ctx, "task-build", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.hold = make(chan struct{})
	rt.execStart = make(chan struct{}, 1)
	hold := rt.hold
	started := rt.execStart
	rt.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = m.Exec(ctx, "task-build", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "make"})
	}()
	<-started
	backdate(t, st, "task-build", time.Hour)

	m.ReapIdle(ctx)
	if sb, _ := st.Get(ctx, "task-build"); sb.Status == StatusEvicted {
		t.Fatal("sandbox evicted mid-command")
	}
	close(hold)
	<-done
}

func TestReapIdleSkipsSandboxBeingAcquired(t *testing.T) {
	m, st, sett, rt := newFakeManager(t)
	ctx := context.Background()
	_ = sett.PutJSON(ctx, settings.SandboxIdleEvictSecondsKey, 60)
	if _, err := m.Acquire(ctx, "task-acq", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	backdate(t, st, "task-acq", time.Hour)

	mu := m.taskLock("task-acq")
	mu.Lock()
	m.ReapIdle(ctx)
	mu.Unlock()

	if sb, _ := st.Get(ctx, "task-acq"); sb.Status == StatusEvicted {
		t.Fatal("sandbox evicted while being acquired")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.removed) != 0 {
		t.Fatalf("container removed while being acquired: %v", rt.removed)
	}
}

func TestReapIdleDisabledAtZero(t *testing.T) {
	m, st, sett, _ := newFakeManager(t)
	ctx := context.Background()
	_ = sett.PutJSON(ctx, settings.SandboxIdleEvictSecondsKey, 0)
	if _, err := m.Acquire(ctx, "task-keep", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	backdate(t, st, "task-keep", 24*time.Hour)
	m.ReapIdle(ctx)
	if sb, _ := st.Get(ctx, "task-keep"); sb.Status == StatusEvicted {
		t.Fatal("sweep ran with idle_evict_seconds=0")
	}
}

func TestExecRequestTimeout(t *testing.T) {
	if got := execRequestTimeout(30 * 60 * 1000); got != 31*time.Minute {
		t.Errorf("30m command = %s, want 31m", got)
	}
	if got := execRequestTimeout(0); got != 15*time.Minute {
		t.Errorf("unbounded command = %s, want 15m", got)
	}
}

func TestReapDeadReleasesAfterTwoSweeps(t *testing.T) {
	m, st, _, rt := newFakeManager(t)
	ctx := context.Background()
	for _, id := range []string{"task-dead", "task-live"} {
		if _, err := m.Acquire(ctx, id, "in_progress", AcquireDockerOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.mu.Lock()
	rt.alive["sb-1"] = false
	rt.mu.Unlock()

	m.ReapDead(ctx)
	if sb, _ := st.Get(ctx, "task-dead"); sb.Status != StatusReady {
		t.Fatalf("after one sweep status = %s, want ready (not yet confirmed)", sb.Status)
	}

	m.ReapDead(ctx)
	if sb, _ := st.Get(ctx, "task-dead"); sb.Status != StatusEvicted {
		t.Errorf("after two sweeps status = %s, want evicted", sb.Status)
	}
	if sb, _ := st.Get(ctx, "task-live"); sb.Status != StatusReady {
		t.Errorf("live sandbox status = %s, want ready", sb.Status)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.released) != 0 {
		t.Errorf("a dead container released the task's storage: %v", rt.released)
	}
}

func TestReapDeadForgetsAContainerThatCameBack(t *testing.T) {
	m, st, _, rt := newFakeManager(t)
	ctx := context.Background()
	if _, err := m.Acquire(ctx, "task-flaky", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	setAlive := func(v bool) {
		rt.mu.Lock()
		rt.alive["sb-1"] = v
		rt.mu.Unlock()
	}
	setAlive(false)
	m.ReapDead(ctx)
	setAlive(true)
	m.ReapDead(ctx)
	setAlive(false)
	m.ReapDead(ctx)
	if sb, _ := st.Get(ctx, "task-flaky"); sb.Status != StatusReady {
		t.Errorf("status = %s, want ready: the strikes were not in a row", sb.Status)
	}
}

func TestReapDeadFreesAnAbandonedStart(t *testing.T) {
	m, st, _, _ := newFakeManager(t)
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"prewarm:p-tienda", "task-starting"} {
		if err := st.Upsert(ctx, &Sandbox{TaskID: id, Status: StatusStarting, HostPorts: map[int]int{}, CreatedAt: now, LastActivityAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	busy := m.taskLock("task-starting")
	busy.Lock()
	defer busy.Unlock()

	m.ReapDead(ctx)
	if sb, _ := st.Get(ctx, "prewarm:p-tienda"); sb.Status != StatusStarting {
		t.Fatalf("after one sweep status = %s, want starting (not yet confirmed)", sb.Status)
	}
	m.ReapDead(ctx)
	if sb, _ := st.Get(ctx, "prewarm:p-tienda"); sb.Status != StatusEvicted {
		t.Errorf("after two sweeps status = %s, want evicted", sb.Status)
	}
	if sb, _ := st.Get(ctx, "task-starting"); sb.Status != StatusStarting {
		t.Errorf("a start in progress was reaped: status = %s", sb.Status)
	}
}
