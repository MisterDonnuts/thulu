package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func tokenVersionFixture(t *testing.T, email string) (store *UserStore, token, userID string, reads *atomic.Int64) {
	t.Helper()
	store, err := NewUserStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	reads = &atomic.Int64{}
	inner := store.tokenVersions.load
	store.tokenVersions.load = func(ctx context.Context, id string) (int, error) {
		reads.Add(1)
		return inner(ctx, id)
	}

	user, err := store.CreateUser(t.Context(), email, "Cache User")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ws, err := store.CreateWorkspace(t.Context(), user.ID, "Cache ws", "cache-"+user.ID)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	token, err = IssueJWT(Claims{
		UserID: user.ID, Email: user.Email, WorkspaceID: ws.ID,
		Role: RoleOwner, TokenVersion: user.TokenVersion,
	})
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return store, token, user.ID, reads
}

func probeStatus(store *UserStore, token string) int {
	probe := JWTMiddlewareWithStore(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ClaimsFromContext(r.Context()) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	probe.ServeHTTP(w, r)
	return w.Code
}

func TestTokenVersion_RepeatRequestsCostOneQuery(t *testing.T) {
	store, token, _, reads := tokenVersionFixture(t, "cache-hit@example.com")

	for i := range 2 {
		if code := probeStatus(store, token); code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i+1, code)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("two authenticated requests should read token_version once, got %d reads", got)
	}
}

func TestTokenVersionCache_ConcurrentMissesShareOneRead(t *testing.T) {
	var reads atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	c := newTokenVersionCache(time.Minute, func(context.Context, string) (int, error) {
		reads.Add(1)
		entered <- struct{}{}
		<-release
		return 7, nil
	})

	var wg sync.WaitGroup
	call := func() {
		defer wg.Done()
		v, err := c.get(t.Context(), "u1")
		if err != nil || v != 7 {
			t.Errorf("get: got (%d, %v), want (7, nil)", v, err)
		}
	}

	wg.Add(1)
	go call()
	<-entered

	for range 8 {
		wg.Add(1)
		go call()
	}
	close(release)
	wg.Wait()

	if got := reads.Load(); got != 1 {
		t.Errorf("nine concurrent misses made %d reads, want 1", got)
	}
}

func TestTokenVersion_BumpIsVisibleBeforeTTL(t *testing.T) {
	store, token, userID, reads := tokenVersionFixture(t, "cache-bump@example.com")

	if code := probeStatus(store, token); code != http.StatusOK {
		t.Fatalf("pre-bump: want 200, got %d", code)
	}
	if err := store.BumpTokenVersion(t.Context(), userID); err != nil {
		t.Fatalf("BumpTokenVersion: %v", err)
	}

	if code := probeStatus(store, token); code != http.StatusUnauthorized {
		t.Fatalf("post-bump: want 401, got %d", code)
	}
	if got := reads.Load(); got != 2 {
		t.Errorf("want 2 reads (one per side of the bump), got %d", got)
	}
}

func TestTokenVersion_DeletedUserStaysRevoked(t *testing.T) {
	store, token, userID, _ := tokenVersionFixture(t, "cache-gone@example.com")

	for _, q := range []string{
		`DELETE FROM workspace_members WHERE user_id = $1`,
		`DELETE FROM workspaces WHERE owner_id = $1`,
		`DELETE FROM users WHERE id = $1`,
	} {
		if _, err := store.DB().Exec(q, userID); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	for i := range 2 {
		if code := probeStatus(store, token); code != http.StatusUnauthorized {
			t.Fatalf("request %d after user deletion: want 401, got %d", i+1, code)
		}
	}
}

func TestTokenVersion_TransientErrorDoesNotRevoke(t *testing.T) {
	store, token, _, reads := tokenVersionFixture(t, "cache-dberr@example.com")

	boom := errors.New("connection reset")
	store.tokenVersions.load = func(context.Context, string) (int, error) {
		reads.Add(1)
		return 0, boom
	}

	for i := range 2 {
		if code := probeStatus(store, token); code != http.StatusOK {
			t.Fatalf("request %d during DB failure: want 200, got %d", i+1, code)
		}
	}
	if got := reads.Load(); got != 2 {
		t.Errorf("a failed read must not be cached: want 2 reads, got %d", got)
	}
}
