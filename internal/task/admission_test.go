package task

import (
	"context"
	"sync"
	"testing"
	"time"
)

func (a *agentSlots) stats() (inflight, waiting int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range a.queues {
		waiting += len(q)
	}
	return a.inflight, waiting
}

func acquireAsync(s *agentSlots, ctx context.Context, ws string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.acquire(ctx, ws) }()
	return done
}

func assertBlocked(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("acquire completed (err=%v) but should have blocked", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func assertAdmitted(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire did not complete in time")
	}
}

func waitWaiting(t *testing.T, s *agentSlots, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, w := s.stats(); w == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, w := s.stats()
	t.Fatalf("waiting=%d, want %d", w, n)
}

func TestAgentSlotsUnlimited(t *testing.T) {
	s := newAgentSlots(nil)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := s.acquire(ctx, "ws-a"); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	inflight, waiting := s.stats()
	if inflight != 50 || waiting != 0 {
		t.Fatalf("inflight=%d waiting=%d, want 50/0", inflight, waiting)
	}
}

func TestAgentSlotsZeroCapIsUnlimited(t *testing.T) {
	s := newAgentSlots(func() int { return 0 })
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := s.acquire(ctx, ""); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
}

func TestAgentSlotsBlocksAtCap(t *testing.T) {
	s := newAgentSlots(func() int { return 2 })
	ctx := context.Background()
	if err := s.acquire(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.acquire(ctx, "ws-b"); err != nil {
		t.Fatal(err)
	}
	done := acquireAsync(s, ctx, "ws-c")
	assertBlocked(t, done)
	if inflight, waiting := s.stats(); inflight != 2 || waiting != 1 {
		t.Fatalf("inflight=%d waiting=%d, want 2/1", inflight, waiting)
	}
	s.release("ws-a")
	assertAdmitted(t, done)
	if inflight, waiting := s.stats(); inflight != 2 || waiting != 0 {
		t.Fatalf("after release inflight=%d waiting=%d, want 2/0", inflight, waiting)
	}
}

func TestAgentSlotsFIFOWithinWorkspace(t *testing.T) {
	s := newAgentSlots(func() int { return 1 })
	ctx := context.Background()
	if err := s.acquire(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.acquire(ctx, "ws-a")
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}()
		waitWaiting(t, s, i+1)
	}
	for i := 0; i < 3; i++ {
		s.release("ws-a")
		for {
			mu.Lock()
			n := len(order)
			mu.Unlock()
			if n == i+1 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	wg.Wait()
	if len(order) != 3 || order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Fatalf("admission order = %v, want [0 1 2]", order)
	}
}

func TestAgentSlotsContextCancel(t *testing.T) {
	s := newAgentSlots(func() int { return 1 })
	if err := s.acquire(context.Background(), "ws-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := acquireAsync(s, ctx, "ws-a")
	assertBlocked(t, done)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled acquire returned nil, want ctx error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled acquire did not return")
	}
	if _, waiting := s.stats(); waiting != 0 {
		t.Fatalf("waiting=%d after cancel, want 0", waiting)
	}
	if len(s.ring) != 0 {
		t.Fatalf("ring=%v after cancel, want empty", s.ring)
	}
	s.release("ws-a")
	if err := s.acquire(context.Background(), "ws-a"); err != nil {
		t.Fatalf("post-cancel acquire failed: %v", err)
	}
}

func TestAgentSlotsDynamicCapRaise(t *testing.T) {
	limit := 1
	var capMu sync.Mutex
	s := newAgentSlots(func() int { capMu.Lock(); defer capMu.Unlock(); return limit })
	ctx := context.Background()
	if err := s.acquire(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	done := acquireAsync(s, ctx, "ws-b")
	assertBlocked(t, done)
	capMu.Lock()
	limit = 5
	capMu.Unlock()
	if err := s.acquire(ctx, "ws-c"); err != nil {
		t.Fatal(err)
	}
	assertAdmitted(t, done)
}

func TestAgentSlotsWorkspaceCap(t *testing.T) {
	s := newAgentSlots(func() int { return 10 }).withWorkspaceCap(func(ws string) int {
		if ws == "free" {
			return 1
		}
		return 0
	})
	ctx := context.Background()
	if err := s.acquire(ctx, "free"); err != nil {
		t.Fatal(err)
	}
	second := acquireAsync(s, ctx, "free")
	assertBlocked(t, second)
	if err := s.acquire(ctx, "team"); err != nil {
		t.Fatalf("other workspace blocked behind a capped one: %v", err)
	}
	s.release("team")
	assertBlocked(t, second)
	s.release("free")
	assertAdmitted(t, second)
}

func TestAgentSlotsRoundRobinPreventsStarvation(t *testing.T) {
	s := newAgentSlots(func() int { return 1 })
	ctx := context.Background()
	if err := s.acquire(ctx, "noisy"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	launch := func(ws string) {
		go func() {
			if err := s.acquire(ctx, ws); err != nil {
				return
			}
			mu.Lock()
			order = append(order, ws)
			mu.Unlock()
		}()
	}
	const noisy = 200
	for i := 0; i < noisy; i++ {
		launch("noisy")
	}
	waitWaiting(t, s, noisy)
	launch("quiet")
	waitWaiting(t, s, noisy+1)

	granted := 0
	holder := "noisy"
	for granted < 3 {
		s.release(holder)
		for {
			mu.Lock()
			n := len(order)
			mu.Unlock()
			if n == granted+1 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		holder = order[granted]
		mu.Unlock()
		granted++
	}
	mu.Lock()
	defer mu.Unlock()
	quietAt := -1
	for i, ws := range order {
		if ws == "quiet" {
			quietAt = i
			break
		}
	}
	if quietAt < 0 || quietAt > 1 {
		t.Fatalf("quiet tenant admitted at position %d of %v, want within the first two grants", quietAt, order)
	}
}

func TestAgentSlotsRoundRobinAlternates(t *testing.T) {
	s := newAgentSlots(func() int { return 1 })
	if err := s.acquire(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	var ws []string
	park := func(w string) *slotWaiter {
		s.mu.Lock()
		defer s.mu.Unlock()
		sw := &slotWaiter{ch: make(chan struct{})}
		if len(s.queues[w]) == 0 {
			s.ring = append(s.ring, w)
		}
		s.queues[w] = append(s.queues[w], sw)
		return sw
	}
	type parked struct {
		ws string
		w  *slotWaiter
	}
	all := []parked{{"a", park("a")}, {"a", park("a")}, {"a", park("a")}, {"b", park("b")}, {"b", park("b")}}
	seen := map[*slotWaiter]bool{}
	holder := "x"
	for range all {
		s.release(holder)
		s.mu.Lock()
		for _, p := range all {
			if p.w.granted && !seen[p.w] {
				seen[p.w] = true
				ws = append(ws, p.ws)
				holder = p.ws
			}
		}
		s.mu.Unlock()
	}
	want := []string{"a", "b", "a", "b", "a"}
	if len(ws) != len(want) {
		t.Fatalf("grant order %v, want %v", ws, want)
	}
	for i := range want {
		if ws[i] != want[i] {
			t.Fatalf("grant order %v, want %v", ws, want)
		}
	}
}

func TestAgentSlotsGrantedThenCanceledPassesSlotOn(t *testing.T) {
	s := newAgentSlots(func() int { return 1 })
	ctx := context.Background()
	if err := s.acquire(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	gone := &slotWaiter{ch: make(chan struct{})}
	s.ring = append(s.ring, "b")
	s.queues["b"] = []*slotWaiter{gone}
	s.mu.Unlock()
	next := acquireAsync(s, ctx, "c")
	waitWaiting(t, s, 2)

	s.release("a")
	s.mu.Lock()
	if !gone.granted {
		s.mu.Unlock()
		t.Fatal("b's waiter was not granted")
	}
	s.releaseLocked("b")
	s.mu.Unlock()
	assertAdmitted(t, next)
	if inflight, waiting := s.stats(); inflight != 1 || waiting != 0 {
		t.Fatalf("inflight=%d waiting=%d, want 1/0", inflight, waiting)
	}
}
