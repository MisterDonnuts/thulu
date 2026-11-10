package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestPerKeyRateLimit(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "")
	t.Setenv("OCTOPUS_RATE_LIMIT_PER_KEY", "0.001")
	t.Setenv("OCTOPUS_RATE_LIMIT_PER_KEY_BURST", "3")
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	grant := auth.KeyGrant{WorkspaceID: "ws-1", Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}}
	noisy, _, err := store.CreateAPIKey(t.Context(), "noisy", "u1", grant)
	if err != nil {
		t.Fatal(err)
	}
	quiet, _, err := store.CreateAPIKey(t.Context(), "quiet", "u1", grant)
	if err != nil {
		t.Fatal(err)
	}
	h := auth.MiddlewareWithStoreAndPublic(store, nil, okHandler())

	call := func(key, from string) int {
		r := httptest.NewRequest("GET", "/tasks", nil)
		r.RemoteAddr = from
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for i, from := range []string{"198.51.100.1:1", "198.51.100.2:1", "198.51.100.3:1"} {
		if code := call(noisy, from); code != http.StatusOK {
			t.Fatalf("call %d: %d, want 200 within the burst", i, code)
		}
	}
	if code := call(noisy, "198.51.100.4:1"); code != http.StatusTooManyRequests {
		t.Errorf("4th call from a fresh address: %d, want 429", code)
	}
	if code := call(quiet, "198.51.100.1:1"); code != http.StatusOK {
		t.Errorf("another key was throttled by the noisy one: %d", code)
	}
}

func TestPerKeyRateLimit_Disabled(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "")
	t.Setenv("OCTOPUS_RATE_LIMIT_PER_KEY", "0")
	t.Setenv("OCTOPUS_RATE_LIMIT_PER_KEY_BURST", "0")
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, _, err := store.CreateAPIKey(t.Context(), "k", "u1",
		auth.KeyGrant{WorkspaceID: "ws-1", Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}})
	if err != nil {
		t.Fatal(err)
	}
	h := auth.MiddlewareWithStoreAndPublic(store, nil, okHandler())
	for i := range 300 {
		r := httptest.NewRequest("GET", "/tasks", nil)
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d with the limiter off: %d", i, w.Code)
		}
	}
}
