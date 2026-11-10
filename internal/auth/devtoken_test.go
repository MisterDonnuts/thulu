package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "dev-token-under-test-0123456789abcdef0123"

func devModeOn(t *testing.T) {
	t.Helper()
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Cleanup(func() { SetDevToken("") })
}

func probe() http.Handler {
	return DevSessionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"dev_session": DevSession(r.Context())})
	}))
}

func TestLoadDevToken(t *testing.T) {
	devModeOn(t)
	t.Setenv(DevTokenEnv, "")
	dir := t.TempDir()

	first, err := LoadDevToken(dir)
	if err != nil || len(first) < minDevTokenLen {
		t.Fatalf("first load = %q, %v", first, err)
	}
	info, err := os.Stat(filepath.Join(dir, devTokenFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file = %v, %v; want 0600", info, err)
	}
	again, err := LoadDevToken(dir)
	if err != nil || again != first {
		t.Fatalf("second load = %q, %v; want the saved %q", again, err, first)
	}

	t.Setenv(DevTokenEnv, testToken)
	if got, err := LoadDevToken(dir); err != nil || got != testToken {
		t.Fatalf("env token = %q, %v", got, err)
	}
	t.Setenv(DevTokenEnv, "short")
	if _, err := LoadDevToken(dir); err == nil {
		t.Fatal("a short env token was accepted")
	}

	t.Setenv("OCTOPUS_DEV_MODE", "")
	if got, err := LoadDevToken(dir); err != nil || got != "" {
		t.Fatalf("outside dev mode = %q, %v; want nothing", got, err)
	}
}

func TestDevSession_LinkSetsTheCookie(t *testing.T) {
	devModeOn(t)
	SetDevToken(testToken)

	w := httptest.NewRecorder()
	probe().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tasks/?id=t1&token="+testToken, nil))

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/tasks/?id=t1" {
		t.Errorf("redirect = %q, want the page without the token", loc)
	}
	var session, csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case CookieDevSession:
			session = c
		case CookieCSRF:
			csrf = c
		}
	}
	if session == nil || session.Value != testToken || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie = %+v", session)
	}
	if csrf == nil || csrf.Value == "" || csrf.HttpOnly {
		t.Errorf("csrf cookie = %+v, want one the dashboard can read", csrf)
	}
}

func TestDevSession_WhoIsLetIn(t *testing.T) {
	devModeOn(t)
	SetDevToken(testToken)

	for _, c := range []struct {
		name   string
		target string
		cookie string
		want   bool
	}{
		{"nothing", "/tasks", "", false},
		{"wrong cookie", "/tasks", "nope", false},
		{"wrong link", "/tasks?token=nope", "", false},
		{"cookie", "/tasks", testToken, true},
	} {
		r := httptest.NewRequest(http.MethodGet, c.target, nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: CookieDevSession, Value: c.cookie})
		}
		w := httptest.NewRecorder()
		probe().ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), map[bool]string{true: `"dev_session":true`, false: `"dev_session":false`}[c.want]) {
			t.Errorf("%s: %d %s, want dev_session=%v", c.name, w.Code, w.Body.String(), c.want)
		}
	}
}

func TestDevSession_NoTokenLetsNobodyIn(t *testing.T) {
	devModeOn(t)
	SetDevToken("")
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.AddCookie(&http.Cookie{Name: CookieDevSession, Value: ""})
	w := httptest.NewRecorder()
	probe().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"dev_session":false`) {
		t.Errorf("no token loaded: %s", w.Body.String())
	}

	SetDevToken(testToken)
	t.Setenv("OCTOPUS_DEV_MODE", "")
	r = httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.AddCookie(&http.Cookie{Name: CookieDevSession, Value: testToken})
	w = httptest.NewRecorder()
	probe().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"dev_session":false`) {
		t.Errorf("outside dev mode: %s", w.Body.String())
	}
}

func TestWithoutDevToken_StaysOnSite(t *testing.T) {
	for _, target := range []string{"//evil.example/?token=x", `/\evil.example?token=x`} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = strings.SplitN(target, "?", 2)[0]
		r.URL.RawQuery = "token=x"
		if got := withoutDevToken(r); got != "/" {
			t.Errorf("%q → %q, want /", target, got)
		}
	}
}

func TestCSRF_CoversTheDevSession(t *testing.T) {
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	r := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	r.AddCookie(&http.Cookie{Name: CookieDevSession, Value: testToken})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("forged post = %d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/tasks", nil)
	r.AddCookie(&http.Cookie{Name: CookieDevSession, Value: testToken})
	r.AddCookie(&http.Cookie{Name: CookieCSRF, Value: "c"})
	r.Header.Set("X-CSRF-Token", "c")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard post = %d, want 200", w.Code)
	}
}
