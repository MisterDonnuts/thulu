package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type bucket struct {
	tokens    float64
	lastRefil time.Time
	mu        sync.Mutex
}

type RateLimiter struct {
	rps   float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

func NewFixedRateLimiter(rps, burst float64) *RateLimiter {
	rl := &RateLimiter{rps: rps, burst: burst, buckets: make(map[string]*bucket)}
	go rl.evictLoop()
	return rl
}

func NewRateLimiter() *RateLimiter {
	rps := parseFloat(os.Getenv("OCTOPUS_RATE_LIMIT_RPS"), 100)
	burst := parseFloat(os.Getenv("OCTOPUS_RATE_LIMIT_BURST"), 200)

	if rps <= 0 && burst <= 0 {
		return nil
	}

	rl := &RateLimiter{
		rps:     rps,
		burst:   burst,
		buckets: make(map[string]*bucket),
	}

	go rl.evictLoop()
	//nolint:gosec // G706: rps and burst are numeric config values, not user-controlled text — no log-injection vector
	log.Printf("[ratelimit] %s", i18n.Log("middleware.log.ratelimit_enabled", "rps", rps, "burst", burst))
	return rl
}

func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	if rl == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}

		if ok, retryAfter := rl.Allow(ClientIP(r)); !ok {
			WriteRateLimited(w, r, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) Allow(key string) (ok bool, retryAfter int) {
	if rl == nil {
		return true, 0
	}
	b := rl.getBucket(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastRefil).Seconds()
	b.tokens = min(rl.burst, b.tokens+elapsed*rl.rps)
	b.lastRefil = now
	if b.tokens < 1 {
		return false, int(1.0/rl.rps) + 1
	}
	b.tokens--
	return true, 0
}

type rateLimitedBody struct {
	apperr.Body
	RetryAfter int `json:"retry_after"`
}

func WriteRateLimited(w http.ResponseWriter, r *http.Request, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	body := apperr.BodyFor(r, apperr.RateLimited("middleware.errors.rate_limited"))
	_ = json.NewEncoder(w).Encode(rateLimitedBody{Body: body, RetryAfter: retryAfter})
}

func NewPerKeyRateLimiter() *RateLimiter {
	rps := parseFloat(os.Getenv("OCTOPUS_RATE_LIMIT_PER_KEY"), 50)
	burst := parseFloat(os.Getenv("OCTOPUS_RATE_LIMIT_PER_KEY_BURST"), 100)
	if rps <= 0 && burst <= 0 {
		return nil
	}
	if rps <= 0 {
		// A burst with no refill would lock a key out for good after its first burst.
		rps = 1
	}
	if burst < 1 {
		burst = max(1, rps)
	}
	rl := &RateLimiter{rps: rps, burst: burst, buckets: make(map[string]*bucket)}
	go rl.evictLoop()
	//nolint:gosec // G706: rps and burst are numeric config values, not user-controlled text — no log-injection vector
	log.Printf("[ratelimit] %s", i18n.Log("middleware.log.ratelimit_per_key", "rps", rps, "burst", burst))
	return rl
}

func (rl *RateLimiter) getBucket(ip string) *bucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &bucket{tokens: rl.burst, lastRefil: time.Now()}
		rl.buckets[ip] = b
	}
	return b
}

func (rl *RateLimiter) evictLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for ip, b := range rl.buckets {
			b.mu.Lock()
			stale := b.lastRefil.Before(cutoff)
			b.mu.Unlock()
			if stale {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}
