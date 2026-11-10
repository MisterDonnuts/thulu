package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/MisterDonnuts/thulu/internal/middleware"
	"github.com/MisterDonnuts/thulu/web"
)

func testSPAHandler() http.Handler {
	return newSPAHandler(fstest.MapFS{
		"dist/index.html":       {Data: []byte("ROOT_DASHBOARD")},
		"dist/login/index.html": {Data: []byte("LOGIN_PAGE")},
		"dist/tasks/index.html": {Data: []byte("TASKS_PAGE")},
		"dist/_astro/app.css":   {Data: []byte("CSS_ASSET")},
	})
}

func serveSPA(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	w.Header().Set("Content-Security-Policy", middleware.CSPStrict)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	h.ServeHTTP(w, r)
	return w
}

func TestSPAHandler_RoutesResolveToOwnPage(t *testing.T) {
	h := testSPAHandler()
	cases := []struct {
		path string
		want string
	}{
		{"/", "ROOT_DASHBOARD"},
		{"/login", "LOGIN_PAGE"},
		{"/login/", "LOGIN_PAGE"},
		{"/tasks/", "TASKS_PAGE"},
		{"/_astro/app.css", "CSS_ASSET"},
		{"/unknown/client/route", "ROOT_DASHBOARD"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := serveSPA(t, h, tc.path)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			body, _ := io.ReadAll(w.Body)
			if string(body) != tc.want {
				t.Errorf("body = %q, want %q", body, tc.want)
			}
		})
	}
}

func TestSPAHandler_OverridesCSPForDashboard(t *testing.T) {
	h := testSPAHandler()
	for _, path := range []string{"/", "/_astro/app.css", "/tasks/"} {
		t.Run(path, func(t *testing.T) {
			w := serveSPA(t, h, path)
			if got := w.Header().Get("Content-Security-Policy"); got != middleware.CSPWebApp {
				t.Errorf("CSP = %q, want web-app policy %q", got, middleware.CSPWebApp)
			}
		})
	}
}

func TestSPAHandler_WritesToUnknownPathsAre404(t *testing.T) {
	h := testSPAHandler()
	for _, method := range []string{
		http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/projects/p1/environments/e1/credential", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s to a retired route = %d, want 404", method, w.Code)
		}
		if strings.Contains(w.Body.String(), "<html") {
			t.Errorf("%s to a retired route was answered with the dashboard HTML", method)
		}
	}
}

func TestSPAHandler_RetiredAuthReadsAre404(t *testing.T) {
	h := testSPAHandler()
	for _, path := range []string{"/auth/login", "/auth/mfa/status", "/auth/webauthn/login/begin"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "<html") {
			t.Errorf("GET %s was answered with the dashboard HTML", path)
		}
	}
}

func TestSPAHandler_GetStillFallsBack(t *testing.T) {
	h := testSPAHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET on an unknown route = %d, want the SPA shell", w.Code)
	}
}

func TestSPAHandler_CacheControlSplitsShellFromAssets(t *testing.T) {
	h := testSPAHandler()

	for _, path := range []string{"/", "/login/", "/tasks/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache so a deploy is picked up", path, got)
		}
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/_astro/app.css", nil))
	got := w.Header().Get("Cache-Control")
	if !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=") {
		t.Errorf("hashed asset Cache-Control = %q, want a long immutable cache", got)
	}
}

func TestSPAHandler_FallbackIsNotCached(t *testing.T) {
	h := testSPAHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/project/deep/link", nil))
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("SPA fallback Cache-Control = %q, want no-cache", got)
	}
}

// /.well-known/security.txt ships in web/public and has to come back as the
// file itself, from the real embedded build: a dot-directory is dropped by a
// plain //go:embed pattern (web/embed.go uses all:dist for this reason) and
// would otherwise fall through to the dashboard shell.
func TestSPAHandler_ServesSecurityTxt(t *testing.T) {
	h := newSPAHandler(web.DistFS)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(w.Body)
	for _, field := range []string{"Contact: ", "Expires: ", "Policy: "} {
		if !strings.Contains(string(body), field) {
			t.Errorf("security.txt lacks %q:\n%s", field, body)
		}
	}
}

func TestSPAHandler_ServesPageInRequestLocale(t *testing.T) {
	h := newSPAHandler(fstest.MapFS{
		"dist/index.html":                   {Data: []byte("ROOT_EN")},
		"dist/tasks/index.html":             {Data: []byte("TASKS_EN")},
		"dist/login/index.html":             {Data: []byte("LOGIN_EN")},
		"dist/_astro/app.css":               {Data: []byte("CSS")},
		"dist/_locales/es/index.html":       {Data: []byte("ROOT_ES")},
		"dist/_locales/es/tasks/index.html": {Data: []byte("TASKS_ES")},
	})
	cases := []struct{ path, lang, cookie, want string }{
		{"/tasks/", "es-AR,es;q=0.9", "", "TASKS_ES"},
		{"/tasks/", "es", "octopus_locale=en", "TASKS_EN"},
		{"/", "es", "", "ROOT_ES"},
		{"/login/", "es", "", "LOGIN_EN"},
		{"/_astro/app.css", "es", "", "CSS"},
		{"/tasks/", "", "", "TASKS_EN"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.lang != "" {
			r.Header.Set("Accept-Language", tc.lang)
		}
		if tc.cookie != "" {
			r.Header.Set("Cookie", tc.cookie)
		}
		h.ServeHTTP(w, r)
		if body := w.Body.String(); body != tc.want {
			t.Errorf("%s (lang %q, cookie %q) = %q, want %q", tc.path, tc.lang, tc.cookie, body, tc.want)
		}
	}
}

func TestLegalPages_ServedWithoutCredentials(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	*srv.spa = *newSPAHandler(fstest.MapFS{
		"dist/index.html":               {Data: []byte("ROOT")},
		"dist/legal/terms/index.html":   {Data: []byte("TERMS_PAGE")},
		"dist/legal/privacy/index.html": {Data: []byte("PRIVACY_PAGE")},
	})
	for path, want := range map[string]string{
		"/legal/terms/":   "TERMS_PAGE",
		"/legal/terms":    "TERMS_PAGE",
		"/legal/privacy/": "PRIVACY_PAGE",
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, w.Code, w.Body.String(), want)
		}
	}
}
