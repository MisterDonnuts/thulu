package sandbox

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

type execTracker struct {
	mu sync.Mutex
	n  map[string]int
}

func (t *execTracker) begin(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == nil {
		t.n = map[string]int{}
	}
	t.n[taskID]++
}

func (t *execTracker) end(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n[taskID] <= 1 {
		delete(t.n, taskID)
		return
	}
	t.n[taskID]--
}

func (t *execTracker) busy(taskID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n[taskID] > 0
}

func clampExecTimeout(requested int, capMs int64) int {
	if capMs <= 0 {
		capMs = settings.DefaultSandboxMaxExecTimeoutMs
	}
	if requested <= 0 || int64(requested) > capMs {
		return int(capMs)
	}
	return requested
}

func (m *Manager) maxExecTimeoutMs(ctx context.Context) int64 {
	if m.settings == nil {
		return settings.DefaultSandboxMaxExecTimeoutMs
	}
	return m.settings.SandboxMaxExecTimeoutMs(ctx)
}

func (m *Manager) ReapIdle(ctx context.Context) {
	if m.settings == nil {
		return
	}
	idleSecs, err := m.settings.SandboxIdleEvictSeconds(ctx)
	if err != nil || idleSecs <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(idleSecs) * time.Second)
	// Bounded so a store that refuses to update can't spin the loop forever.
	for range 1000 {
		sb, err := m.store.IdleEvictionCandidate(ctx, cutoff)
		if err != nil || sb == nil {
			return
		}
		evicted, err := m.evictIfIdle(ctx, sb, cutoff, idleSecs)
		if err != nil {
			return
		}
		if !evicted {
			if err := m.store.Touch(ctx, sb.TaskID); err != nil {
				return
			}
		}
	}
}

// Holds the task lock Acquire takes, so an exec can't pick up the container between the check and
// the removal;
// an exec marks itself busy before acquiring.
func (m *Manager) evictIfIdle(ctx context.Context, sb *Sandbox, cutoff time.Time, idleSecs int) (evicted bool, err error) {
	mu := m.taskLock(sb.TaskID)
	if !mu.TryLock() {
		return false, nil
	}
	defer mu.Unlock()
	if m.execs.busy(sb.TaskID) {
		return false, nil
	}
	cur, err := m.store.Get(ctx, sb.TaskID)
	if err != nil {
		return false, err
	}
	if cur.Status == StatusEvicted || cur.Status == StatusFailed || cur.ContainerID != sb.ContainerID || !cur.LastActivityAt.Before(cutoff) {
		return false, nil
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.idle_evicting", "task", sb.TaskID, "container", sb.ContainerID, "seconds", idleSecs))
	if sb.ContainerID != "" {
		logx.Warn(m.docker.Remove(ctx, sb.ContainerID), "sandbox.log.idle_remove", "container", sb.ContainerID)
	}
	if err := m.queue.Release(ctx, sb.TaskID); err != nil {
		logx.Warn(err, "sandbox.log.idle_release", "task", sb.TaskID)
		return false, err
	}
	return true, nil
}

// The in-pod exec agent kills the command at its limit and still has to report back.
const execRequestGrace = time.Minute

func execRequestTimeout(timeoutMs int) time.Duration {
	if timeoutMs <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(timeoutMs)*time.Millisecond + execRequestGrace
}

type deadSuspects struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (d *deadSuspects) strike(containerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids[containerID] {
		return true
	}
	if d.ids == nil {
		d.ids = map[string]bool{}
	}
	d.ids[containerID] = true
	return false
}

func (d *deadSuspects) keepOnly(found map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.ids {
		if !found[id] {
			delete(d.ids, id)
		}
	}
}

// Reaped only on the second sweep in a row that finds it dead, so one failed inspect does not end a
// live sandbox.
func (m *Manager) ReapDead(ctx context.Context) {
	live, err := m.store.ListLive(ctx)
	if err != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.reap_list_failed", "err", err))
		return
	}
	found := map[string]bool{}
	for _, sb := range live {
		if sb.Status == StatusStarting && sb.ContainerID == "" {
			key := stuckStartKey(sb.TaskID)
			found[key] = true
			if m.dead.strike(key) && m.reapStuckStart(ctx, sb.TaskID) {
				delete(found, key)
			}
			continue
		}
		if sb.Status != StatusReady || sb.ContainerID == "" || m.docker.Alive(ctx, sb.ContainerID) {
			continue
		}
		found[sb.ContainerID] = true
		if !m.dead.strike(sb.ContainerID) {
			continue
		}
		if m.reapDead(ctx, sb) {
			delete(found, sb.ContainerID)
		}
	}
	m.dead.keepOnly(found)
}

func stuckStartKey(taskID string) string { return "starting:" + taskID }

// The grant writes the row and the container is created right after, both under the task lock,
// so a starting row with no container whose lock is free has nobody coming to finish it.
func (m *Manager) reapStuckStart(ctx context.Context, taskID string) (reaped bool) {
	mu := m.taskLock(taskID)
	if !mu.TryLock() {
		return false
	}
	defer mu.Unlock()
	cur, err := m.store.Get(ctx, taskID)
	if err != nil || cur.Status != StatusStarting || cur.ContainerID != "" {
		return false
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.start_abandoned", "task", taskID))
	m.inits.drop(taskID)
	if err := m.queue.Release(ctx, taskID); err != nil {
		logx.Warn(err, "sandbox.log.release_slot", "task", taskID)
		return false
	}
	return true
}

func (m *Manager) reapDead(ctx context.Context, sb *Sandbox) (reaped bool) {
	mu := m.taskLock(sb.TaskID)
	if !mu.TryLock() {
		return false
	}
	defer mu.Unlock()
	if m.execs.busy(sb.TaskID) {
		return false
	}
	cur, err := m.store.Get(ctx, sb.TaskID)
	if err != nil || cur.Status != StatusReady || cur.ContainerID != sb.ContainerID {
		return false
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.container_gone", "task", sb.TaskID, "container", sb.ContainerID))
	m.inits.drop(sb.TaskID)
	logx.Warn(m.docker.Remove(ctx, sb.ContainerID), "sandbox.log.reap_container", "container", sb.ContainerID)
	if err := m.queue.Release(ctx, sb.TaskID); err != nil {
		logx.Warn(err, "sandbox.log.release_slot", "task", sb.TaskID)
		return false
	}
	return true
}
