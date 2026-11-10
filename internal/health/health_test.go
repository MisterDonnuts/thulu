package health_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dockerclient "github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/health"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestHandleLive(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	w := httptest.NewRecorder()
	c.HandleLive(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", resp["status"])
	}
}

func TestHandleReady_NoDocker(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	c.HandleReady(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with no deps configured, got %d", w.Code)
	}
}

func TestHandleFull_NoDocker(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] == nil {
		t.Error("expected status field in response")
	}
	if resp["uptime"] == nil {
		t.Error("expected uptime field in response")
	}
	if secs, ok := resp["uptime_seconds"].(float64); !ok || secs < 0 {
		t.Errorf("uptime_seconds = %v, want a number of seconds", resp["uptime_seconds"])
	}
	if resp["checks"] == nil {
		t.Error("expected checks field in response")
	}
}

func TestHandleFull_ContentType(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}

func TestHandleReady_ContentType(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	c.HandleReady(w, r)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}

func TestRegisterDB_IncludedInHealthCheck(t *testing.T) {
	db := testpg.New(t)

	c := health.New(nil, nil)
	c.RegisterDB("tasks", db)

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	checks, ok := resp["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks field is not a map: %T", resp["checks"])
	}
	if checks["db:tasks"] == nil {
		t.Error("expected db:tasks check in response")
	}
}

func TestHandleReady_WithDB_OK(t *testing.T) {
	db := testpg.New(t)

	c := health.New(nil, map[string]*sql.DB{"tasks": db})
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	c.HandleReady(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("HandleReady with healthy DB: got %d, want 200", w.Code)
	}
}

func TestHandleReady_WithDB_Fail(t *testing.T) {
	db := testpg.New(t)
	db.Close()

	c := health.New(nil, map[string]*sql.DB{"main": db})
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	c.HandleReady(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("HandleReady with closed DB: got %d, want 503", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "not_ready" {
		t.Errorf("status = %q, want not_ready", resp["status"])
	}
}

func TestHandleFull_WithDB_Degraded(t *testing.T) {
	db := testpg.New(t)
	db.Close()

	c := health.New(nil, map[string]*sql.DB{"tasks": db})
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "degraded" {
		t.Errorf("expected degraded status with closed DB, got %q", resp["status"])
	}
	checks, _ := resp["checks"].(map[string]any)
	dbCheck, _ := checks["db:tasks"].(map[string]any)
	if dbCheck["status"] != "fail" {
		t.Errorf("db:tasks check: expected fail, got %q", dbCheck["status"])
	}
}

func TestHandleFull_SystemFields(t *testing.T) {
	c := health.New(nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	sys, _ := resp["system"].(map[string]any)
	if sys["go_version"] == nil {
		t.Error("expected go_version in system info")
	}
	if sys["goroutines"] == nil {
		t.Error("expected goroutines in system info")
	}
}

func TestHandleFull_DockerUnavailable(t *testing.T) {
	dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("NewClientWithOpts: %v", err)
	}
	defer dc.Close()

	c := health.New(dc, nil)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleFull(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("HandleFull with dead docker: got HTTP %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "degraded" {
		t.Errorf("expected overall=degraded, got %q", resp["status"])
	}
	checks, _ := resp["checks"].(map[string]any)
	dockerCheck, _ := checks["docker"].(map[string]any)
	if dockerCheck["status"] != "fail" {
		t.Errorf("docker check: expected fail, got %q", dockerCheck["status"])
	}
}

func TestHandleReady_DockerUnavailable(t *testing.T) {
	dc, err := dockerclient.NewClientWithOpts(dockerclient.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("NewClientWithOpts: %v", err)
	}
	defer dc.Close()

	c := health.New(dc, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	c.HandleReady(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("HandleReady with dead docker: got %d, want 503", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "not_ready" {
		t.Errorf("status = %q, want not_ready", resp["status"])
	}
}

func TestDetailGate_HidesTheReport(t *testing.T) {
	db := testpg.New(t)
	db.Close()

	c := health.New(nil, map[string]*sql.DB{"main": db})
	c.SetDetailGate(func(r *http.Request) bool { return r.Header.Get("X-Operator") == "1" })

	w := httptest.NewRecorder()
	c.HandleFull(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var anon map[string]any
	json.NewDecoder(w.Body).Decode(&anon)
	if len(anon) != 1 || anon["status"] != "degraded" {
		t.Errorf("gated /health = %v, want {status: degraded} alone", anon)
	}

	w = httptest.NewRecorder()
	c.HandleReady(w, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	var ready map[string]string
	json.NewDecoder(w.Body).Decode(&ready)
	if w.Code != http.StatusServiceUnavailable || ready["reason"] != "" {
		t.Errorf("gated /health/ready = %d %v, want 503 with no reason", w.Code, ready)
	}

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.Header.Set("X-Operator", "1")
	w = httptest.NewRecorder()
	c.HandleFull(w, r)
	var full map[string]any
	json.NewDecoder(w.Body).Decode(&full)
	if full["checks"] == nil || full["system"] == nil {
		t.Errorf("allowed /health = %v, want the full report", full)
	}
}
