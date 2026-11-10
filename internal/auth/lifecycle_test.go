package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func provisionAndTokens(t *testing.T, h *auth.Handler, email string) (access, refresh string) {
	t.Helper()
	body := mustProvision(t, h, email, "User")
	access, _ = body["access_token"].(string)
	refresh, _ = body["refresh_token"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("provisioning did not return tokens: %v", body)
	}
	return access, refresh
}

func postJSONRefreshCookie(t *testing.T, mux http.Handler, path, refresh string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("{}")))
	r.Header.Set("Content-Type", "application/json")
	if refresh != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieRefresh, Value: refresh})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func rotatedRefreshCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieRefresh {
			return c.Value
		}
	}
	return ""
}

func TestRefresh_CookieCallerGetsNoTokensInBody(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	_, refresh := provisionAndTokens(t, h, "cookiebody@example.com")

	w := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh)
	if w.Code != http.StatusOK {
		t.Fatalf("cookie refresh: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["access_token"]; ok {
		t.Error("cookie refresh returned access_token in the body")
	}
	if _, ok := body["refresh_token"]; ok {
		t.Error("cookie refresh returned refresh_token in the body")
	}
	if body["expires_at"] == nil {
		t.Error("cookie refresh must still say when the session expires")
	}
	next := rotatedRefreshCookie(t, w)
	if next == "" {
		t.Fatal("cookie refresh set no refresh cookie")
	}

	w = postJSON(t, mux, "/auth/refresh", map[string]string{"refresh_token": next})
	if w.Code != http.StatusOK {
		t.Fatalf("body refresh: %d", w.Code)
	}
	body = map[string]any{}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if s, _ := body["refresh_token"].(string); s == "" {
		t.Error("body (CLI) refresh lost its tokens")
	}
}

func TestRevokeAll_CookieSessionGetsNoTokensInBody(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	handler := auth.JWTMiddlewareWithStore(store, mux)

	send := func(viaCookie bool) map[string]any {
		who := map[bool]string{true: "cookie", false: "header"}[viaCookie]
		access, _ := mustProvision(t, h, "revokeall-"+who+"@example.com", "Revoke "+who)["access_token"].(string)
		r := httptest.NewRequest(http.MethodPost, "/auth/sessions/revoke-all", nil)
		if viaCookie {
			r.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: access})
		} else {
			r.Header.Set("Authorization", "Bearer "+access)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("revoke-all (cookie=%v): %d %s", viaCookie, w.Code, w.Body.String())
		}
		var m map[string]any
		_ = json.NewDecoder(w.Body).Decode(&m)
		return m
	}
	if m := send(true); m["access_token"] != nil || m["refresh_token"] != nil {
		t.Errorf("cookie session got tokens in the body: %v", m)
	}
	if m := send(false); m["access_token"] == nil {
		t.Errorf("header (CLI) caller lost its re-issued tokens: %v", m)
	}
}

func TestTokenVersionBump_InvalidatesAccessToken(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	access, _ := provisionAndTokens(t, h, "tv@example.com")
	user, _ := store.GetUserByEmail(t.Context(), "tv@example.com")

	probe := auth.JWTMiddlewareWithStore(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.ClaimsFromContext(r.Context()) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	if w := authedGet(t, probe, "/auth/me", access); w.Code != http.StatusOK {
		t.Fatalf("pre-bump: want 200, got %d", w.Code)
	}

	if err := store.BumpTokenVersion(t.Context(), user.ID); err != nil {
		t.Fatalf("BumpTokenVersion: %v", err)
	}
	if w := authedGet(t, probe, "/auth/me", access); w.Code != http.StatusUnauthorized {
		t.Fatalf("post-bump: want 401, got %d", w.Code)
	}
}

func TestStorelessMiddleware_SkipsVersionCheck(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	access, _ := provisionAndTokens(t, h, "noversion@example.com")
	user, _ := store.GetUserByEmail(t.Context(), "noversion@example.com")
	if err := store.BumpTokenVersion(t.Context(), user.ID); err != nil {
		t.Fatalf("BumpTokenVersion: %v", err)
	}

	probe := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.ClaimsFromContext(r.Context()) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	if w := authedGet(t, probe, "/auth/me", access); w.Code != http.StatusOK {
		t.Fatalf("storeless middleware should ignore token_version: got %d", w.Code)
	}
}

func TestRefreshRotation_KeepsSessionWorking(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	_, refresh := provisionAndTokens(t, h, "rot@example.com")

	w := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	newRefresh := rotatedRefreshCookie(t, w)
	if newRefresh == "" || newRefresh == refresh {
		t.Fatalf("expected a rotated refresh token, got %q", newRefresh)
	}

	if w2 := postJSONRefreshCookie(t, mux, "/auth/refresh", newRefresh); w2.Code != http.StatusOK {
		t.Fatalf("refresh with rotated token: want 200, got %d", w2.Code)
	}
}

func TestRefreshReuse_RevokesFamily(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	_, refresh := provisionAndTokens(t, h, "reuse@example.com")

	// Rotate twice: the refresh grace window forgives a replay of the last-rotated token, so only
	// an older one counts as reuse.
	w := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh)
	if w.Code != http.StatusOK {
		t.Fatalf("first refresh: want 200, got %d", w.Code)
	}
	w = postJSONRefreshCookie(t, mux, "/auth/refresh", rotatedRefreshCookie(t, w))
	if w.Code != http.StatusOK {
		t.Fatalf("second refresh: want 200, got %d", w.Code)
	}
	newRefresh := rotatedRefreshCookie(t, w)

	if w2 := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh); w2.Code != http.StatusUnauthorized {
		t.Fatalf("replay of rotated token: want 401, got %d", w2.Code)
	}

	if w3 := postJSONRefreshCookie(t, mux, "/auth/refresh", newRefresh); w3.Code != http.StatusUnauthorized {
		t.Fatalf("post-reuse, latest token should be revoked: want 401, got %d", w3.Code)
	}
}

func TestRefresh_AbsoluteLifetimeCap(t *testing.T) {
	t.Setenv("OCTOPUS_SESSION_MAX_LIFETIME", "1ns")
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	_, refresh := provisionAndTokens(t, h, "cap@example.com")
	time.Sleep(2 * time.Millisecond)

	if w := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh); w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh past absolute lifetime: want 401, got %d body=%s", w.Code, w.Body.String())
	}
	_ = store
}

func TestSessionListAndRevoke(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	handler := auth.JWTMiddlewareWithStore(store, mux)

	access, refresh := provisionAndTokens(t, h, "sess@example.com")

	r := httptest.NewRequest(http.MethodGet, "/auth/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	r.AddCookie(&http.Cookie{Name: auth.CookieRefresh, Value: refresh})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list sessions: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var sessions []auth.SessionFamily
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	if !sessions[0].IsCurrent {
		t.Errorf("expected the listed session to be marked current")
	}
	fam := sessions[0].FamilyID

	dr := httptest.NewRequest(http.MethodDelete, "/auth/sessions/"+fam, nil)
	dr.Header.Set("Authorization", "Bearer "+access)
	dw := httptest.NewRecorder()
	handler.ServeHTTP(dw, dr)
	if dw.Code != http.StatusOK {
		t.Fatalf("revoke session: want 200, got %d body=%s", dw.Code, dw.Body.String())
	}

	if rw := postJSONRefreshCookie(t, mux, "/auth/refresh", refresh); rw.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after revoke: want 401, got %d", rw.Code)
	}
}

func TestRevokeSession_OtherUsersFamily_NotFound(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	handler := auth.JWTMiddlewareWithStore(store, mux)

	accessA, _ := provisionAndTokens(t, h, "alice@example.com")

	bob, err := store.CreateUser(t.Context(), "bob@example.com", "Bob")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	wsB, _ := store.CreateWorkspace(t.Context(), bob.ID, "Bob ws", "bobws")
	rawB, hashB, _ := auth.IssueRefreshToken()
	famB, err := store.StoreRefreshTokenFamily(t.Context(), bob.ID, wsB.ID, hashB, "", time.Now(), time.Now().Add(time.Hour), "", "")
	if err != nil {
		t.Fatalf("store bob refresh: %v", err)
	}
	refreshB := rawB
	famsB := []auth.SessionFamily{{FamilyID: famB}}

	dr := httptest.NewRequest(http.MethodDelete, "/auth/sessions/"+famsB[0].FamilyID, nil)
	dr.Header.Set("Authorization", "Bearer "+accessA)
	dw := httptest.NewRecorder()
	handler.ServeHTTP(dw, dr)
	if dw.Code != http.StatusNotFound {
		t.Fatalf("cross-user revoke: want 404, got %d", dw.Code)
	}

	if rw := postJSONRefreshCookie(t, mux, "/auth/refresh", refreshB); rw.Code != http.StatusOK {
		t.Fatalf("bob's session should survive: want 200, got %d", rw.Code)
	}
}
