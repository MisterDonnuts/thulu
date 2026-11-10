package auth

import (
	"context"
	"hash/fnv"
	"sync"
	"time"
)

// Tabs share the refresh cookie and may post the same token at once. Inside this window the repeat
// gets the same successor instead of revoking the family.
const refreshGraceWindow = 10 * time.Second

// Two hashes on one stripe only wait for each other; they never share a result.
const refreshLockStripes = 64

type graceEntry struct {
	tokens  tokenResponse
	newHash string
	until   time.Time
}

// In-memory only: with several replicas, a repeat that lands on another replica gets a 401
// (ErrRefreshAlreadyRotated), never a second successor.
type refreshGrace struct {
	mu      sync.Mutex
	entries map[string]graceEntry
	stripes [refreshLockStripes]sync.Mutex
	now     func() time.Time
}

func newRefreshGrace() *refreshGrace {
	return &refreshGrace{entries: map[string]graceEntry{}, now: time.Now}
}

// Serializes refreshes of one token hash, so exactly one of two simultaneous posts rotates it.
func (g *refreshGrace) lock(hash string) func() {
	if g == nil {
		return func() {}
	}
	f := fnv.New32a()
	_, _ = f.Write([]byte(hash))
	m := &g.stripes[f.Sum32()%refreshLockStripes]
	m.Lock()
	return m.Unlock
}

func (g *refreshGrace) remember(oldHash string, tokens *tokenResponse) {
	if g == nil || tokens == nil {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, e := range g.entries {
		if !now.Before(e.until) {
			delete(g.entries, k)
		}
	}
	g.entries[oldHash] = graceEntry{
		tokens:  *tokens,
		newHash: HashRefreshToken(tokens.RefreshToken),
		until:   now.Add(refreshGraceWindow),
	}
}

func (g *refreshGrace) successor(ctx context.Context, store *UserStore, oldHash string) *tokenResponse {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	e, ok := g.entries[oldHash]
	g.mu.Unlock()
	if !ok || !g.now().Before(e.until) {
		return nil
	}
	rec, err := store.LookupRefreshToken(ctx, e.newHash)
	if err != nil || rec.State != RefreshValid {
		return nil
	}
	tokens := e.tokens
	return &tokens
}
