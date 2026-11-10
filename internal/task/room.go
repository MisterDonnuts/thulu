package task

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

// A launch refused for lack of workspace room (Kubernetes ResourceQuota): the dispatcher queues
// instead of failing. Launchers wrap with NoCapacity.
var ErrNoCapacity error = apperr.Unavailable("task.errors.no_capacity")

func NoCapacity(err error) error {
	if err == nil {
		return nil
	}
	return noCapacityError{err}
}

type noCapacityError struct{ error }

func (e noCapacityError) Unwrap() error        { return e.error }
func (e noCapacityError) Is(target error) bool { return target == ErrNoCapacity }

// The caller cleans up silently; whoever dropped the wait already reported it.
var errRoomDropped error = apperr.New("task.errors.room_dropped")

const (
	roomRetryFirst = 5 * time.Second
	roomRetryMax   = 30 * time.Second
)

// Only the head of a workspace's line retries, so the first run refused launches first and a
// newcomer cannot take its room.
type roomLine struct {
	mu     sync.Mutex
	lines  map[string][]*roomWaiter
	byTask map[string]*roomWaiter

	retryFirst, retryMax, minute time.Duration
}

type roomWaiter struct {
	ws     string
	taskID string
	wake   chan struct{}
	cancel context.CancelFunc
}

func newRoomLine() *roomLine {
	return &roomLine{
		lines: map[string][]*roomWaiter{}, byTask: map[string]*roomWaiter{},
		retryFirst: roomRetryFirst, retryMax: roomRetryMax, minute: time.Minute,
	}
}

func (l *roomLine) busy(ws string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines[ws]) > 0
}

func (l *roomLine) join(w *roomWaiter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w.taskID != "" {
		if old := l.byTask[w.taskID]; old != nil {
			old.cancel()
		}
		l.byTask[w.taskID] = w
	}
	l.lines[w.ws] = append(l.lines[w.ws], w)
}

func (l *roomLine) leave(w *roomWaiter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w.taskID != "" && l.byTask[w.taskID] == w {
		delete(l.byTask, w.taskID)
	}
	q := l.lines[w.ws]
	for i, x := range q {
		if x != w {
			continue
		}
		q = append(q[:i:i], q[i+1:]...)
		if len(q) == 0 {
			delete(l.lines, w.ws)
			return
		}
		l.lines[w.ws] = q
		if i == 0 {
			signal(q[0].wake)
		}
		return
	}
}

func (l *roomLine) head(w *roomWaiter) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.lines[w.ws]
	return len(q) > 0 && q[0] == w
}

func (l *roomLine) current(w *roomWaiter) bool {
	if w.taskID == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byTask[w.taskID] == w
}

func (l *roomLine) kick(ws string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if q := l.lines[ws]; len(q) > 0 {
		signal(q[0].wake)
	}
}

func (l *roomLine) kickWhere(match func(ws string) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ws, q := range l.lines {
		if len(q) > 0 && match(ws) {
			signal(q[0].wake)
		}
	}
}

func (l *roomLine) drop(taskID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.byTask[taskID]; w != nil {
		w.cancel()
	}
}

func (l *roomLine) waitingTask(taskID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byTask[taskID] != nil
}

func (l *roomLine) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, q := range l.lines {
		n += len(q)
	}
	return n
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

type launched struct {
	containerID, instanceID, outputDir string
}

type roomRun struct {
	ws     string
	taskID string
	gate   *agentSlots
	launch func(ctx context.Context) (launched, error)
	parked func(err error)
	wanted func(ctx context.Context) bool
}

// Caller holds a slot of r.gate. It is released while waiting and reacquired per try; held on
// success, released on error.
func (d *Dispatcher) launchWithRoom(ctx context.Context, r roomRun) (launched, error) {
	out, waiting, lastErr := d.firstTry(ctx, r)
	if !waiting {
		return out, lastErr
	}
	r.gate.release(r.ws)

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &roomWaiter{ws: r.ws, taskID: r.taskID, wake: make(chan struct{}, 1), cancel: cancel}
	d.room.join(w)
	defer d.room.leave(w)

	minutes := d.maxQueueMinutes()
	deadline := time.NewTimer(time.Duration(minutes) * d.room.minute)
	defer deadline.Stop()
	backoff := d.room.retryFirst
	for {
		woke, waitErr := d.awaitTurn(wctx, w, deadline.C, backoff)
		switch {
		case errors.Is(waitErr, errRoomDropped):
			return launched{}, waitErr
		case waitErr != nil:
			return launched{}, apperr.Unavailable("task.errors.room_timeout",
				"minutes", minutes, "err", lastErrOr(lastErr))
		case !woke:
			backoff = min(backoff*2, d.room.retryMax)
		}
		if !d.room.current(w) || !r.wanted(wctx) {
			return launched{}, errRoomDropped
		}
		if !d.room.head(w) {
			continue
		}
		if r.gate.acquire(wctx, r.ws) != nil {
			return launched{}, errRoomDropped
		}
		out, err := r.launch(wctx)
		if err == nil {
			return out, nil
		}
		r.gate.release(r.ws)
		if !errors.Is(err, ErrNoCapacity) {
			return launched{}, err
		}
		lastErr = err
	}
}

func (d *Dispatcher) firstTry(ctx context.Context, r roomRun) (out launched, waiting bool, err error) {
	if d.room.busy(r.ws) {
		r.parked(apperr.Unavailable("task.errors.room_line"))
		return launched{}, true, nil
	}
	out, err = r.launch(ctx)
	if err == nil {
		return out, false, nil
	}
	if !errors.Is(err, ErrNoCapacity) {
		r.gate.release(r.ws)
		return launched{}, false, err
	}
	r.parked(err)
	return launched{}, true, err
}

var errRoomTimeout error = apperr.New("task.errors.room_wait_limit")

func (d *Dispatcher) awaitTurn(ctx context.Context, w *roomWaiter, deadline <-chan time.Time, backoff time.Duration) (bool, error) {
	var retry <-chan time.Time
	if d.room.head(w) {
		t := time.NewTimer(jitter(backoff, w.ws))
		defer t.Stop()
		retry = t.C
	}
	select {
	case <-ctx.Done():
		return false, errRoomDropped
	case <-deadline:
		return false, errRoomTimeout
	case <-w.wake:
		return true, nil
	case <-retry:
		return false, nil
	}
}

func lastErrOr(err error) error {
	if err != nil {
		return err
	}
	return apperr.Unavailable("task.errors.room_line")
}

// Deterministic per workspace id on purpose: it only has to differ between workspaces and keep each
// one's pace stable.
func jitter(d time.Duration, ws string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ws))
	frac := float64(h.Sum32()%401) / 1000
	return time.Duration(float64(d) * (0.8 + frac))
}

func (d *Dispatcher) maxQueueMinutes() int {
	if d.queueMinutes != nil {
		if n := d.queueMinutes(); n > 0 {
			return n
		}
	}
	return settings.DefaultMaxQueueMinutes
}

func (d *Dispatcher) WithMaxQueueMinutes(fn func() int) *Dispatcher {
	d.queueMinutes = fn
	return d
}

func (d *Dispatcher) RoomFreed(match func(workspaceID string) bool) { d.room.kickWhere(match) }

func (d *Dispatcher) RoomWaiting() int { return d.room.count() }
