package syncx

import "sync"

// Publish never blocks and drops events for full subscribers; consumers must treat events as
// notifications and
// resync over REST after a gap. In-process only: other replicas never see these events.
type Bus[T any] struct {
	mu   sync.RWMutex
	subs map[int]chan T
	next int
}

func NewBus[T any]() *Bus[T] {
	return &Bus[T]{subs: make(map[int]chan T)}
}

func (b *Bus[T]) Publish(ev T) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *Bus[T]) Subscribe() (<-chan T, func()) {
	ch := make(chan T, busBuffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
		})
	}
}

func (b *Bus[T]) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

const busBuffer = 64
