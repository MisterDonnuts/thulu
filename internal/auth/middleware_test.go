package auth_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestJWTMiddleware_NoToken_PassesThrough(t *testing.T) {
	reached := false
	var claimsInContext *auth.Claims
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		claimsInContext = auth.ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !reached {
		t.Error("handler not reached without token")
	}
	if claimsInContext != nil {
		t.Error("expected nil claims with no token")
	}
}

func TestJWTMiddleware_ValidToken_InjectsClaims(t *testing.T) {
	claims := auth.Claims{
		UserID: "user-1",
		Email:  "test@example.com",
		Role:   auth.RoleDeveloper,
	}
	token, err := auth.IssueJWT(claims)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	var got *auth.Claims
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = auth.ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if got == nil {
		t.Fatal("expected claims to be injected into context")
	}
	if got.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", got.UserID)
	}
	if got.Email != "test@example.com" {
		t.Errorf("Email = %q", got.Email)
	}
}

func TestJWTMiddleware_InvalidToken_Returns401(t *testing.T) {
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.Header.Set("Authorization", "Bearer invalid.token.here")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid token = %d, want 401", w.Code)
	}
}

func TestJWTMiddleware_InvalidCookie_BrowserNav_RedirectsToLogin(t *testing.T) {
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/usage/?foo=bar", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: "fake.jwt.token"})
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (See Other)", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login/?next=") {
		t.Errorf("Location = %q, want /login/?next=…", loc)
	}
	setCookies := w.Result().Cookies()
	cleared := map[string]bool{}
	for _, c := range setCookies {
		if c.MaxAge < 0 || (c.MaxAge == 0 && c.Expires.IsZero()) {
			cleared[c.Name] = true
		}
	}
	if !cleared[auth.CookieAccess] {
		t.Errorf("expected access cookie cleared in Set-Cookie, got %+v", setCookies)
	}
}

func TestJWTMiddleware_InvalidCookie_XHR_PassesThrough(t *testing.T) {
	reached := false
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if _, err := r.Cookie(auth.CookieAccess); err == nil {
			t.Error("downstream still received the stale cookie")
		}
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/usage/by-engine", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieAccess, Value: "fake.jwt.token"})
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !reached {
		t.Error("XHR request with stale cookie should pass through to next")
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("status = %d, want next-handler's 200 (not 401)", w.Code)
	}
}

func TestJWTMiddleware_InvalidHeader_StillReturns401(t *testing.T) {
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.Header.Set("Authorization", "Bearer invalid.token.here")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("API client invalid token = %d, want 401", w.Code)
	}
}

func TestJWTMiddleware_APIKeyFormat_PassesThrough(t *testing.T) {
	reached := false
	handler := auth.JWTMiddlewareWithStore(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.Header.Set("Authorization", "Bearer anopaquekeywithnodots")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !reached {
		t.Error("API key format should pass through JWTMiddleware")
	}
}

func TestRequireRole_WithoutJWTSecret_FailsClosed(t *testing.T) {
	os.Unsetenv("OCTOPUS_JWT_SECRET")
	os.Unsetenv("OCTOPUS_DEV_MODE")

	reached := false
	handler := auth.RequireRole(auth.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()
	handler(w, r)
	if reached {
		t.Error("without JWT secret and without dev mode, RequireRole must reject anonymous requests")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireRole_DevMode_OnlyTheDevSession(t *testing.T) {
	os.Unsetenv("OCTOPUS_JWT_SECRET")
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	const tok = "dev-token-for-requirerole-0123456789abcdef"
	auth.SetDevToken(tok)
	t.Cleanup(func() { auth.SetDevToken("") })

	handler := auth.DevSessionMiddleware(auth.RequireRole(auth.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, c := range []struct {
		name   string
		cookie string
		want   int
	}{
		{"no cookie", "", http.StatusUnauthorized},
		{"wrong cookie", "guess", http.StatusUnauthorized},
		{"dev session", tok, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/settings", nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.CookieDevSession, Value: c.cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestRequireRole_MissingClaims_Returns401(t *testing.T) {
	old := os.Getenv("OCTOPUS_JWT_SECRET")
	os.Setenv("OCTOPUS_JWT_SECRET", "testkeytestkeytestkey")
	t.Cleanup(func() { os.Setenv("OCTOPUS_JWT_SECRET", old) })

	handler := auth.RequireRole(auth.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no claims = %d, want 401", w.Code)
	}
}

func TestRequireRole_InsufficientRole_Returns403(t *testing.T) {
	old := os.Getenv("OCTOPUS_JWT_SECRET")
	os.Setenv("OCTOPUS_JWT_SECRET", "testkeytestkeytestkey")
	t.Cleanup(func() { os.Setenv("OCTOPUS_JWT_SECRET", old) })

	handler := auth.RequireRole(auth.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	viewerClaims := &auth.Claims{UserID: "u1", Role: auth.RoleViewer}
	r := httptest.NewRequest(http.MethodGet, "/settings", nil)
	r = r.WithContext(auth.WithClaims(r.Context(), viewerClaims))
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer calling admin endpoint = %d, want 403", w.Code)
	}
}

func TestRequireRole_SufficientRole_Passes(t *testing.T) {
	old := os.Getenv("OCTOPUS_JWT_SECRET")
	os.Setenv("OCTOPUS_JWT_SECRET", "testkeytestkeytestkey")
	t.Cleanup(func() { os.Setenv("OCTOPUS_JWT_SECRET", old) })

	reached := false
	handler := auth.RequireRole(auth.RoleDeveloper, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	adminClaims := &auth.Claims{UserID: "u1", Role: auth.RoleAdmin}
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r = r.WithContext(auth.WithClaims(r.Context(), adminClaims))
	w := httptest.NewRecorder()
	handler(w, r)
	if !reached {
		t.Error("admin should pass developer-level check")
	}
}

func TestHandleMe_WithoutToken_Returns401(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := auth.JWTMiddlewareWithStore(nil, mux)

	r := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /auth/me without token = %d, want 401", w.Code)
	}
}
