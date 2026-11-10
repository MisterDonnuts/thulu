package syncx

import (
	"sync"
	"testing"
	"time"
)

func TestBus_FansOutToEverySubscriber(t *testing.T) {
	b := NewBus[string]()
	a, cancelA := b.Subscribe()
	c, cancelC := b.Subscribe()
	defer cancelA()
	defer cancelC()

	b.Publish("hello")
	for i, ch := range []<-chan string{a, c} {
		select {
		case got := <-ch:
			if got != "hello" {
				t.Errorf("subscriber %d got %q", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

func TestBus_CancelStopsDelivery(t *testing.T) {
	b := NewBus[int]()
	ch, cancel := b.Subscribe()
	cancel()
	cancel()

	b.Publish(1)
	select {
	case v := <-ch:
		t.Fatalf("canceled subscriber still received %v", v)
	case <-time.After(50 * time.Millisecond):
	}
	if n := b.Subscribers(); n != 0 {
		t.Errorf("Subscribers() = %d after cancel, want 0", n)
	}
}

func TestBus_PublishNeverBlocksOnASlowSubscriber(t *testing.T) {
	b := NewBus[int]()
	_, cancel := b.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < busBuffer*4; i++ {
			b.Publish(i)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that stopped reading")
	}
}

func TestBus_NilIsANoOp(t *testing.T) {
	var b *Bus[int]
	b.Publish(1)
}

func TestBus_ConcurrentSubscribeAndPublish(t *testing.T) {
	b := NewBus[int]()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ch, cancel := b.Subscribe()
				b.Publish(j)
				select {
				case <-ch:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	if n := b.Subscribers(); n != 0 {
		t.Errorf("Subscribers() = %d, want 0", n)
	}
}
