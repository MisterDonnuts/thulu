package middleware

import (
	"context"
	"net/http"
	"os"
	"time"
)

// 45s sits above the DSN's 30s per-statement statement_timeout, leaving room for several round
// trips plus a Docker or Kubernetes call.
const DefaultRequestTimeout = 45 * time.Second

func RequestTimeout() time.Duration {
	raw := os.Getenv("OCTOPUS_REQUEST_TIMEOUT")
	if raw == "" {
		return DefaultRequestTimeout
	}
	if raw == "off" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return DefaultRequestTimeout
	}
	return d
}

// Not http.TimeoutHandler: it buffers the whole response, holding 50 MB uploads in RAM and never
// flushing SSE.
func Timeout(d time.Duration, exempt func(*http.Request) bool, next http.Handler) http.Handler {
	if d <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt != nil && exempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
