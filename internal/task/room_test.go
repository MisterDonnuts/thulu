package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func waiter(ws, taskID string) *roomWaiter {
	return &roomWaiter{ws: ws, taskID: taskID, wake: make(chan struct{}, 1), cancel: func() {}}
}

func woken(w *roomWaiter) bool {
	select {
	case <-w.wake:
		return true
	default:
		return false
	}
}

func TestRoomLineFIFO(t *testing.T) {
	l := newRoomLine()
	a, b, other := waiter("ws1", ""), waiter("ws1", ""), waiter("ws2", "")
	l.join(a)
	l.join(b)
	l.join(other)
	if !l.head(a) || l.head(b) || !l.head(other) {
		t.Fatal("each workspace's first arrival leads its own line")
	}
	if !l.busy("ws1") || l.busy("ws3") || l.count() != 3 {
		t.Fatalf("busy/count wrong: count=%d", l.count())
	}
	l.leave(a)
	if !l.head(b) || !woken(b) {
		t.Error("the next run must lead, and be told so, when the head leaves")
	}
	l.leave(other)
	l.leave(b)
	if l.busy("ws1") || l.busy("ws2") || l.count() != 0 {
		t.Error("an empty line must not count as busy")
	}
}

func TestRoomLineNewerLaunchReplacesTheWaitingOne(t *testing.T) {
	l := newRoomLine()
	canceled := false
	old := waiter("ws1", "task1")
	old.cancel = func() { canceled = true }
	l.join(old)
	newer := waiter("ws1", "task1")
	l.join(newer)
	if !canceled || l.current(old) || !l.current(newer) {
		t.Fatal("a newer launch of the task must cancel the waiting one")
	}
	l.leave(old)
	if !l.waitingTask("task1") {
		t.Error("the replaced waiter leaving must not forget the newer one")
	}
	l.drop("task1")
	l.leave(newer)
	if l.waitingTask("task1") {
		t.Error("leaving must forget the task")
	}
}

func TestRoomLineKickWhere(t *testing.T) {
	l := newRoomLine()
	a, a2, b := waiter("ws1", ""), waiter("ws1", ""), waiter("ws2", "")
	l.join(a)
	l.join(a2)
	l.join(b)
	l.kickWhere(func(ws string) bool { return ws == "ws1" })
	if !woken(a) || woken(a2) || woken(b) {
		t.Error("a pod end must wake only the head of its workspace's line")
	}
}

func TestNoCapacityWraps(t *testing.T) {
	cause := errors.New("quota full")
	err := NoCapacity(cause)
	if !errors.Is(err, ErrNoCapacity) || !errors.Is(err, cause) || err.Error() != "quota full" {
		t.Errorf("NoCapacity must match both and keep the message: %v", err)
	}
	if NoCapacity(nil) != nil {
		t.Error("NoCapacity(nil) must be nil")
	}
}

type roomLauncher struct {
	*recordingLauncher
	mu       sync.Mutex
	open     bool
	refusals int
}

func (l *roomLauncher) LaunchForStage(ctx context.Context, t *Task, s Stage, at *types.AgentType,
	env, wt, sm map[string]string, ro bool,
) (string, string, string, error) {
	l.mu.Lock()
	if !l.open {
		l.refusals++
		l.mu.Unlock()
		return "", "", "", NoCapacity(errors.New("exceeded quota: requests.cpu"))
	}
	l.mu.Unlock()
	return l.recordingLauncher.LaunchForStage(ctx, t, s, at, env, wt, sm, ro)
}

func (l *roomLauncher) setOpen() {
	l.mu.Lock()
	l.open = true
	l.mu.Unlock()
}

func (l *roomLauncher) refused() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refusals
}

func newRoomDispatcher(t *testing.T) (*Dispatcher, *Store, *roomLauncher) {
	t.Helper()
	d, store := newStageTestDispatcher(t, customBoard())
	l := &roomLauncher{recordingLauncher: newRecordingLauncher()}
	d.launcher = l
	d.room.retryFirst, d.room.retryMax = time.Hour, time.Hour
	return d, store, l
}

func statusOf(t *testing.T, store *Store, id string) RunStatus {
	t.Helper()
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Status
}

func TestStageRunWaitsForRoom(t *testing.T) {
	d, store, l := newRoomDispatcher(t)
	tk := gatedTask(t, store)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	}()
	waitFor(t, func() bool { return d.RoomWaiting() == 1 })

	if s := statusOf(t, store, tk.ID); s != StatusQueued {
		t.Errorf("status = %q, want queued", s)
	}
	if q, ok := store.latestQueuedRun(t.Context(), tk.ID); !ok || q.Stage != string(tk.Stage) ||
		q.LaunchKind != string(LaunchStage) || !strings.Contains(q.Reason, "exceeded quota") {
		t.Errorf("run_queued event = %+v, %v", q, ok)
	}
	if st, _ := d.AdmissionStats(); st.Inflight != 0 {
		t.Errorf("a waiting run must give its slot back, inflight = %d", st.Inflight)
	}
	if !strings.Contains(lastComment(t, store, tk.ID), "developer") {
		t.Error("the task must say the run is waiting")
	}

	d.RoomFreed(func(ws string) bool { return ws == "elsewhere" })
	time.Sleep(30 * time.Millisecond)
	if n := l.refused(); n != 1 {
		t.Fatalf("refusals = %d after another workspace's pod ended, want 1", n)
	}

	l.setOpen()
	d.RoomFreed(func(ws string) bool { return ws == testWS })
	<-done
	if s := statusOf(t, store, tk.ID); s != StatusRunning {
		t.Errorf("status = %q after room freed, want running", s)
	}
	if d.RoomWaiting() != 0 {
		t.Error("the started run must leave the line")
	}
	if st, _ := d.AdmissionStats(); st.Inflight != 1 {
		t.Errorf("the started run holds its slot, inflight = %d", st.Inflight)
	}
}

func TestNewcomerQueuesBehindTheLine(t *testing.T) {
	d, store, l := newRoomDispatcher(t)
	first, second := gatedTask(t, store), gatedTask(t, store)
	var wg sync.WaitGroup
	launch := func(tk *Task) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
		}()
	}
	launch(first)
	waitFor(t, func() bool { return d.RoomWaiting() == 1 })
	launch(second)
	waitFor(t, func() bool { return d.RoomWaiting() == 2 })
	if n := l.refused(); n != 1 {
		t.Errorf("refusals = %d, the newcomer must not try while the line waits", n)
	}

	l.setOpen()
	d.RoomFreed(func(string) bool { return true })
	wg.Wait()
	for _, tk := range []*Task{first, second} {
		if s := statusOf(t, store, tk.ID); s != StatusRunning {
			t.Errorf("%s status = %q, want running", tk.ID, s)
		}
	}
	if got := l.recordingLauncher.launchedTasks(); len(got) != 2 || got[0] != first.ID {
		t.Errorf("launch order = %v, want %s first", got, first.ID)
	}
}

func TestStageMoveDropsTheWait(t *testing.T) {
	d, store, l := newRoomDispatcher(t)
	tk := gatedTask(t, store)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	}()
	waitFor(t, func() bool { return d.RoomWaiting() == 1 })

	d.room.drop(tk.ID)
	<-done
	if d.RoomWaiting() != 0 || l.refused() != 1 {
		t.Errorf("waiting=%d refusals=%d after the move", d.RoomWaiting(), l.refused())
	}
	if st, _ := d.AdmissionStats(); st.Inflight != 0 {
		t.Errorf("inflight = %d after a dropped wait", st.Inflight)
	}
	if s := statusOf(t, store, tk.ID); s != StatusIdle {
		t.Errorf("status = %q after a dropped wait, want idle", s)
	}
}

func TestRoomWaitGivesUp(t *testing.T) {
	d, store, _ := newRoomDispatcher(t)
	d.room.minute = 20 * time.Millisecond
	d.WithMaxQueueMinutes(func() int { return 1 })
	tk := gatedTask(t, store)
	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	if s := statusOf(t, store, tk.ID); s != StatusNotStarted {
		t.Errorf("status = %q after the wait limit, want not_started", s)
	}
	if body := lastComment(t, store, tk.ID); !strings.Contains(body, "exceeded quota") {
		t.Errorf("the failure must name the last refusal:\n%s", body)
	}
	if d.RoomWaiting() != 0 {
		t.Error("a run that gave up must leave the line")
	}
}

func TestOtherLaunchErrorsDoNotQueue(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.launcher = failingLauncher{newRecordingLauncher()}
	tk := gatedTask(t, store)
	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	if d.RoomWaiting() != 0 {
		t.Error("a failure that is not for room must not queue")
	}
	if s := statusOf(t, store, tk.ID); s == StatusQueued {
		t.Error("the task must not show queued")
	}
}

func TestResumeQueued(t *testing.T) {
	d, store, l := newRoomDispatcher(t)
	l.setOpen()
	agent := &types.AgentType{Name: "developer", ValidStages: []types.Stage{"verify"}}
	d.launcher = &findingLauncher{roomLauncher: l, agent: agent}

	assigned := func() *Task {
		tk := &Task{WorkspaceID: testWS, Title: "ship it", Stage: "verify",
			StageAssignments: map[Stage]string{"verify": "developer"}}
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	plain, resumed := assigned(), assigned()
	d.setStatus(t.Context(), plain.ID, StatusQueued)
	if err := store.RecordEvent(t.Context(), plain.ID, EventRunQueued, plain.ID, "octopus", "q",
		queuedRun{Stage: string(plain.Stage), Agent: "developer", LaunchKind: string(LaunchStage), PrevStatus: string(StatusIdle)}); err != nil {
		t.Fatal(err)
	}
	d.setStatus(t.Context(), resumed.ID, StatusQueued)
	if err := store.RecordEvent(t.Context(), resumed.ID, EventRunQueued, resumed.ID, "octopus", "q",
		queuedRun{Stage: string(resumed.Stage), Agent: "developer", LaunchKind: string(LaunchContinuation), PrevStatus: string(StatusAwaitingInput)}); err != nil {
		t.Fatal(err)
	}

	d.ResumeQueued(t.Context())

	waitFor(t, func() bool { return statusOf(t, store, plain.ID) == StatusRunning })
	if s := statusOf(t, store, resumed.ID); s != StatusAwaitingInput {
		t.Errorf("resumed status = %q, want its previous awaiting_input", s)
	}
	if body := lastComment(t, store, resumed.ID); !strings.Contains(body, "developer") {
		t.Errorf("the task must say the run has to be sent again:\n%s", body)
	}
}

type findingLauncher struct {
	*roomLauncher
	agent *types.AgentType
}

func (l *findingLauncher) FindAgentType(_, name string) *types.AgentType {
	if name == l.agent.Name {
		return l.agent
	}
	return nil
}
