package syncx

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTickUntil_RunsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		TickUntil(ctx, time.Millisecond, func(context.Context) { n.Add(1) })
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for n.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("ticker never fired 3 times (got %d)", n.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TickUntil did not return after cancel")
	}
}

func TestKeyedMutex_SameKeySameMutex(t *testing.T) {
	var km KeyedMutex
	if km.Get("a") != km.Get("a") {
		t.Error("same key must return the same mutex")
	}
	if km.Get("a") == km.Get("b") {
		t.Error("different keys must return different mutexes")
	}
}

func TestKeyedMutex_SerializesPerKey(t *testing.T) {
	var km KeyedMutex
	var wg sync.WaitGroup
	counter := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu := km.Get("k")
			mu.Lock()
			counter++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counter != 50 {
		t.Errorf("counter = %d, want 50", counter)
	}
}
