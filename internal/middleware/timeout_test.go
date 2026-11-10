package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

func deadlineProbe(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.Context().Deadline(); !ok {
		_, _ = w.Write([]byte("none"))
		return
	}
	_, _ = w.Write([]byte("deadline"))
}

func TestTimeout_SetsDeadline(t *testing.T) {
	h := middleware.Timeout(time.Second, nil, http.HandlerFunc(deadlineProbe))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if got := w.Body.String(); got != "deadline" {
		t.Errorf("a normal request should carry a deadline, got %q", got)
	}
}

func TestTimeout_ExemptKeepsContextOpen(t *testing.T) {
	exempt := func(r *http.Request) bool { return r.URL.Path == "/events" }
	h := middleware.Timeout(time.Second, exempt, http.HandlerFunc(deadlineProbe))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events", nil))
	if got := w.Body.String(); got != "none" {
		t.Errorf("an exempt route must keep an open-ended context, got %q", got)
	}
}

func TestTimeout_ZeroDurationDisables(t *testing.T) {
	h := middleware.Timeout(0, nil, http.HandlerFunc(deadlineProbe))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if got := w.Body.String(); got != "none" {
		t.Errorf("a non-positive timeout must be a no-op, got %q", got)
	}
}

func TestTimeout_CancelsSlowHandler(t *testing.T) {
	var ctxErr error
	h := middleware.Timeout(10*time.Millisecond, nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		ctxErr = r.Context().Err()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if ctxErr == nil {
		t.Fatal("the handler's context should have expired")
	}
}

func TestRequestTimeout_Env(t *testing.T) {
	cases := map[string]time.Duration{
		"":        middleware.DefaultRequestTimeout,
		"off":     0,
		"0":       0,
		"90s":     90 * time.Second,
		"garbage": middleware.DefaultRequestTimeout,
	}
	for raw, want := range cases {
		t.Setenv("OCTOPUS_REQUEST_TIMEOUT", raw)
		if got := middleware.RequestTimeout(); got != want {
			t.Errorf("OCTOPUS_REQUEST_TIMEOUT=%q: got %v, want %v", raw, got, want)
		}
	}
}
