package sandbox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"

	"github.com/MisterDonnuts/thulu/internal/testdocker"
)

type managerRig struct {
	mgr      *Manager
	store    *Store
	settings *settings.Store
	docker   *Docker

	mu     sync.Mutex
	grants []string
}

func newManagerRig(t *testing.T, max int, blockMs int, idleSecs int) *managerRig {
	t.Helper()
	cli := testdocker.Client(t)
	requireRuntimeImage(t, cli)

	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatalf("settings.NewStore: %v", err)
	}
	_ = sett.SetSandboxMax(t.Context(), max)
	_ = sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, blockMs)
	_ = sett.PutJSON(t.Context(), settings.SandboxIdleEvictSecondsKey, idleSecs)

	dock := NewDocker(cli, DefaultRuntimeImage)

	rig := &managerRig{store: st, settings: sett, docker: dock}
	rig.mgr = NewManager(st, sett, dock, ManagerConfig{
		PublicHost: "localhost",
		OnSlotGranted: func(taskID, stage string) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.grants = append(rig.grants, taskID)
		},
	})

	t.Cleanup(func() {
		all, _ := st.ListAll(t.Context())
		for _, sb := range all {
			if sb.ContainerID != "" {
				_ = dock.Remove(context.Background(), sb.ContainerID)
			}
		}
	})
	return rig
}

func TestManagerAcquireGrantedCreatesContainer(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()

	res, err := r.mgr.Acquire(ctx, "task-mg-1", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if res.Outcome != AcquireGranted {
		t.Errorf("Outcome: %d", res.Outcome)
	}
	if res.ContainerID == "" {
		t.Errorf("ContainerID empty")
	}
	if !r.docker.Alive(ctx, res.ContainerID) {
		t.Errorf("container not alive: %s", res.ContainerID)
	}

	sb, _ := r.store.Get(ctx, "task-mg-1")
	if sb.Status != StatusReady {
		t.Errorf("Status: %q", sb.Status)
	}
	if len(sb.HostPorts) == 0 {
		t.Errorf("HostPorts empty")
	}

	_ = r.mgr.Release(ctx, "task-mg-1")
}

func TestManagerAcquireExistingReturnsSameContainer(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()

	first, err := r.mgr.Acquire(ctx, "task-mg-2", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatalf("Acquire #1: %v", err)
	}
	t.Cleanup(func() { _ = r.mgr.Release(ctx, "task-mg-2") })

	second, err := r.mgr.Acquire(ctx, "task-mg-2", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatalf("Acquire #2: %v", err)
	}
	if second.Outcome != AcquireExisting {
		t.Errorf("Outcome: %d", second.Outcome)
	}
	if second.ContainerID != first.ContainerID {
		t.Errorf("ContainerID: %q vs %q", second.ContainerID, first.ContainerID)
	}
}

func TestManagerExecRunsInsideTaskSandbox(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()
	t.Cleanup(func() { _ = r.mgr.Release(ctx, "task-mg-3") })

	res, ar, err := r.mgr.Exec(ctx, "task-mg-3", "in_progress", AcquireDockerOpts{},
		ExecOpts{Command: "echo from-manager"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if ar.Outcome != AcquireGranted {
		t.Errorf("Outcome: %d", ar.Outcome)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode: %d (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "from-manager") {
		t.Errorf("Stdout: %q", res.Stdout)
	}
}

func TestManagerExecTouchesActivity(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()
	t.Cleanup(func() { _ = r.mgr.Release(ctx, "task-mg-4") })

	_, _, err := r.mgr.Exec(ctx, "task-mg-4", "in_progress", AcquireDockerOpts{},
		ExecOpts{Command: "true"})
	if err != nil {
		t.Fatalf("Exec #1: %v", err)
	}

	old := time.Now().Add(-1 * time.Hour).UTC()
	sb, _ := r.store.Get(ctx, "task-mg-4")
	sb.LastActivityAt = old
	_ = r.store.Upsert(ctx, sb)

	_, _, err = r.mgr.Exec(ctx, "task-mg-4", "in_progress", AcquireDockerOpts{},
		ExecOpts{Command: "true"})
	if err != nil {
		t.Fatalf("Exec #2: %v", err)
	}
	sb, _ = r.store.Get(ctx, "task-mg-4")
	if time.Since(sb.LastActivityAt) > 5*time.Second {
		t.Errorf("Activity not touched: %v", sb.LastActivityAt)
	}
}

func TestManagerPreviewURLOnExposedPort(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()
	t.Cleanup(func() { _ = r.mgr.Release(ctx, "task-mg-5") })

	_, err := r.mgr.Acquire(ctx, "task-mg-5", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := r.mgr.PreviewURL(ctx, "task-mg-5", 3000)
	if err != nil {
		t.Fatalf("PreviewURL: %v", err)
	}
	if !strings.HasPrefix(info.HumanURL, "http://localhost:") {
		t.Errorf("HumanURL shape: %s", info.HumanURL)
	}
	if info.URL != "http://octopus-sb-task-mg-5:3000" {
		t.Errorf("URL shape: %s", info.URL)
	}
	if info.Port != 3000 {
		t.Errorf("Port: %d", info.Port)
	}
}

func TestManagerPreviewURLOnUnexposedPortErrors(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()
	t.Cleanup(func() { _ = r.mgr.Release(ctx, "task-mg-6") })

	_, _ = r.mgr.Acquire(ctx, "task-mg-6", "in_progress", AcquireDockerOpts{})
	_, err := r.mgr.PreviewURL(ctx, "task-mg-6", 9999)
	if err == nil {
		t.Error("expected error for unexposed port")
	}
}

func TestManagerReleaseTearsDownContainer(t *testing.T) {
	r := newManagerRig(t, 3, 200, 1800)
	ctx := context.Background()

	res, _ := r.mgr.Acquire(ctx, "task-mg-7", "in_progress", AcquireDockerOpts{})
	containerID := res.ContainerID

	if err := r.mgr.Release(ctx, "task-mg-7"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if r.docker.Alive(ctx, containerID) {
		t.Errorf("container still alive after Release")
	}

	sb, _ := r.store.Get(ctx, "task-mg-7")
	if sb.Status != StatusEvicted {
		t.Errorf("Status: %q", sb.Status)
	}
}

func TestManagerCapacityWaterline(t *testing.T) {
	r := newManagerRig(t, 2, 150, 1800)
	ctx := context.Background()

	for _, id := range []string{"task-cap-1", "task-cap-2"} {
		res, err := r.mgr.Acquire(ctx, id, "in_progress", AcquireDockerOpts{})
		if err != nil {
			t.Fatalf("Acquire %s: %v", id, err)
		}
		if res.Outcome != AcquireGranted {
			t.Errorf("%s outcome: %d", id, res.Outcome)
		}
		t.Cleanup(func() { _ = r.mgr.Release(ctx, id) })
	}

	res, err := r.mgr.Acquire(ctx, "task-cap-3", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatalf("Acquire #3: %v", err)
	}
	if res.Outcome != AcquireQueued {
		t.Errorf("Outcome: expected queued, got %d", res.Outcome)
	}
}

func TestManagerReleaseUnblocksQueuedTask(t *testing.T) {
	r := newManagerRig(t, 1, 5000, 1800)
	ctx := context.Background()

	_, err := r.mgr.Acquire(ctx, "first", "in_progress", AcquireDockerOpts{})
	if err != nil {
		t.Fatal(err)
	}

	type res struct {
		ar  *AcquireResult
		err error
	}
	resCh := make(chan res, 1)
	go func() {
		ar, err := r.mgr.Acquire(ctx, "second", "handoff", AcquireDockerOpts{})
		resCh <- res{ar: ar, err: err}
	}()

	time.Sleep(150 * time.Millisecond)
	if err := r.mgr.Release(ctx, "first"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case got := <-resCh:
		if got.err != nil {
			t.Fatalf("Acquire second: %v", got.err)
		}
		if got.ar.Outcome != AcquireGranted {
			t.Errorf("Outcome: %d", got.ar.Outcome)
		}
		if !r.docker.Alive(ctx, got.ar.ContainerID) {
			t.Errorf("second container not alive")
		}
		_ = r.mgr.Release(ctx, "second")
	case <-time.After(15 * time.Second):
		t.Fatal("queued acquire never unblocked")
	}
}
