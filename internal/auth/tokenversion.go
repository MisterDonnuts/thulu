package auth

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// 5s absorbs a page load's burst of authenticated requests (poll, SSE, clicks) in one read
// while staying below the lag a person would notice on a revocation.
const tokenVersionTTL = 5 * time.Second

// Another daemon sharing the DB honors a bumped version for up to tokenVersionTTL; refresh tokens
// are not cached.
// Never cache errors: a transient DB failure must not be remembered as a valid token.
type tokenVersionCache struct {
	ttl  time.Duration
	load func(ctx context.Context, userID string) (int, error)
	now  func() time.Time

	flight singleflight.Group

	mu      sync.Mutex
	entries map[string]tokenVersionEntry
	// gen advances on every invalidation so a read in flight during a bump cannot store its stale
	// value.
	gen uint64
}

type tokenVersionEntry struct {
	version int
	expires time.Time
}

func newTokenVersionCache(ttl time.Duration, load func(ctx context.Context, userID string) (int, error)) *tokenVersionCache {
	return &tokenVersionCache{
		ttl:     ttl,
		load:    load,
		now:     time.Now,
		entries: make(map[string]tokenVersionEntry),
	}
}

func (c *tokenVersionCache) get(ctx context.Context, userID string) (int, error) {
	if v, ok := c.cached(userID); ok {
		return v, nil
	}
	v, err, _ := c.flight.Do(userID, func() (any, error) {
		// A caller can reach Do just after the previous flight stored the value and left.
		if v, ok := c.cached(userID); ok {
			return v, nil
		}
		c.mu.Lock()
		gen := c.gen
		c.mu.Unlock()

		// The read is shared by every waiter on this user; one client hanging up must not fail the
		// others' auth.
		// The DSN's statement_timeout still bounds it.
		version, loadErr := c.load(context.WithoutCancel(ctx), userID)
		if loadErr != nil {
			return 0, loadErr
		}

		c.mu.Lock()
		if c.gen == gen {
			c.entries[userID] = tokenVersionEntry{version: version, expires: c.now().Add(c.ttl)}
		}
		c.mu.Unlock()
		return version, nil
	})
	if err != nil {
		// Must wrap with %w semantics so errTokenVersionUserGone stays distinguishable from a DB
		// hiccup.
		return 0, apperr.Wrap(err, "auth.errors.token_version_read")
	}
	version, _ := v.(int) //nolint:errcheck // type assertion ok-idiom; the flight only ever returns an int
	return version, nil
}

func (c *tokenVersionCache) cached(userID string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[userID]
	if !ok {
		return 0, false
	}
	if !c.now().Before(e.expires) {
		// Delete on expiry, or the map grows with every user the process has ever seen.
		delete(c.entries, userID)
		return 0, false
	}
	return e.version, true
}

// Every path that writes token_version must call this, or revocation is not instant on this daemon.
func (c *tokenVersionCache) invalidate(userID string) {
	c.mu.Lock()
	delete(c.entries, userID)
	c.gen++
	c.mu.Unlock()
}
