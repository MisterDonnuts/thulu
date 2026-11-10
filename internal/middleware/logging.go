package middleware

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

var sensitivePatterns = []string{
	"authorization",
	"x-api-key",
	"bearer ",
	"token=",
	"api_key=",
	"apikey=",
	"secret=",
	"password=",
	"passwd=",
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// SSE handlers assert w.(http.Flusher); the embedded interface does not lift Flush onto the
// wrapper.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

var jsonLogs = os.Getenv("OCTOPUS_LOG_FORMAT") == "json"

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isProbe := r.URL.Path == "/health" || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready"
		if isProbe {
			next.ServeHTTP(w, r)
			return
		}

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)
		elapsed := time.Since(start)

		reqID := w.Header().Get("X-Request-ID")
		ip := ClientIP(r)

		// A token pasted into a URL path must not survive into the request log.
		path := MaskSecrets(r.URL.Path)

		if jsonLogs {
			slog.Info(i18n.Log("middleware.log.request"),
				slog.String("method", r.Method),
				slog.String("path", path),
				slog.Int("status", rw.status),
				slog.Int("bytes", rw.bytes),
				slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
				slog.String("request_id", reqID),
				slog.String("ip", ip),
			)
		} else {
			//nolint:gosec // G706: fields sanitized with logx.Safe (strips control chars); gosec taint can't track custom sanitizers
			log.Printf("[api] %s", i18n.Log("middleware.log.request_line",
				"method", logx.Safe(r.Method), "path", logx.Safe(path), "status", rw.status,
				"duration", elapsed.Round(time.Millisecond), "request_id", reqID, "ip", logx.Safe(ip),
			))
		}
	})
}

func MaskSecrets(s string) string {
	lower := strings.ToLower(s)
	for _, pat := range sensitivePatterns {
		if strings.Contains(lower, pat) {
			return "***redacted***"
		}
	}
	return s
}
