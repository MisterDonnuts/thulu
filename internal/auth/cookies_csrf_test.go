package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestSetAuthCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	auth.SetAuthCookies(rec, "atok", "rtok", "csrftok", time.Now().Add(time.Hour), true)

	byName := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		byName[c.Name] = c
	}

	at := byName[auth.CookieAccess]
	if at == nil || at.Value != "atok" || !at.HttpOnly || !at.Secure {
		t.Fatalf("access cookie: want HttpOnly+Secure with value atok, got %+v", at)
	}
	rt := byName[auth.CookieRefresh]
	if rt == nil || rt.Value != "rtok" || !rt.HttpOnly || !rt.Secure {
		t.Fatalf("refresh cookie: want HttpOnly+Secure with value rtok, got %+v", rt)
	}
	csrf := byName[auth.CookieCSRF]
	if csrf == nil || csrf.Value != "csrftok" || csrf.HttpOnly || !csrf.Secure {
		t.Fatalf("csrf cookie: want JS-readable+Secure with value csrftok, got %+v", csrf)
	}
}

func TestSetAuthCookiesInsecure(t *testing.T) {
	rec := httptest.NewRecorder()
	auth.SetAuthCookies(rec, "atok", "rtok", "csrftok", time.Now().Add(time.Hour), false)
	for _, c := range rec.Result().Cookies() {
		if c.Secure {
			t.Errorf("cookie %s: want Secure=false over HTTP, got Secure=true", c.Name)
		}
	}
}

func TestJWTMiddleware_CookieFallback(t *testing.T) {
	tok, err := auth.IssueJWT(auth.Claims{
		UserID:    "u1",
		Email:     "a@b.c",
		Role:      auth.RoleViewer,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	var got *auth.Claims
	h := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = auth.ClaimsFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/usage/sessions/x/logs/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: tok})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil || got.UserID != "u1" {
		t.Fatalf("expected claims resolved from cookie, got %+v", got)
	}
}
