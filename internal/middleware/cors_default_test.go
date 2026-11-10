package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

func corsAllows(origin string) bool {
	h := middleware.CORS(http.HandlerFunc(okHandler))
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Header().Get("Access-Control-Allow-Origin") == origin
}

func TestCORS_DefaultIsSameOriginOnlyInProduction(t *testing.T) {
	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "")
	t.Setenv("OCTOPUS_DEV_MODE", "")
	for _, o := range []string{"http://localhost:4321", "http://localhost:3000", "http://localhost:5173"} {
		if corsAllows(o) {
			t.Errorf("production default admitted %s", o)
		}
	}
}

func TestCORS_DevModeKeepsDevServerOrigins(t *testing.T) {
	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "")
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if !corsAllows("http://localhost:4321") {
		t.Error("dev mode lost the Astro dev server origin")
	}
	if corsAllows("https://evil.example") {
		t.Error("dev mode admitted an arbitrary origin")
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	if corsAllows("http://localhost:4321") {
		t.Error("dev-server origins admitted inside Kubernetes")
	}
}

func TestCORS_ExplicitAllowlistStillWorksWithoutDevMode(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "")
	t.Setenv("OCTOPUS_ALLOWED_ORIGINS", "https://thulu.acme.com")
	if !corsAllows("https://thulu.acme.com") {
		t.Error("explicit origin refused")
	}
}
