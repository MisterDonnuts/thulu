package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db := testpg.New(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func newSandbox(taskID, containerID string, status Status) *Sandbox {
	now := time.Now().UTC()
	return &Sandbox{
		TaskID:         taskID,
		ContainerID:    containerID,
		Image:          "octopus-runtime:latest",
		Status:         status,
		HostPorts:      map[int]int{3000: 30001, 5173: 30002},
		CreatedAt:      now,
		LastActivityAt: now,
	}
}

func TestUpsertAndGet(t *testing.T) {
	s := newStore(t)
	in := newSandbox("task-1", "c1", StatusReady)

	if err := s.Upsert(t.Context(), in); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContainerID != "c1" {
		t.Errorf("ContainerID: got %q", got.ContainerID)
	}
	if got.Status != StatusReady {
		t.Errorf("Status: got %q", got.Status)
	}
	if got.HostPorts[3000] != 30001 || got.HostPorts[5173] != 30002 {
		t.Errorf("HostPorts roundtrip mismatch: %v", got.HostPorts)
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.Get(t.Context(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpsertReplacesExisting(t *testing.T) {
	s := newStore(t)
	if err := s.Upsert(t.Context(), newSandbox("task-1", "c1", StatusStarting)); err != nil {
		t.Fatal(err)
	}
	updated := newSandbox("task-1", "c1", StatusReady)
	updated.HostPorts = map[int]int{8080: 30099}
	if err := s.Upsert(t.Context(), updated); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), "task-1")
	if got.Status != StatusReady {
		t.Errorf("Status not updated: %q", got.Status)
	}
	if _, ok := got.HostPorts[3000]; ok {
		t.Errorf("HostPorts not replaced; still has 3000: %v", got.HostPorts)
	}
	if got.HostPorts[8080] != 30099 {
		t.Errorf("HostPorts replacement: %v", got.HostPorts)
	}
}

func TestTouchUpdatesActivity(t *testing.T) {
	s := newStore(t)
	in := newSandbox("task-1", "c1", StatusReady)
	in.LastActivityAt = time.Now().Add(-10 * time.Minute).UTC()
	if err := s.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	if err := s.Touch(t.Context(), "task-1"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _ := s.Get(t.Context(), "task-1")
	if time.Since(got.LastActivityAt) > 5*time.Second {
		t.Errorf("LastActivityAt not updated: %v", got.LastActivityAt)
	}
}

func TestTouchMissingReturnsErrNotFound(t *testing.T) {
	s := newStore(t)
	if err := s.Touch(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSetStatus(t *testing.T) {
	s := newStore(t)
	_ = s.Upsert(t.Context(), newSandbox("t", "c", StatusStarting))
	if err := s.SetStatus(t.Context(), "t", StatusReady); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), "t")
	if got.Status != StatusReady {
		t.Errorf("Status: %q", got.Status)
	}
}

func TestMarkReleasedFlipsStatusAndStamps(t *testing.T) {
	s := newStore(t)
	_ = s.Upsert(t.Context(), newSandbox("t", "c", StatusReady))
	if err := s.MarkReleased(t.Context(), "t"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), "t")
	if got.Status != StatusEvicted {
		t.Errorf("Status: %q", got.Status)
	}
	if got.ReleasedAt == nil {
		t.Errorf("ReleasedAt should be set")
	}
}

func TestCountLiveExcludesEvictedAndFailed(t *testing.T) {
	s := newStore(t)
	_ = s.Upsert(t.Context(), newSandbox("t1", "c1", StatusReady))
	_ = s.Upsert(t.Context(), newSandbox("t2", "c2", StatusStarting))
	_ = s.Upsert(t.Context(), newSandbox("t3", "c3", StatusEvicted))
	_ = s.Upsert(t.Context(), newSandbox("t4", "c4", StatusFailed))

	n, err := s.CountLive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("CountLive: expected 2, got %d", n)
	}
}

func TestListLiveExcludesEvicted(t *testing.T) {
	s := newStore(t)
	_ = s.Upsert(t.Context(), newSandbox("t1", "c1", StatusReady))
	_ = s.Upsert(t.Context(), newSandbox("t2", "c2", StatusEvicted))

	live, err := s.ListLive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Errorf("ListLive: expected 1, got %d", len(live))
	}
	if live[0].TaskID != "t1" {
		t.Errorf("ListLive: wrong task: %q", live[0].TaskID)
	}
}

func TestIdleEvictionCandidate(t *testing.T) {
	s := newStore(t)

	old := newSandbox("t1", "c1", StatusReady)
	old.LastActivityAt = time.Now().Add(-1 * time.Hour).UTC()
	_ = s.Upsert(t.Context(), old)

	young := newSandbox("t2", "c2", StatusReady)
	young.LastActivityAt = time.Now().UTC()
	_ = s.Upsert(t.Context(), young)

	c, err := s.IdleEvictionCandidate(t.Context(), time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.TaskID != "t1" {
		t.Errorf("expected t1, got %+v", c)
	}

	c, err = s.IdleEvictionCandidate(t.Context(), time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Errorf("expected nil, got %+v", c)
	}
}

func TestIdleEvictionCandidateSkipsEvidenceApps(t *testing.T) {
	s := newStore(t)

	app := newSandbox(EvidenceOwner("b1"), "c1", StatusReady)
	app.LastActivityAt = time.Now().Add(-2 * time.Hour).UTC()
	_ = s.Upsert(t.Context(), app)

	task := newSandbox("t1", "c2", StatusReady)
	task.LastActivityAt = time.Now().Add(-1 * time.Hour).UTC()
	_ = s.Upsert(t.Context(), task)

	c, err := s.IdleEvictionCandidate(t.Context(), time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.TaskID != "t1" {
		t.Errorf("expected t1, got %+v", c)
	}
}

func TestEnqueueDequeue(t *testing.T) {
	s := newStore(t)
	if err := s.Enqueue(t.Context(), "t1", "in_progress", 50); err != nil {
		t.Fatal(err)
	}

	q, _ := s.ListQueue(t.Context())
	if len(q) != 1 {
		t.Errorf("queue length: expected 1, got %d", len(q))
	}

	if err := s.Dequeue(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.ListQueue(t.Context())
	if len(q) != 0 {
		t.Errorf("queue length after dequeue: %d", len(q))
	}
}

func TestNextRespectsPriorityThenFIFO(t *testing.T) {
	s := newStore(t)

	_ = s.Enqueue(t.Context(), "t-low-old", "refinement", 20)
	time.Sleep(10 * time.Millisecond)
	_ = s.Enqueue(t.Context(), "t-high", "handoff", 100)
	time.Sleep(10 * time.Millisecond)
	_ = s.Enqueue(t.Context(), "t-mid", "in_progress", 50)

	first, err := s.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskID != "t-high" {
		t.Errorf("highest priority should win: got %q", first.TaskID)
	}

	_ = s.Enqueue(t.Context(), "t-mid-2", "in_progress", 50)
	_ = s.Dequeue(t.Context(), "t-high")
	first, _ = s.Next(t.Context())
	if first.TaskID != "t-mid" {
		t.Errorf("FIFO within priority: expected t-mid, got %q", first.TaskID)
	}
}

func TestNextEmptyReturnsNilNil(t *testing.T) {
	s := newStore(t)
	e, err := s.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Errorf("expected nil entry, got %+v", e)
	}
}

func TestPosition(t *testing.T) {
	s := newStore(t)
	_ = s.Enqueue(t.Context(), "t-low", "refinement", 20)
	_ = s.Enqueue(t.Context(), "t-high", "handoff", 100)
	_ = s.Enqueue(t.Context(), "t-mid", "in_progress", 50)

	if pos, _ := s.Position(t.Context(), "t-high"); pos != 0 {
		t.Errorf("t-high pos: %d", pos)
	}
	if pos, _ := s.Position(t.Context(), "t-mid"); pos != 1 {
		t.Errorf("t-mid pos: %d", pos)
	}
	if pos, _ := s.Position(t.Context(), "t-low"); pos != 2 {
		t.Errorf("t-low pos: %d", pos)
	}
	if pos, _ := s.Position(t.Context(), "not-queued"); pos != -1 {
		t.Errorf("non-queued: %d", pos)
	}
}

func TestEnqueueIdempotentRefreshesPriority(t *testing.T) {
	s := newStore(t)
	_ = s.Enqueue(t.Context(), "t1", "refinement", 20)
	if err := s.Enqueue(t.Context(), "t1", "handoff", 100); err != nil {
		t.Fatal(err)
	}
	q, _ := s.ListQueue(t.Context())
	if len(q) != 1 {
		t.Errorf("expected 1 row (idempotent), got %d", len(q))
	}
	first, _ := s.Next(t.Context())
	if first.Priority != 100 {
		t.Errorf("priority not updated: %d", first.Priority)
	}
	if first.Stage != "handoff" {
		t.Errorf("stage not updated: %q", first.Stage)
	}
}

func TestListQueueReturnsInPriorityOrder(t *testing.T) {
	s := newStore(t)
	_ = s.Enqueue(t.Context(), "t-low", "refinement", 20)
	_ = s.Enqueue(t.Context(), "t-high", "handoff", 100)
	_ = s.Enqueue(t.Context(), "t-mid", "in_progress", 50)

	all, err := s.ListQueue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}
	expectedOrder := []string{"t-high", "t-mid", "t-low"}
	for i, e := range all {
		if e.TaskID != expectedOrder[i] {
			t.Errorf("pos %d: expected %q, got %q", i, expectedOrder[i], e.TaskID)
		}
	}
}

func TestStore_DB_Accessor(t *testing.T) {
	s := newStore(t)
	if s.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestStore_ListAll(t *testing.T) {
	s := newStore(t)
	_ = s.Upsert(t.Context(), newSandbox("ta1", "c1", StatusReady))
	_ = s.Upsert(t.Context(), newSandbox("ta2", "c2", StatusEvicted))

	all, err := s.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected at least 2 sandboxes, got %d", len(all))
	}
}

func TestDeleteAndOwnersInWorkspace(t *testing.T) {
	s := newStore(t)
	for _, sb := range []*Sandbox{
		newSandbox("task-a", "ca", StatusReady),
		newSandbox("task-b", "cb", StatusEvicted),
		newSandbox("task-c", "cc", StatusReady),
	} {
		sb.WorkspaceID = "ws1"
		if sb.TaskID == "task-c" {
			sb.WorkspaceID = "ws2"
		}
		if err := s.Upsert(t.Context(), sb); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	if err := s.Enqueue(t.Context(), "task-a", "in_progress", 50); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	owners, err := s.OwnersInWorkspace(t.Context(), "ws1")
	if err != nil {
		t.Fatalf("OwnersInWorkspace: %v", err)
	}
	if len(owners) != 2 {
		t.Errorf("ws1 owners = %v, want task-a and task-b", owners)
	}

	if err := s.Delete(t.Context(), "task-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(t.Context(), "task-a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("row survived Delete: %v", err)
	}
	if pos, _ := s.Position(t.Context(), "task-a"); pos != -1 {
		t.Errorf("queue entry survived Delete (position %d)", pos)
	}
	if _, err := s.Get(t.Context(), "task-c"); err != nil {
		t.Errorf("another owner's row must survive: %v", err)
	}
	if err := s.Delete(t.Context(), "task-a"); err != nil {
		t.Errorf("Delete is not idempotent: %v", err)
	}
}

type removeRecorder struct{ removed []string }

func (r *removeRecorder) Image() string { return "runtime:test" }
func (r *removeRecorder) Create(context.Context, CreateOpts) (*CreateResult, error) {
	return nil, errors.New("not used")
}
func (r *removeRecorder) Exec(context.Context, string, ExecOpts) (*ExecResult, error) {
	return nil, errors.New("not used")
}
func (r *removeRecorder) Alive(context.Context, string) bool { return false }
func (r *removeRecorder) Remove(_ context.Context, id string) error {
	r.removed = append(r.removed, id)
	return nil
}

func TestManagerDestroyRemovesContainerAndRow(t *testing.T) {
	s := newStore(t)
	sett, err := settings.NewStore(s.DB())
	if err != nil {
		t.Fatalf("settings.NewStore: %v", err)
	}
	rt := &removeRecorder{}
	mgr := NewManager(s, sett, rt, ManagerConfig{})
	if err := s.Upsert(t.Context(), newSandbox("task-d", "cd", StatusReady)); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Destroy(t.Context(), "task-d"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(rt.removed) != 1 || rt.removed[0] != "cd" {
		t.Errorf("removed = %v, want [cd]", rt.removed)
	}
	if _, err := s.Get(t.Context(), "task-d"); !errors.Is(err, ErrNotFound) {
		t.Errorf("row survived Destroy: %v", err)
	}
	if err := mgr.Destroy(t.Context(), "task-d"); err != nil {
		t.Errorf("second Destroy: %v", err)
	}
}
