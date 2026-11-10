package syncx

import (
	"context"
	"sync"
	"time"
)

func TickUntil(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// Mutexes are never removed: keys must stay low-cardinality (task ids, chat ids).
type KeyedMutex struct {
	mus sync.Map
}

func (k *KeyedMutex) Get(key string) *sync.Mutex {
	mu, _ := k.mus.LoadOrStore(key, &sync.Mutex{})
	lock, _ := mu.(*sync.Mutex) //nolint:errcheck // the map only ever stores *sync.Mutex
	if lock == nil {
		return &sync.Mutex{}
	}
	return lock
}
