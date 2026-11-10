package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

func TestRecover_TurnsPanicInto500(t *testing.T) {
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	w := httptest.NewRecorder()
	middleware.Recover(panicky).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on panic, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Errorf("body should be a generic error, got %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "boom") {
		t.Error("panic value must not leak to the client")
	}
}

func TestRecover_PassesThroughNormal(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	w := httptest.NewRecorder()
	middleware.Recover(ok).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusTeapot {
		t.Errorf("non-panicking handler should pass through, got %d", w.Code)
	}
}
