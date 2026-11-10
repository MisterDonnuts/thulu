package chatruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestChatContainerGetsResourceCaps(t *testing.T) {
	r := newRig(t, 5)
	ctx := context.Background()
	if err := r.settings.PutJSON(ctx, settings.ChatMemoryBytesKey, int64(1<<30)); err != nil {
		t.Fatal(err)
	}
	if err := r.settings.PutJSON(ctx, settings.ChatNanoCPUsKey, int64(1_500_000_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mgr.Acquire(ctx, testChat("chat-caps"), testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	caps := r.last.Resources
	r.mu.Unlock()
	if caps.MemoryBytes != 1<<30 || caps.NanoCPUs != 1_500_000_000 || caps.PidsLimit != 1024 {
		t.Fatalf("chat launched with caps %+v", caps)
	}
}

func wsChat(id, ws string) *chat.Chat {
	return &chat.Chat{ID: id, WorkspaceID: ws, AgentType: "manager"}
}

func TestCapacity_NeverEvictsAnotherWorkspace(t *testing.T) {
	r := newRig(t, 2)
	ctx := context.Background()
	victimA, victimB := wsChat("chat-va", "ws-victim"), wsChat("chat-vb", "ws-victim")
	for _, c := range []*chat.Chat{victimA, victimB} {
		if _, err := r.mgr.Acquire(ctx, c, testAgent(), AcquireOpts{}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := r.mgr.Acquire(ctx, wsChat("chat-noisy", "ws-noisy"), testAgent(), AcquireOpts{})
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("expected ErrNoCapacity, got %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "chat slot") {
		t.Errorf("refusal is not readable: %q", msg)
	}
	for _, c := range []*chat.Chat{victimA, victimB} {
		if rt, _ := r.store.Get(c.ID); rt.Status == StatusEvicted {
			t.Errorf("%s of another workspace was evicted", c.ID)
		}
	}
}

func TestCapacity_EvictsOwnOldestIdleSkippingInFlight(t *testing.T) {
	r := newRig(t, 3)
	ctx := context.Background()
	other := wsChat("chat-other", "ws-other")
	busy, idle := wsChat("chat-busy", "ws-me"), wsChat("chat-idle", "ws-me")
	for _, c := range []*chat.Chat{other, busy, idle} {
		if _, err := r.mgr.Acquire(ctx, c, testAgent(), AcquireOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	rt, _ := r.store.Get(busy.ID)
	rt.LastActivityAt = time.Now().Add(-time.Minute)
	if err := r.store.Upsert(rt); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn(busy.ID); err != nil {
		t.Fatal(err)
	}
	defer r.mgr.EndTurn(busy.ID)

	if _, err := r.mgr.Acquire(ctx, wsChat("chat-new", "ws-me"), testAgent(), AcquireOpts{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt, _ := r.store.Get(idle.ID); rt.Status != StatusEvicted {
		t.Errorf("own idle chat should have made room, status=%s", rt.Status)
	}
	if rt, _ := r.store.Get(busy.ID); rt.Status == StatusEvicted {
		t.Error("a chat mid-turn was evicted")
	}
	if rt, _ := r.store.Get(other.ID); rt.Status == StatusEvicted {
		t.Error("another workspace's chat was evicted")
	}
}

func TestCapacity_ReapsAbandonedChatsFirst(t *testing.T) {
	r := newRig(t, 1)
	ctx := context.Background()
	stale := wsChat("chat-stale", "ws-gone")
	if _, err := r.mgr.Acquire(ctx, stale, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	rt, _ := r.store.Get(stale.ID)
	rt.LastActivityAt = time.Now().Add(-2 * time.Hour)
	if err := r.store.Upsert(rt); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mgr.Acquire(ctx, wsChat("chat-fresh", "ws-here"), testAgent(), AcquireOpts{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt, _ := r.store.Get(stale.ID); rt.Status != StatusEvicted {
		t.Errorf("abandoned chat not reaped, status=%s", rt.Status)
	}
}

func TestWorkspaceLimit_RefusalNamesTheLimit(t *testing.T) {
	r := newRig(t, 10)
	r.mgr.workspaceLimit = func(string) int { return 1 }
	ctx := context.Background()
	a := testChat("chat-lim-a")
	if _, err := r.mgr.Acquire(ctx, a, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn(a.ID); err != nil {
		t.Fatal(err)
	}
	defer r.mgr.EndTurn(a.ID)
	_, err := r.mgr.Acquire(ctx, testChat("chat-lim-b"), testAgent(), AcquireOpts{})
	if err == nil || !strings.Contains(err.Error(), "1 open chat,") {
		t.Fatalf("refusal = %v", err)
	}
}

func TestCapacity_ConcurrentBurstStaysWithinCaps(t *testing.T) {
	r := newRig(t, 3)
	var launched, starting, peak atomic.Int32
	r.mgr = newManagerForTest(r.store, r.settings, r.docker, ManagerConfig{
		DataDir: r.dataDir,
		Launch: func(_ context.Context, _ types.AgentType, _ types.LaunchParams) (string, error) {
			n := launched.Add(1)
			now := starting.Add(1)
			for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
			}
			time.Sleep(50 * time.Millisecond)
			starting.Add(-1)
			id := fmt.Sprintf("ctr-burst-%d", n)
			r.docker.setRunning(id, true)
			return id, nil
		},
	})
	r.mgr.workspaceLimit = func(ws string) int {
		if ws == "ws-noisy" {
			return 2
		}
		return 0
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := wsChat(fmt.Sprintf("chat-burst-%d", i), "ws-noisy")
			if _, err := r.mgr.Acquire(ctx, c, testAgent(), AcquireOpts{}); err == nil {
				_ = r.mgr.BeginTurn(c.ID)
			}
		}()
	}
	wg.Wait()
	n, err := r.store.CountLiveForWorkspace("ws-noisy")
	if err != nil {
		t.Fatal(err)
	}
	if n > 2 {
		t.Fatalf("workspace holds %d live chats, its cap is 2", n)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("%d containers were starting at once for a workspace capped at 2", got)
	}
}
