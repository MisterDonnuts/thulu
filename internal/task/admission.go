package task

import (
	"context"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Caps are read live on every decision (0 or nil = unlimited). Freed slots go round-robin across
// waiting
// workspaces so one tenant's queue can't starve others; a workspace at its own cap is skipped.
type agentSlots struct {
	mu      sync.Mutex
	capFn   func() int
	wsCapFn func(workspaceID string) int

	inflight int
	byWS     map[string]int
	queues   map[string][]*slotWaiter
	ring     []string
	next     int
}

// granted is set under mu when a slot is reserved, so a waiter canceling at the same moment hands
// it back instead of leaking it.
type slotWaiter struct {
	ch      chan struct{}
	granted bool
}

func newAgentSlots(capFn func() int) *agentSlots {
	return &agentSlots{capFn: capFn, byWS: map[string]int{}, queues: map[string][]*slotWaiter{}}
}

func (a *agentSlots) setCap(fn func() int) {
	a.mu.Lock()
	a.capFn = fn
	a.mu.Unlock()
}

func (a *agentSlots) withWorkspaceCap(fn func(workspaceID string) int) *agentSlots {
	a.mu.Lock()
	a.wsCapFn = fn
	a.mu.Unlock()
	return a
}

func (a *agentSlots) cap() int {
	if a.capFn == nil {
		return 0
	}
	return a.capFn()
}

// On success the caller MUST pair it with exactly one release(workspaceID).
func (a *agentSlots) acquire(ctx context.Context, workspaceID string) error {
	return a.acquireOr(ctx, workspaceID, nil)
}

type slotsFull struct {
	Workspace bool
	Line      int
	Used, Cap int
}

func (f slotsFull) err() error {
	switch {
	case f.Line > 0:
		return apperr.Unavailable("task.errors.slots_line", "count", f.Line)
	case f.Workspace:
		return apperr.Unavailable("task.errors.slots_full_workspace", "used", f.Used, "cap", f.Cap)
	}
	return apperr.Unavailable("task.errors.slots_full_install", "used", f.Used, "cap", f.Cap)
}

func (a *agentSlots) acquireOr(ctx context.Context, workspaceID string, onWait func(slotsFull)) error {
	a.mu.Lock()
	caps := a.capCache()
	// Pump existing waiters first: a raised cap or an unpumped freed slot is theirs before it is
	// ours.
	a.pumpLocked(caps)
	if a.globalRoomLocked(caps) && a.wsRoomLocked(caps, workspaceID) && len(a.queues[workspaceID]) == 0 {
		a.admitLocked(workspaceID)
		a.mu.Unlock()
		return nil
	}
	full := a.fullLocked(caps, workspaceID)
	w := &slotWaiter{ch: make(chan struct{})}
	if len(a.queues[workspaceID]) == 0 {
		a.ring = append(a.ring, workspaceID)
	}
	a.queues[workspaceID] = append(a.queues[workspaceID], w)
	a.mu.Unlock()
	if onWait != nil {
		onWait(full)
	}

	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		a.mu.Lock()
		defer a.mu.Unlock()
		if w.granted {
			a.releaseLocked(workspaceID)
			return ctx.Err()
		}
		a.dropWaiterLocked(workspaceID, w)
		return ctx.Err()
	}
}

func (a *agentSlots) release(workspaceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.releaseLocked(workspaceID)
}

func (a *agentSlots) releaseLocked(workspaceID string) {
	if a.inflight > 0 {
		a.inflight--
	}
	if n := a.byWS[workspaceID]; n > 1 {
		a.byWS[workspaceID] = n - 1
	} else {
		delete(a.byWS, workspaceID)
	}
	a.pumpLocked(a.capCache())
}

func (a *agentSlots) admitLocked(workspaceID string) {
	a.inflight++
	a.byWS[workspaceID]++
}

type capReader struct {
	global int
	ws     map[string]int
	fn     func(string) int
}

func (a *agentSlots) capCache() *capReader {
	return &capReader{global: a.cap(), ws: map[string]int{}, fn: a.wsCapFn}
}

func (c *capReader) workspace(id string) int {
	if c.fn == nil {
		return 0
	}
	if v, ok := c.ws[id]; ok {
		return v
	}
	v := c.fn(id)
	c.ws[id] = v
	return v
}

func (a *agentSlots) globalRoomLocked(c *capReader) bool {
	return c.global <= 0 || a.inflight < c.global
}

func (a *agentSlots) fullLocked(c *capReader, workspaceID string) slotsFull {
	switch {
	case !a.globalRoomLocked(c):
		return slotsFull{Used: a.inflight, Cap: c.global}
	case !a.wsRoomLocked(c, workspaceID):
		return slotsFull{Workspace: true, Used: a.byWS[workspaceID], Cap: c.workspace(workspaceID)}
	}
	return slotsFull{Line: len(a.queues[workspaceID])}
}

func (a *agentSlots) wsRoomLocked(c *capReader, workspaceID string) bool {
	limit := c.workspace(workspaceID)
	return limit <= 0 || a.byWS[workspaceID] < limit
}

func (a *agentSlots) pumpLocked(c *capReader) {
	for len(a.ring) > 0 && a.globalRoomLocked(c) {
		granted := false
		for i := range a.ring {
			idx := (a.next + i) % len(a.ring)
			ws := a.ring[idx]
			if !a.wsRoomLocked(c, ws) {
				continue
			}
			q := a.queues[ws]
			w := q[0]
			a.queues[ws] = q[1:]
			a.admitLocked(ws)
			w.granted = true
			close(w.ch)
			if len(a.queues[ws]) == 0 {
				// The workspace after it slides into idx, so idx is already the next one.
				a.removeFromRingLocked(idx)
				if len(a.ring) > 0 {
					a.next = idx % len(a.ring)
				}
			} else {
				a.next = (idx + 1) % len(a.ring)
			}
			granted = true
			break
		}
		if !granted {
			return
		}
	}
}

func (a *agentSlots) removeFromRingLocked(idx int) {
	delete(a.queues, a.ring[idx])
	a.ring = append(a.ring[:idx], a.ring[idx+1:]...)
	if idx < a.next {
		a.next--
	}
	if len(a.ring) == 0 {
		a.next = 0
		return
	}
	a.next %= len(a.ring)
}

func (a *agentSlots) dropWaiterLocked(workspaceID string, w *slotWaiter) {
	q := a.queues[workspaceID]
	for i, x := range q {
		if x != w {
			continue
		}
		a.queues[workspaceID] = append(q[:i], q[i+1:]...)
		if len(a.queues[workspaceID]) == 0 {
			for idx, ws := range a.ring {
				if ws == workspaceID {
					a.removeFromRingLocked(idx)
					break
				}
			}
		}
		return
	}
}
