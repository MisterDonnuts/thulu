package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type graceFixture struct {
	h     *Handler
	store *UserStore
	user  *User
	ws    string
	clock time.Time
}

func newGraceFixture(t *testing.T) *graceFixture {
	t.Helper()
	store := newSSOTestStore(t)
	f := &graceFixture{h: NewHandler(store), store: store, clock: time.Now()}
	f.h.grace.now = func() time.Time { return f.clock }
	f.user = ssoUser(t, store, "tabs@example.com", "Tabs")
	ws, err := store.CreateWorkspace(t.Context(), f.user.ID, "Tabs", "tabs")
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws.ID
	return f
}

func (f *graceFixture) login(t *testing.T) string {
	t.Helper()
	tok, err := f.h.issueTokenPair(t.Context(), f.user, f.ws, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return tok.RefreshToken
}

func (f *graceFixture) refresh(t *testing.T, refreshTok string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshTok})
	w := httptest.NewRecorder()
	f.h.handleRefresh(w, httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		return w.Code, ""
	}
	var out tokenResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return w.Code, out.RefreshToken
}

func (f *graceFixture) state(t *testing.T, refreshTok string) RefreshTokenState {
	t.Helper()
	rec, err := f.store.LookupRefreshToken(t.Context(), HashRefreshToken(refreshTok))
	if err != nil {
		t.Fatal(err)
	}
	return rec.State
}

func (f *graceFixture) tokenVersion(t *testing.T) int {
	t.Helper()
	v, err := f.store.GetTokenVersion(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRefreshGrace_SecondTabGetsTheSameSuccessor(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	version := f.tokenVersion(t)

	code, t1 := f.refresh(t, t0)
	if code != http.StatusOK {
		t.Fatalf("first refresh: %d", code)
	}
	f.clock = f.clock.Add(3 * time.Second)
	code, again := f.refresh(t, t0)
	if code != http.StatusOK {
		t.Fatalf("second tab inside the window: %d, want 200", code)
	}
	if again != t1 {
		t.Error("second tab got a different successor: the family forked")
	}
	if f.state(t, t1) != RefreshValid {
		t.Error("successor revoked by the second tab's refresh")
	}
	if f.tokenVersion(t) != version {
		t.Error("token version bumped: reuse detection fired")
	}
	if code, _ := f.refresh(t, t1); code != http.StatusOK {
		t.Errorf("rotating the successor: %d", code)
	}
}

func TestRefreshGrace_ExpiresAfterWindow(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	_, t1 := f.refresh(t, t0)

	f.clock = f.clock.Add(refreshGraceWindow + time.Second)
	if code, _ := f.refresh(t, t0); code != http.StatusUnauthorized {
		t.Fatalf("repeat after the window: %d, want 401", code)
	}
	if f.state(t, t1) != RefreshRevoked {
		t.Error("reuse after the window left the family alive")
	}
}

func TestRefreshGrace_OlderTokenIsStillReuse(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	version := f.tokenVersion(t)
	_, t1 := f.refresh(t, t0)
	_, t2 := f.refresh(t, t1)
	if t2 == "" {
		t.Fatal("second rotation failed")
	}

	if code, _ := f.refresh(t, t0); code != http.StatusUnauthorized {
		t.Fatalf("replayed T0: %d, want 401", code)
	}
	if f.state(t, t2) != RefreshRevoked {
		t.Error("replaying an older token left the family's live token valid")
	}
	if f.tokenVersion(t) == version {
		t.Error("replaying an older token did not bump the token version")
	}
}

func TestRefreshGrace_RevokedFamilyNotRevived(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	_, t1 := f.refresh(t, t0)
	fam, err := f.store.RefreshFamilyForHash(t.Context(), HashRefreshToken(t1))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeRefreshTokenFamily(t.Context(), fam); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.refresh(t, t0); code != http.StatusUnauthorized {
		t.Errorf("repeat after sign-out: %d, want 401", code)
	}
}

func TestRefreshGrace_ConcurrentRefreshesShareOneSuccessor(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	const n = 8
	got := make([]string, n)
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], got[i] = f.refresh(t, t0)
		}()
	}
	wg.Wait()
	for i := range n {
		if codes[i] != http.StatusOK {
			t.Fatalf("refresh %d: %d", i, codes[i])
		}
		if got[i] != got[0] {
			t.Fatalf("refresh %d got another successor", i)
		}
	}
	var live int
	if err := f.store.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, f.user.ID,
	).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Errorf("%d live refresh tokens after concurrent refreshes, want 1", live)
	}
}

func TestRotateRefreshToken_OnlyOnce(t *testing.T) {
	f := newGraceFixture(t)
	t0 := f.login(t)
	rec, err := f.store.LookupRefreshToken(t.Context(), HashRefreshToken(t0))
	if err != nil {
		t.Fatal(err)
	}
	rotate := func(newHash string) error {
		return f.store.RotateRefreshToken(t.Context(), HashRefreshToken(t0), newHash, f.user.ID, f.ws,
			rec.FamilyID, rec.FamilyStart, time.Now().Add(time.Hour), "", "")
	}
	if err := rotate("hash-a"); err != nil {
		t.Fatal(err)
	}
	if err := rotate("hash-b"); !errors.Is(err, ErrRefreshAlreadyRotated) {
		t.Errorf("second rotation: err = %v, want ErrRefreshAlreadyRotated", err)
	}
}
