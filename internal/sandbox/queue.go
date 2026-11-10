package sandbox

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

// After AcquireGranted the caller must eventually call Release(taskID).
type AcquireOutcome int

const (
	AcquireGranted AcquireOutcome = iota
	AcquireExisting
	AcquireQueued
)

type QueueAcquireResult struct {
	Outcome       AcquireOutcome
	QueuePosition int
}

type SlotGrantedCallback func(taskID, stage string)

// 30s covers a typical RelaunchStage round-trip without holding the slot hostage if the agent
// crashed.
const pendingGrantTTL = 30 * time.Second

// All scheduling decisions take mu; Docker creates and removes happen outside it so slow IO never
// blocks the scheduler.
type Queue struct {
	store    *Store
	settings *settings.Store

	mu            sync.Mutex
	waiters       map[string]chan struct{}
	pendingGrants map[string]time.Time
	onSlotGranted SlotGrantedCallback
	concurrency   ConcurrencyPolicy
}

type ConcurrencyPolicy func(ctx context.Context, taskID string) (workspaceID string, limit int)

func (q *Queue) SetConcurrencyPolicy(p ConcurrencyPolicy) {
	q.mu.Lock()
	q.concurrency = p
	q.mu.Unlock()
}

func (q *Queue) SetOnSlotGranted(cb SlotGrantedCallback) {
	q.mu.Lock()
	q.onSlotGranted = cb
	q.mu.Unlock()
}

func NewQueue(store *Store, settingsStore *settings.Store, onSlotGranted SlotGrantedCallback) *Queue {
	return &Queue{
		store:         store,
		settings:      settingsStore,
		waiters:       map[string]chan struct{}{},
		pendingGrants: map[string]time.Time{},
		onSlotGranted: onSlotGranted,
	}
}

func (q *Queue) StartGC(ctx context.Context) {
	go q.gcPendingGrants(ctx)
}

func (q *Queue) Acquire(ctx context.Context, taskID, stage string) (*QueueAcquireResult, error) {
	if taskID == "" {
		return nil, apperr.Invalid("sandbox.errors.task_required")
	}

	if sb, err := q.store.Get(ctx, taskID); err == nil && isLive(sb.Status) {
		return &QueueAcquireResult{Outcome: AcquireExisting}, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if granted, err := q.tryGrant(ctx, taskID, stage); err != nil {
		return nil, err
	} else if granted {
		return &QueueAcquireResult{Outcome: AcquireGranted}, nil
	}

	return q.enqueueAndWait(ctx, taskID, stage)
}

// Register the waiter before Enqueue so a Release in between wakes this call instead of firing
// OnSlotGranted.
func (q *Queue) enqueueAndWait(ctx context.Context, taskID, stage string) (*QueueAcquireResult, error) {
	blockMs, err := q.settings.SandboxAcquireBlockMs(ctx)
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.read_block_ms")
	}

	notify := q.registerWaiter(taskID)
	defer q.unregisterWaiter(taskID)

	prio, err := q.priorityFor(ctx, stage)
	if err != nil {
		return nil, err
	}
	if err := q.store.Enqueue(ctx, taskID, stage, prio); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.enqueue")
	}

	timer := time.NewTimer(time.Duration(blockMs) * time.Millisecond)
	defer timer.Stop()

	select {
	case <-notify:
		if granted, err := q.claimGrant(ctx, taskID, stage); err != nil {
			return nil, err
		} else if granted {
			return &QueueAcquireResult{Outcome: AcquireGranted}, nil
		}
		return q.queuedResult(ctx, taskID), nil
	case <-timer.C:
		return q.queuedResult(ctx, taskID), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *Queue) queuedResult(ctx context.Context, taskID string) *QueueAcquireResult {
	pos, err := q.store.Position(ctx, taskID)
	logx.Warn(err, "sandbox.log.queue_position", "task", taskID)
	return &QueueAcquireResult{Outcome: AcquireQueued, QueuePosition: pos}
}

func (q *Queue) Release(ctx context.Context, taskID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := q.store.MarkReleased(ctx, taskID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	next, err := q.nextEligibleLocked(ctx)
	if err != nil || next == nil {
		return err
	}
	q.grantToLocked(next)
	return nil
}

func (q *Queue) grantToLocked(next *QueueEntry) {
	q.pendingGrants[next.TaskID] = time.Now()
	if notifyCh, hasWaiter := q.waiters[next.TaskID]; hasWaiter {
		// Non-blocking send: never block under q.mu if the waiter already gave up.
		select {
		case notifyCh <- struct{}{}:
		default:
		}
		return
	}
	// The callback may call back into the queue, so it must run outside q.mu.
	if cb := q.onSlotGranted; cb != nil {
		go cb(next.TaskID, next.Stage)
	}
}

func (q *Queue) nextEligibleLocked(ctx context.Context) (*QueueEntry, error) {
	if q.concurrency == nil {
		return q.store.Next(ctx)
	}
	queued, err := q.store.ListQueue(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range queued {
		room, rErr := q.workspaceHasRoomLocked(ctx, e.TaskID)
		if rErr != nil {
			return nil, rErr
		}
		if room {
			return e, nil
		}
	}
	return nil, nil
}

func (q *Queue) Cancel(ctx context.Context, taskID string) error {
	q.mu.Lock()
	delete(q.pendingGrants, taskID)
	delete(q.waiters, taskID) // Don't close the channel: a pending receiver would read it as a grant.
	q.mu.Unlock()
	return q.store.Dequeue(ctx, taskID)
}

func (q *Queue) Remove(ctx context.Context, taskID string) error {
	held := q.PendingGrant(taskID)
	if err := q.Cancel(ctx, taskID); err != nil {
		return err
	}
	if held {
		q.maybeGrantNext(ctx)
	}
	return nil
}

func (q *Queue) PendingGrant(taskID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.pendingGrants[taskID]
	return ok
}

func (q *Queue) tryGrant(ctx context.Context, taskID, _ string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.pendingGrants[taskID]; ok {
		return q.commitGrantLocked(ctx, taskID)
	}

	wsRoom, err := q.workspaceHasRoomLocked(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !wsRoom {
		return false, nil
	}

	free, err := q.hasCapacityLocked(ctx)
	if err != nil {
		return false, err
	}
	if free {
		return q.commitGrantLocked(ctx, taskID)
	}

	idle, err := q.findEvictableLocked(ctx)
	if err != nil {
		return false, err
	}
	if idle != nil {
		// The queue only marks the row evicted; Manager.ReapEvicted removes the container.
		if err := q.store.MarkReleased(ctx, idle.TaskID); err != nil {
			return false, err
		}
		return q.commitGrantLocked(ctx, taskID)
	}

	return false, nil
}

func (q *Queue) claimGrant(ctx context.Context, taskID, _ string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pendingGrants[taskID]; !ok {
		return false, nil
	}
	return q.commitGrantLocked(ctx, taskID)
}

func (q *Queue) commitGrantLocked(ctx context.Context, taskID string) (bool, error) {
	delete(q.pendingGrants, taskID)
	if err := q.store.Dequeue(ctx, taskID); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	wsID := ""
	if q.concurrency != nil {
		wsID, _ = q.concurrency(ctx, taskID)
	}
	sb := &Sandbox{
		TaskID:         taskID,
		WorkspaceID:    wsID,
		ContainerID:    "",
		Image:          DefaultRuntimeImage,
		Status:         StatusStarting,
		HostPorts:      map[int]int{},
		CreatedAt:      now,
		LastActivityAt: now,
	}
	if err := q.store.Upsert(ctx, sb); err != nil {
		return false, err
	}
	return true, nil
}

func (q *Queue) workspaceHasRoomLocked(ctx context.Context, taskID string) (bool, error) {
	if q.concurrency == nil {
		return true, nil
	}
	wsID, limit := q.concurrency(ctx, taskID)
	if limit <= 0 || wsID == "" {
		return true, nil
	}
	live, err := q.store.CountLiveForWorkspace(ctx, wsID)
	if err != nil {
		return false, err
	}
	used := live
	for grantTask := range q.pendingGrants {
		if grantTask == taskID {
			continue
		}
		if gWS, _ := q.concurrency(ctx, grantTask); gWS == wsID {
			used++
		}
	}
	return used < limit, nil
}

func (q *Queue) hasCapacityLocked(ctx context.Context) (bool, error) {
	maxSandboxes, err := q.settings.SandboxMax(ctx)
	if err != nil {
		return false, err
	}
	live, err := q.store.CountLive(ctx)
	if err != nil {
		return false, err
	}
	used := live + len(q.pendingGrants)
	return used < maxSandboxes, nil
}

func (q *Queue) findEvictableLocked(ctx context.Context) (*Sandbox, error) {
	idleSecs, err := q.settings.SandboxIdleEvictSeconds(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(idleSecs) * time.Second)
	return q.store.IdleEvictionCandidate(ctx, cutoff)
}

func (q *Queue) Reprioritize(ctx context.Context) (int, error) {
	prios, err := q.settings.SandboxPriorities(ctx)
	if err != nil {
		return 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.store.Reprioritize(ctx, prios)
}

func (q *Queue) priorityFor(ctx context.Context, stage string) (int, error) {
	prios, err := q.settings.SandboxPriorities(ctx)
	if err != nil {
		return 0, err
	}
	return prios[stage], nil
}

func (q *Queue) registerWaiter(taskID string) chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch, ok := q.waiters[taskID]
	if !ok {
		ch = make(chan struct{}, 1)
		q.waiters[taskID] = ch
	}
	return ch
}

func (q *Queue) unregisterWaiter(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.waiters, taskID)
}

func (q *Queue) gcPendingGrants(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.expireOnce(ctx)
		}
	}
}

func (q *Queue) expireOnce(ctx context.Context) {
	q.mu.Lock()
	expired := []string{}
	for taskID, ts := range q.pendingGrants {
		if time.Since(ts) > pendingGrantTTL {
			expired = append(expired, taskID)
		}
	}
	for _, taskID := range expired {
		delete(q.pendingGrants, taskID)
	}
	q.mu.Unlock()

	if len(expired) == 0 {
		return
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.expired_grants", "count", len(expired), "tasks", expired))

	for range expired {
		q.maybeGrantNext(ctx)
	}
}

func (q *Queue) maybeGrantNext(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next, err := q.nextEligibleLocked(ctx)
	if err != nil || next == nil {
		return
	}
	q.grantToLocked(next)
}

func isLive(s Status) bool {
	return s != StatusEvicted && s != StatusFailed
}
