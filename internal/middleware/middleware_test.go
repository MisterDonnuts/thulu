package middleware_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestSecurityHeaders_Present(t *testing.T) {
	handler := middleware.SecurityHeaders(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)

	cases := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for header, want := range cases {
		got := w.Header().Get(header)
		if got != want {
			t.Errorf("%s: got %q, want %q", header, got, want)
		}
	}
}

func TestSecurityHeaders_StrictCSPByDefault(t *testing.T) {
	handler := middleware.SecurityHeaders(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Content-Security-Policy"); got != middleware.CSPStrict {
		t.Errorf("CSP = %q, want strict policy %q", got, middleware.CSPStrict)
	}
}

func cspDirectives(policy string) map[string][]string {
	out := map[string][]string{}
	for _, d := range strings.Split(policy, ";") {
		f := strings.Fields(d)
		if len(f) > 0 {
			out[f[0]] = f[1:]
		}
	}
	return out
}

func TestCSPWebApp_NoInlineScriptNoThirdParties(t *testing.T) {
	d := cspDirectives(middleware.CSPWebApp)

	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src-elem":  "'self'",
		"font-src":        "'self'",
		"connect-src":     "'self'",
		"object-src":      "'none'",
		"base-uri":        "'self'",
		"frame-ancestors": "'none'",
	}
	for name, sources := range want {
		got, ok := d[name]
		if !ok {
			t.Errorf("%s missing from %q", name, middleware.CSPWebApp)
			continue
		}
		if strings.Join(got, " ") != sources {
			t.Errorf("%s = %q, want %q", name, strings.Join(got, " "), sources)
		}
	}

	for name, sources := range d {
		for _, s := range sources {
			if strings.Contains(s, "://") || strings.HasPrefix(s, "https:") || s == "*" {
				t.Errorf("%s allows another origin: %q", name, s)
			}
			if name != "style-src" && (s == "'unsafe-inline'" || s == "'unsafe-eval'") {
				t.Errorf("%s allows %s", name, s)
			}
		}
	}
}

func TestSecurityHeaders_HSTSOnlyOnDirectTLS(t *testing.T) {
	handler := middleware.SecurityHeaders(http.HandlerFunc(okHandler))

	plain := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	plain.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, plain)
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HTTP (even with X-Forwarded-Proto) got HSTS %q", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "https://octopus.example/tasks", nil)
	if direct.TLS == nil {
		t.Fatal("httptest did not mark the https request as TLS")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, direct)
	if got := w.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Errorf("direct TLS: HSTS = %q, want max-age=31536000", got)
	}
}

func TestRequestBodyLimit_SmallBodyPasses(t *testing.T) {
	handler := middleware.RequestBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	body := strings.Repeat("a", 1024)
	r := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequestBodyLimit_GetSkipped(t *testing.T) {
	handler := middleware.RequestBodyLimit(http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequestID_Generated(t *testing.T) {
	handler := middleware.RequestID(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)
	if id := w.Header().Get("X-Request-ID"); id == "" {
		t.Error("expected X-Request-ID to be set")
	}
}

func TestRequestID_EchoClientID(t *testing.T) {
	handler := middleware.RequestID(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "client-req-id")
	handler.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); got != "client-req-id" {
		t.Errorf("expected echoed request ID, got %q", got)
	}
}

func TestCORS_AllowedOrigin(t *testing.T) {
	os.Setenv("OCTOPUS_ALLOWED_ORIGINS", "http://localhost:4321")
	defer os.Unsetenv("OCTOPUS_ALLOWED_ORIGINS")

	handler := middleware.CORS(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "http://localhost:4321")
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:4321" {
		t.Errorf("expected allowed origin, got %q", got)
	}
}

func TestCORS_DisallowedOrigin(t *testing.T) {
	os.Setenv("OCTOPUS_ALLOWED_ORIGINS", "http://localhost:4321")
	defer os.Unsetenv("OCTOPUS_ALLOWED_ORIGINS")

	handler := middleware.CORS(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "http://evil.example.com")
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no ACAO header for disallowed origin, got %q", got)
	}
}

func TestCORS_Preflight(t *testing.T) {
	os.Setenv("OCTOPUS_ALLOWED_ORIGINS", "http://localhost:4321")
	defer os.Unsetenv("OCTOPUS_ALLOWED_ORIGINS")

	handler := middleware.CORS(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/api/tasks", nil)
	r.Header.Set("Origin", "http://localhost:4321")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight: expected 204, got %d", w.Code)
	}
}

func TestRateLimiter_HealthProbesNotLimited(t *testing.T) {
	os.Setenv("OCTOPUS_RATE_LIMIT_RPS", "1")
	os.Setenv("OCTOPUS_RATE_LIMIT_BURST", "1")
	defer os.Unsetenv("OCTOPUS_RATE_LIMIT_RPS")
	defer os.Unsetenv("OCTOPUS_RATE_LIMIT_BURST")

	rl := middleware.NewRateLimiter()
	handler := rl.Handler(http.HandlerFunc(okHandler))

	r1 := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r1.RemoteAddr = "1.2.3.4:9000"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	for _, path := range []string{"/health", "/health/live", "/health/ready"} {
		rh := httptest.NewRequest(http.MethodGet, path, nil)
		rh.RemoteAddr = "1.2.3.4:9000"
		wh := httptest.NewRecorder()
		handler.ServeHTTP(wh, rh)
		if wh.Code == http.StatusTooManyRequests {
			t.Errorf("%s: got 429 but health probes must never be rate-limited", path)
		}
	}
}

func TestLogger_WritesStatusAndBody(t *testing.T) {
	handler := middleware.RequestID(middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("hello"))
	})))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("Logger: got %d, want 201", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "hello" {
		t.Errorf("Logger: body = %q, want hello", body)
	}
}

func TestLogger_HealthProbesPassThrough(t *testing.T) {
	called := false
	handler := middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/health", "/health/live", "/health/ready"} {
		called = false
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(w, r)
		if !called {
			t.Errorf("%s: inner handler should be called", path)
		}
	}
}

type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushableRecorder) Flush() { f.flushed = true }

func TestLogger_PropagatesFlusher(t *testing.T) {
	handler := middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("handler: ResponseWriter must implement http.Flusher through Logger wrapper")
		}
		w.WriteHeader(http.StatusOK)
		f.Flush()
	}))

	rec := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sandbox/sse", nil))

	if !rec.flushed {
		t.Error("Logger wrapper did not propagate Flush() to underlying writer")
	}
}

func TestMaskSecrets(t *testing.T) {
	cases := []struct {
		in     string
		redact bool
	}{
		{"Authorization: Bearer abc123", true},
		{"X-API-Key: mykey", true},
		{"token=secret", true},
		{"api_key=abc", true},
		{"password=hunter2", true},
		{"normal log message", false},
		{"", false},
	}
	for _, c := range cases {
		got := middleware.MaskSecrets(c.in)
		if c.redact && got != "***redacted***" {
			t.Errorf("MaskSecrets(%q) = %q, want redacted", c.in, got)
		}
		if !c.redact && got != c.in {
			t.Errorf("MaskSecrets(%q) = %q, want unchanged", c.in, got)
		}
	}
}

func TestRateLimiter_XForwardedFor(t *testing.T) {
	limiter := middleware.NewRateLimiter()
	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 192.168.1.1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("XFF status = %d, want 200", w.Code)
	}
}

func TestCORS_WildcardAllowsAll(t *testing.T) {
	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "*")
	handler := middleware.CORS(http.HandlerFunc(okHandler))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://anything.example.com")
	handler.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS wildcard: got %q, want *", got)
	}
}

func TestRateLimiter_ExceedsLimit_Returns429(t *testing.T) {
	t.Setenv("OCTOPUS_RATE_LIMIT_RPS", "1")
	t.Setenv("OCTOPUS_RATE_LIMIT_BURST", "1")

	rl := middleware.NewRateLimiter()
	handler := rl.Handler(http.HandlerFunc(okHandler))

	r1 := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r1.RemoteAddr = "5.6.7.8:1234"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r2.RemoteAddr = "5.6.7.8:1234"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when rate limited, got %d", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header")
	}
}

func TestRateLimiter_NilHandler(t *testing.T) {
	var rl *middleware.RateLimiter
	handler := rl.Handler(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("nil RateLimiter: got %d, want 200", w.Code)
	}
}

func TestRateLimiter_ClientIP_NoXFF(t *testing.T) {
	rl := middleware.NewRateLimiter()
	handler := rl.Handler(http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.RemoteAddr = "192.168.1.1"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("no-port RemoteAddr: got %d, want 200", w.Code)
	}
}

func TestCORS_WildcardPreflight(t *testing.T) {
	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "*")
	handler := middleware.CORS(http.HandlerFunc(okHandler))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/api/tasks", nil)
	r.Header.Set("Origin", "https://anything.example.com")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("CORS wildcard OPTIONS: got %d, want 204", w.Code)
	}
}

func TestRateLimiter_NegativeRPS_UsesDefault(t *testing.T) {
	t.Setenv("OCTOPUS_RATE_LIMIT_RPS", "-10")
	t.Setenv("OCTOPUS_RATE_LIMIT_BURST", "-5")
	rl := middleware.NewRateLimiter()
	if rl == nil {
		t.Error("NewRateLimiter with negative env vars: got nil")
	}
	handler := rl.Handler(http.HandlerFunc(okHandler))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.RemoteAddr = "9.9.9.9:1111"
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("negative RPS: expected 200 on first request, got %d", w.Code)
	}
}

func TestRequestBodyLimit_ProjectUpload(t *testing.T) {
	handler := middleware.RequestBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	big := bytes.Repeat([]byte("a"), 60<<20)
	for path, want := range map[string]int{
		"/projects/upload":       http.StatusOK,
		"/projects/p-1/upload":   http.StatusOK,
		"/tasks/t-1/attachments": http.StatusRequestEntityTooLarge,
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(big)))
		if w.Code != want {
			t.Errorf("POST %s with 60 MB = %d, want %d", path, w.Code, want)
		}
	}
}

func TestRequestBodyLimit_AttachmentPath(t *testing.T) {
	handler := middleware.RequestBodyLimit(http.HandlerFunc(okHandler))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tasks/123/attachments", bytes.NewReader([]byte(`{}`)))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("attachment path POST: got %d, want 200", w.Code)
	}
}

func TestNewRateLimiter_BothZeroReturnsNil(t *testing.T) {
	t.Setenv("OCTOPUS_RATE_LIMIT_RPS", "0")
	t.Setenv("OCTOPUS_RATE_LIMIT_BURST", "0")
	rl := middleware.NewRateLimiter()
	if rl != nil {
		t.Error("expected nil when both RPS and burst are 0")
	}
}
