package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Checker struct {
	docker *client.Client
	dbs    map[string]*sql.DB
	mu     sync.Mutex
	detail func(*http.Request) bool
}

// The probes are unauthenticated for the LB and kubelet; the full report leaks DB/Docker addresses,
// socket paths and shared host details. The API server must always set a gate.
func (c *Checker) SetDetailGate(allow func(*http.Request) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detail = allow
}

func (c *Checker) showsDetail(r *http.Request) bool {
	c.mu.Lock()
	allow := c.detail
	c.mu.Unlock()
	return allow == nil || allow(r)
}

type summary struct {
	Status string `json:"status"`
}

func New(docker *client.Client, dbs map[string]*sql.DB) *Checker {
	if dbs == nil {
		dbs = make(map[string]*sql.DB)
	}
	return &Checker{docker: docker, dbs: dbs}
}

func (c *Checker) RegisterDB(name string, db *sql.DB) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dbs == nil {
		c.dbs = make(map[string]*sql.DB)
	}
	c.dbs[name] = db
}

type checkResult struct {
	Status  string `json:"status"`
	Latency string `json:"latency,omitempty"`
	Error   string `json:"error,omitempty"`
}

type response struct {
	Status        string                 `json:"status"`
	Timestamp     time.Time              `json:"timestamp"`
	Uptime        string                 `json:"uptime"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	Checks        map[string]checkResult `json:"checks"`
	System        systemInfo             `json:"system"`
}

type systemInfo struct {
	GoVersion  string  `json:"go_version"`
	Goroutines int     `json:"goroutines"`
	DiskFreeGB float64 `json:"disk_free_gb,omitempty"`
}

var startTime = time.Now()

func (c *Checker) HandleFull(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks := make(map[string]checkResult)
	overall := "ok"

	if c.docker != nil {
		t := time.Now()
		_, err := c.docker.Ping(ctx)
		cr := checkResult{Status: "ok", Latency: time.Since(t).Round(time.Millisecond).String()}
		if err != nil {
			cr.Status = "fail"
			cr.Error = err.Error()
			overall = "degraded"
		}
		checks["docker"] = cr
	}

	c.mu.Lock()
	dbSnapshot := make(map[string]*sql.DB, len(c.dbs))
	for k, v := range c.dbs {
		dbSnapshot[k] = v
	}
	c.mu.Unlock()

	for name, db := range dbSnapshot {
		t := time.Now()
		err := db.PingContext(ctx)
		cr := checkResult{Status: "ok", Latency: time.Since(t).Round(time.Millisecond).String()}
		if err != nil {
			cr.Status = "fail"
			cr.Error = err.Error()
			overall = "degraded"
		}
		checks["db:"+name] = cr
	}

	diskFreeGB := diskFree()
	if diskFreeGB < 1.0 {
		checks["disk"] = checkResult{Status: "fail", Error: i18n.TCtx(r.Context(), "health.errors.disk_low")}
		overall = "degraded"
	} else {
		checks["disk"] = checkResult{Status: "ok"}
	}

	status := http.StatusOK
	if overall == "fail" {
		status = http.StatusServiceUnavailable
	}

	if !c.showsDetail(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(summary{Status: overall})
		return
	}

	up := time.Since(startTime).Round(time.Second)
	resp := response{
		Status:        overall,
		Timestamp:     time.Now().UTC(),
		Uptime:        up.String(),
		UptimeSeconds: int64(up / time.Second),
		Checks:        checks,
		System: systemInfo{
			GoVersion:  runtime.Version(),
			Goroutines: runtime.NumGoroutine(),
			DiskFreeGB: diskFreeGB,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (c *Checker) HandleLive(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (c *Checker) HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	notReady := func(reason string) {
		body := map[string]string{"status": "not_ready"}
		if c.showsDetail(r) {
			body["reason"] = reason
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(body)
	}

	if c.docker != nil {
		if _, err := c.docker.Ping(ctx); err != nil {
			notReady("docker: " + err.Error())
			return
		}
	}

	c.mu.Lock()
	dbSnapshot := make(map[string]*sql.DB, len(c.dbs))
	for k, v := range c.dbs {
		dbSnapshot[k] = v
	}
	c.mu.Unlock()
	for name, db := range dbSnapshot {
		if err := db.PingContext(ctx); err != nil {
			notReady("db:" + name + ": " + err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

func diskFree() float64 {
	dir, _ := os.Getwd() //nolint:errcheck // Getwd error means working dir is unavailable; Statfs will fail and return -1
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return -1
	}
	if stat.Bsize <= 0 {
		return -1
	}
	return float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
}
